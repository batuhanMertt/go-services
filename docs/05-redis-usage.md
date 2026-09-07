# 05 — Redis Kullanım Stratejisi

Redis bu platformda **beş** ayrı işi görür. Hepsinde ortak olan tasarım
ilkesi şudur:

> **Redis hiçbir yerde tek doğruluk kaynağı (source of truth) değildir.**
> Her Redis kullanımının arkasında ya PostgreSQL vardır ya da kaybı
> tolere edilebilir bir veridir.

Bu ilke, Redis'in bir SPOF'a dönüşmesini yapısal olarak engeller (NFR-1.2).

---

## Kullanım alanları

| # | Kullanım | Anahtar deseni | TTL | Redis düşerse |
| :-- | :--- | :--- | :--- | :--- |
| 1 | Rate limit (token bucket) | `rl:{scope}:{id}` | `ceil(burst/rate)+2` sn | Fail-open (yapılandırılabilir) |
| 2 | Dağıtık kilit | `lock:delivery:{task_id}` | 30 sn | Fail-open; DB durum makinesi korur |
| 3 | Routing kural cache | `rules:{gen}:{type}@{v}` | 60 sn | Doğrudan Postgres'ten okunur |
| 4 | Cache invalidation sayacı | `rules:gen` | yok | TTL ile en geç 60 sn'de tazelenir |
| 5 | Circuit breaker durumu | *(bkz. §5)* | — | Yerel bellekte tutulur |

---

## 1. Rate limiting — atomik token bucket

**Uygulama:** `internal/ratelimit/ratelimit.go`

Tüm kova mantığı **tek bir Lua script'i** içinde çalışır.

### Neden Lua?

"Oku → hesapla → yaz" üç ayrı Redis komutu olsaydı, iki eşzamanlı istek
arasında yarış oluşur ve limit aşılabilirdi (TOCTOU). Redis Lua script'leri
tek iş parçacığında **atomik** çalışır. Ayrıca `INCR` + `EXPIRE` ikilisinden
farklı olarak kısmi durum (sayaç var ama TTL yok) oluşamaz.

### Neden token bucket, sabit pencere değil?

Sabit pencerede (fixed window) pencere sınırında **2× burst** geçebilir:
100 istek 00:00:59'da + 100 istek 00:01:00'da = 1 saniyede 200 istek.
Token bucket hem düzgün akış sağlar hem de kontrollü burst'e izin verir.

Sliding window log daha hassastır ama her istek için bir zaman damgası
saklamayı gerektirir — bellek maliyeti burada gereksizdir.

### Neden Redis'in `TIME` komutu?

Zaman kaynağı olarak uygulama saati kullanılsaydı, birden fazla ingest
instance'ının saatleri kaydığında limit tutarsız olurdu. Redis `TIME`
tüm istemciler için tek bir referans saat verir.

### Kapsamlar

| Kapsam | Varsayılan | Amaç |
| :--- | :--- | :--- |
| `ip` | 200 rps / 400 burst | Kimliği doğrulanmamış kaba kuvvet trafiği |
| `tool` | 100 rps / 200 burst | Tek tool'un platformu doldurmasını engelleme |
| `admin_ip` | 20 rps / 40 burst | Login brute-force koruması |
| `admin_user` | 20 rps / 40 burst | Yönetim API'si aşırı kullanımı |

### Fail-open kararı

Varsayılan `RATE_LIMIT_FAIL_CLOSED=false`.

Rate limit bir **güvenlik duvarı değil, kaynak koruma mekanizmasıdır.**
Redis düştüğünde tüm platformu durdurmak, geçici olarak aşırı yüke izin
vermekten çok daha büyük hasardır. Kritik ortamlarda tek env değişkeniyle
fail-closed'a alınabilir.

**Doğrulandı:** limit 2 rps / 3 burst'e düşürüldüğünde 12 istekten 3'ü
geçti, 9'u `429` + `Retry-After` aldı.

---

## 2. Dağıtık kilit — mükerrer webhook engelleme

**Uygulama:** `internal/lock/lock.go`

### Problem

`events.delivery` topic'i at-least-once tüketilir. Bir consumer mesajı
işlerken çökerse (offset commit edilmeden) mesaj yeniden dağıtılır.
Rebalance sırasında da aynı mesaj iki worker'a düşebilir. Kilit olmadan
subscriber aynı webhook'u iki kez alır.

### Çözüm

`SET NX PX` ile teslimat görevi id'si üzerinde kısa ömürlü kilit.

Serbest bırakma **Lua ile atomik** yapılır:

```lua
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
```

**Neden token karşılaştırması?** Düz `DEL key` kullanılsaydı: A kilidi alır,
işi TTL'den uzun sürer, kilit kendiliğinden düşer, B kilidi alır, sonra A
işini bitirip `DEL` çağırır ve **B'nin kilidini siler**. Token kontrolü bunu
imkânsız kılar.

### Kilit tek savunma hattı DEĞİLDİR

Kilit bir **optimizasyondur**. Kalıcı idempotency garantisi
`delivery_tasks` tablosundaki koşullu UPDATE'ten gelir:

```sql
UPDATE hub.delivery_tasks
SET status = 'in_flight', attempt_count = attempt_count + 1
WHERE id = $1 AND status IN ('pending', 'in_flight')
```

Zaten `succeeded` olan bir görev bu koşulu geçemez. Redis tamamen düşse bile
veri bütünlüğü DB seviyesinde korunur.

### Neden tek instance, Redlock değil?

Redlock algoritması 5 bağımsız Redis düğümü gerektirir ve kendi içinde
tartışmalıdır (Martin Kleppmann'ın eleştirisi). Bizim kilidimiz "doğruluk
için zorunlu" değil "verimlilik için faydalı" kategorisindedir — bu tam
olarak Redlock'a **gerek olmayan** kullanım sınıfıdır.

---

## 3. Routing kural cache — üç katmanlı

**Uygulama:** `services/routing-engine/cache.go`

```
1) Yerel bellek   → sıfır ağ maliyeti, sıcak yol
2) Redis          → instance'lar arası paylaşım, soğuk başlangıcı hızlandırır
3) PostgreSQL     → tek doğruluk kaynağı
```

Her event için Postgres'e gitmek, routing engine'i saniyede birkaç yüz
event'te tıkardı. Kurallar nadiren değişir, çok sık okunur — klasik cache
senaryosu.

**Redis erişilemezse 2. katman sessizce atlanır**, doğrudan Postgres'ten
okunur. Performans düşer, işlevsellik düşmez.

---

## 4. Cache invalidation — generation sayacı

Admin bir kuralı değiştirdiğinde `INCR rules:gen` çalışır. Routing engine
bu sayacı ~2 saniyede bir yoklar; değiştiyse **tüm yerel önbelleğini boşaltır**.

### Neden sayaç, anahtar silme değil?

Kurallar event tipi bazında önbelleklenir. Bir aboneliği değiştirmenin hangi
event tiplerini etkilediğini admin-api'de yeniden hesaplamak gerekirdi.
Tek bir sayacı artırmak **O(1)**'dir. Redis anahtarları da generation'ı
içerdiği için (`rules:{gen}:{type}`) eski nesil anahtarlar erişilemez hâle
gelir ve TTL ile kendiliğinden temizlenir — pahalı `SCAN` + `DEL` gerekmez.

### İki katmanlı güvence

| Mekanizma | Gecikme | Redis gerekir mi? |
| :--- | :--- | :--- |
| Generation sayacı | ~2 sn | Evet |
| TTL (60 sn) | ≤60 sn | Hayır |

Redis düşse bile kurallar en geç 60 saniyede tazelenir.

---

## 5. Circuit breaker durumu

**Uygulama:** `internal/breaker/breaker.go`

Durum **her worker instance'ının belleğindedir**, Redis'te paylaşılmaz.

**Neden?** Breaker yerel bir kaynak koruma mekanizmasıdır; her instance kendi
worker havuzunu korur. Paylaşımlı durum, her teslimat kararı için bir ağ
gidiş-dönüşü eklerdi ve breaker'ın varlık sebebi olan **"hızlı başarısızlık"**
özelliğini zayıflatırdı.

Bedeli: 3 instance varsa devrenin açılması için toplam 15 hata gerekir (her
birinde 5). Kabul edilebilir — Redis anahtar alanı `redisx.NSBreaker` olarak
ayrılmıştır, ihtiyaç doğarsa paylaşıma geçiş hazırdır.

---

## TTL stratejisi özeti

| Anahtar | TTL | Neden bu değer? |
| :--- | :--- | :--- |
| `rl:*` | `ceil(burst/rate)+2` sn | Kova dolana kadar geçen süre + emniyet payı. Atıl anahtarlar kendiliğinden silinir. |
| `lock:delivery:*` | 30 sn | Webhook timeout'undan (10 sn) uzun; worker çökerse kilit sonsuza dek kalmaz. |
| `rules:*` | 60 sn | Kural değişiminin en kötü ihtimalle yansıma süresi. |
| `rules:gen` | TTL yok | Kalıcı sayaç; kaybolursa 0'dan başlar ve bir kez fazladan invalidation olur (zararsız). |

---

## SPOF risk analizi

| Senaryo | Etki | Azaltma |
| :--- | :--- | :--- |
| Redis çöker | Rate limit devre dışı, kilit yok, cache miss | Tüm yollar Postgres'e düşer; **sistem çalışmaya devam eder** |
| Redis bellek dolar | Yeni anahtar yazılamaz | `maxmemory-policy volatile-lru` → TTL'li anahtarlar atılır. `noeviction` seçilseydi rate-limit yazımları hata verirdi |
| Redis yavaşlar | Ingest gecikmesi artar | İstemci `ReadTimeout: 3s`; aşımda fail-open |
| Redis verisi kaybolur | Rate limit sayaçları sıfırlanır | Kabul edilebilir — sayaçlar yeniden üretilebilir |

### Yapılandırma kararları (`infra/redis/redis.conf`)

- `appendonly yes` — Rate limit sayaçları kaybolabilir, ancak rule cache'in
  soğuk başlangıcını yumuşatmak için AOF açık tutuldu.
- `maxmemory 192mb` — Container limiti 256M; kalan overhead'e bırakıldı.
- `maxmemory-policy volatile-lru` — Cache + kilit karışık kullanım.
- `rename-command FLUSHALL/FLUSHDB/CONFIG/SHUTDOWN ""` — Yıkıcı komutlar
  kapatıldı (least privilege).
- Parola **runtime'da** verilir (`--requirepass`), config dosyasında değil —
  `docker inspect` ile sızmasın.

---

## Redis'in KULLANILMADIĞI yerler

Bir teknolojiyi nerede kullanmadığını bilmek, nerede kullandığını bilmek
kadar önemlidir.

| İhtiyaç | Neden Redis değil? |
| :--- | :--- |
| Event kuyruğu | Kafka: kalıcılık, replay, consumer group, partition ordering |
| Idempotency kaydı | Postgres: kalıcı olmak **zorunda**; Redis'te TTL dolarsa mükerrer event kabul edilir |
| Teslimat görev durumu | Postgres: ACID ve sorgulanabilirlik gerekir; admin panelinde görünmeli |
| Oturum saklama | JWT stateless + refresh token DB'de; Redis'e oturum koymak gereksiz bağımlılık olurdu |
