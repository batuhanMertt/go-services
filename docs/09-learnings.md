# 09 — Öğrenim Retrospektifi

> Bu doküman, projeyi bitirdikten sonra geriye dönüp yazılmıştır. Amaç
> "ne yaptım" değil, **"ne öğrendim ve tekrar yapsam neyi değiştirirdim"**
> sorusunu dürüstçe yanıtlamak.

---

## 1. En değerli üç ders

### 1.1 "At-least-once + idempotent alıcı" gerçek bir mühendislik kararıdır

Başlarken exactly-once teslimatın bir yapılandırma seçeneği olduğunu
sanıyordum. Değil.

Dağıtık bir sistemde "mesajı gönderdim" ile "alıcı işledi" arasında her zaman
bir pencere vardır; o pencerede çökme olabilir. Gönderen ya tekrar dener
(→ mükerrer) ya denemez (→ kayıp). Üçüncü bir seçenek yok.

Bu kabul edildiğinde tasarım netleşti: **mükerrerliği kabul et, zararsız hâle
getir.** Bu projede üç ayrı katmanda uygulandı:

| Katman | Mekanizma |
| :--- | :--- |
| Ingest | `event_instances(event_id)` UNIQUE + saklanan yanıt |
| Routing | `delivery_tasks(event_instance_id, subscription_id)` UNIQUE |
| Delivery | Redis kilidi + koşullu `UPDATE ... WHERE status IN (...)` |

**Anlamlı olan detay:** Bu üç mekanizmanın hiçbiri diğerine güvenmiyor. Redis
düşse bile DB kısıtı devrede. Bu "defense in depth"in veri bütünlüğü
versiyonu.

### 1.2 Bir deseni "uygulamak" ile "anlamak" arasında büyük fark var

**Transactional Outbox**'ı ilk yazdığımda "iki tabloya yaz, sonra Kafka'ya
bas" olarak anlamıştım. Neden ters sırayla yapılamayacağını ancak şu soruyu
sorunca kavradım: *"Önce Kafka'ya bassam ne olurdu?"*

Cevap: Kafka yazımı başarılı olup DB yazımı çökerse, sistemde **kaydı olmayan
bir event** dolaşırdı. Routing engine onu işler, teslimat yapılır — ama
idempotency kontrolü yapılamaz, admin panelinde görünmez, replay edilemez.
Yani "kayıp" değil, ondan daha kötü: **hayalet veri**.

Aynı şey backoff'taki **jitter** için de geçerliydi. Formülü kopyalamak kolay;
neden gerekli olduğunu ancak şunu düşününce anladım: 100 teslimat aynı anda
başarısız olursa, jitter olmadan hepsi tam olarak 1 saniye sonra geri döner ve
zaten zorlanan hedefi ikinci kez çökertir. Jitter bir mikro-optimizasyon değil,
**geri besleme döngüsünü kırmak**.

### 1.3 Güvenlik sonradan eklenemez — çünkü veri modelini değiştirir

Görev tanımı "güvenlik sonradan eklenmez" diyordu; bunun neden doğru olduğunu
somut olarak gördüm.

`api_keys` tablosunda üç ayrı alan var: `key_prefix` (düz metin), `key_hash`
(SHA-256), `hmac_secret_enc` (AES-GCM). Bu üçlü tesadüf değil:

- Anahtar hash'lenerek saklanmalı → ama o zaman "bu isteği kim gönderdi"
  sorusunu O(1) yanıtlayamayız → **prefix alanı gerekli**.
- HMAC secret'ı hash'lenemez, çünkü imzayı **hesaplamamız** gerekiyor →
  şifrelenmeli → **ayrı bir sütun ve şifreleme anahtarı yönetimi gerekli**.

Bunları sonradan eklemek, migration + tüm anahtarları yeniden üretme + tüm
tool'ları güncelleme demekti. Baştan düşünüldüğünde bir tablo tanımı.

---

## 2. En çok zorlayan üç şey

### 2.1 Kafka'da gecikmeli mesaj olmaması

Retry mekanizmasını yazarken "1 saniye sonra tekrar dene" ifadesinin Kafka'da
karşılığı olmadığını fark ettim. Üç seçeneği de düşünmek zorunda kaldım:

| Seçenek | Neden reddedildi |
| :--- | :--- |
| Consumer'da `sleep` | **Partition'ı bloke eder.** Aynı partition'daki sağlıklı mesajlar da bekler — çözmeye çalıştığımız problemin ta kendisi |
| Delay topic zinciri | Her gecikme için ayrı topic + consumer; gecikme çözünürlüğü topic sayısıyla sınırlı |
| DB'de zamanlanmış görev | **Seçildi** — keyfi gecikme, admin panelinde görünür, worker yeniden başlasa da plan kaybolmaz |

Beklenmedik yan fayda: bu zamanlayıcı aynı zamanda bir **kurtarma
mekanizması** oldu. Routing engine görevi DB'ye yazıp Kafka'ya basamadan
çökerse, görev orada `pending` kalır ve zamanlayıcı onu bulur. Boru hattının
tek bir noktası bile kalıcı kayba yol açmıyor.

### 2.2 Circuit breaker ile retry bütçesinin birbirini kilitlemesi

Bu, testte yakaladığım **gerçek bir hataydı** ve en öğretici an oldu.

Kurgu şuydu: bulkhead veya breaker isteği engellediğinde görevi "ertele" ve
deneme sayacını geri al — çünkü gerçekten bir deneme yapılmadı. Mantıklı
görünüyordu.

Ama erteleme kararı `BeginAttempt`'ten **önce** veriliyordu, yani sayaç o tur
için hiç artırılmamıştı. Sayacı azaltmak, **yapılmamış bir denemeyi geri
almak** oldu:

```
attempt_count: 5 → (breaker açık, ertele) → 4 → (deneme) → 5 → (ertele) → 4 → ...
```

Sonuç: kalıcı olarak çökmüş bir subscriber'ın mesajı **asla DLQ'ya
ulaşmıyordu**. Sonsuza dek 5. denemede dönüyordu.

Testte bunu şöyle yakaladım: DLQ tablosu boş kaldı ama `delivery_tasks`
tablosunda `attempt_count = 5, status = pending` bir satır duruyordu. Sayının
sabit kalması sorunu ele verdi.

**Ders:** Bir sayacı değiştiren kod, o sayacı **kimin ve ne zaman
artırdığını** bilmek zorunda. "Geri al" mantığı yalnızca gerçekten yapılmış
bir işlem için doğrudur.

### 2.3 Least privilege'ın sessiz maliyeti

Servis başına Postgres rolü tanımlamak kolaydı. Zor olan, **hangi sorgunun
hangi yetkiyi gerektirdiğini** doğru tahmin etmekti.

`dlq-handler`'ın retention job'ına `DELETE` verdim ama `SELECT` vermedim.
Sorgu şuydu:

```sql
DELETE FROM hub.delivery_attempts
WHERE id IN (SELECT id FROM hub.delivery_attempts WHERE attempted_at < ... LIMIT 5000)
```

Alt sorgu `SELECT` istiyor. Hata ancak retention job ilk kez çalıştığında
(servis açıldıktan 1 dakika sonra) log'a düştü:

```
permission denied for table delivery_attempts (SQLSTATE 42501)
```

**Ders:** Least privilege'ı test etmeden uygulamak, yetkiyi hiç vermemekle
aynı riski taşır — yalnızca hata daha geç ortaya çıkar. Bu tür hatalar için
e2e testi tek gerçek güvence.

### 2.4 İki farklı sayacın aynı sütunu paylaşması

Bu hatayı **arayüzü gözle test ederken** buldum; hiçbir otomatik test
yakalamamıştı, çünkü hepsi doğru soruyu soruyordu ve doğru cevabı alıyordu.

Replay sonrası teslimat başarılı oluyordu (`delivery_tasks.status = succeeded`),
ama Admin UI'daki teslimat geçmişinde **yalnızca 6 başarısız deneme
görünüyordu** — başarılı olan hiç yoktu.

Sebep: iki farklı kavram tek bir sayıya bağlanmıştı.

| Kavram | Nerede | Davranışı |
| :--- | :--- | :--- |
| Retry **bütçesi** | `delivery_tasks.attempt_count` | Replay'de 0'a sıfırlanır (mesaj yeni bir şans almalı) |
| Operasyonel **log sırası** | `delivery_attempts.attempt_no` | Asla sıfırlanmamalı (geçmiş silinmez) |

İkisi için de aynı sayı kullanılınca, replay sonrası deneme "#1" oldu ve
`delivery_attempts` tablosunda zaten var olan eski "#1" ile çakıştı.
`ON CONFLICT DO NOTHING` başarılı teslimatı **sessizce yuttu**.

Düzeltme: log sırası artık `INSERT ... SELECT COALESCE(MAX(attempt_no),0)+1`
ile atomik olarak hesaplanıyor; retry bütçesi sayacına dokunulmadı.

**İki ders:**

1. Bir sütunun "sayaç" olması, tek bir amacı olduğu anlamına gelmiyor.
   İki farklı yaşam döngüsü (biri sıfırlanan, biri sıfırlanmayan) tek alanı
   paylaşamaz.
2. `ON CONFLICT DO NOTHING` sessizdir. Sessizlik, "her şey yolunda" ile
   "veri kayboldu" arasında ayrım yapmaz. Kullandığın her yerde
   "çakışma gerçekten zararsız mı?" diye sormak gerekiyor — bu durumda
   değildi.

Ve en önemlisi: **56 otomatik kontrol geçerken bir arayüz ekranına bakmak
bu hatayı ortaya çıkardı.** Testler doğru soruları soruyordu; ben yanlış
soruyu sormuştum.

---

## 3. Tekrar yapsam neyi değiştirirdim

### 3.1 Testleri servislerden önce yazardım

`internal/` paketlerinin testlerini kod bittikten sonra yazdım. Testler
geçti — ama iki gerçek hatayı (§2.2 ve §2.3) ancak **çalışan sistemde**
yakaladım.

Özellikle `delivery-worker`'ın durum makinesini önce test olarak yazsaydım,
breaker/sayaç çakışmasını daha ilk saatte görürdüm. Durum makineleri tam
olarak test-first yaklaşımının en çok kazandırdığı yerdir: geçişler sonlu ve
sayılabilir.

### 3.2 Servis kodunda entegrasyon testi katmanı eklerdim

Şu an `services/*` paketlerinin unit testi yok; doğrulama e2e script'i
üzerinden yapılıyor. Bu, hataların **en pahalı noktada** (tüm yığın ayaktayken)
bulunması demek.

`testcontainers-go` ile gerçek Postgres + Redis'e karşı store katmanı testleri
yazardım. Özellikle `BeginAttempt`, `ClaimDueTasks`, `ClaimReplayQueue` gibi
`SKIP LOCKED` kullanan sorgular — bunlar unit test edilemez ama entegrasyon
testinde birkaç saniyede doğrulanabilir.

### 3.3 Metrikleri kod yazarken tanımlardım

Metrikleri servisler bittikten sonra ekledim ve bazı yerlerde ölçmek
istediğim şeyi ölçemediğimi fark ettim. Örneğin uçtan uca gecikme için
`received_at` bilgisinin teslimat görevine kadar **taşınması** gerekiyordu;
bunu sonradan eklemek `model.DeliveryTask`'a alan eklemek demekti.

"Bu servisin sağlıklı olduğunu hangi tek metrikten anlarım?" sorusunu kod
yazmadan önce sormak, hem metrik tasarımını hem de veri modelini iyileştirirdi.

### 3.4 Kural DSL'ini daha erken dondururdum

`routing.Condition` yapısını yazarken operatör listesini birkaç kez
değiştirdim (`contains` sonradan eklendi, `startswith` düşünüldü sonra
vazgeçildi). Her değişiklik admin-api doğrulaması, routing-engine
değerlendirmesi, UI yardım metni ve dokümanı etkiledi.

Kullanıcıya görünen bir DSL, **API kadar bir kontrattır** — önce yazılıp
dondurulmalıydı.

---

## 4. Beklediğimden farklı çıkan şeyler

| Beklenti | Gerçek |
| :--- | :--- |
| Go servisleri ~50 MB bellek yer | **~12 MB** — distroless + statik binary çok etkili |
| Kafka en çok kaynak yiyen bileşen olur | Doğru (~417 MB) ama **Kafka UI 333 MB** ile şaşırtıcı derecede yakın |
| Loki kurulumu zahmetli olur | En kolay parçalardan biri; Elasticsearch'ün ~1/30'u bellek |
| p95 < 50 ms hedefi zor olur | **2-9 ms** ölçüldü — darboğaz DB değil, ağ gidiş-dönüşü |
| Docker Compose 20 servis için yetersiz kalır | Gayet yeterli; `depends_on: service_healthy` beklediğimden güçlü |
| En zor kısım Kafka olur | En zor kısım **durum makinesi tasarımı** oldu |

---

## 5. Yapay zeka araçlarını nasıl kullandım

Kod üretimi, dokümantasyon taslakları ve alternatif değerlendirmelerinde
kullandım. Ancak şu iki şey **yapay zekadan gelmedi**, sistemi çalıştırıp
gözlemleyerek bulundu:

1. Breaker/sayaç kilitlenmesi (§2.2) — yalnızca DLQ'nun boş kalmasını fark
   ederek bulundu.
2. `dlq-handler` eksik `SELECT` yetkisi (§2.3) — yalnızca retention job'ın
   ilk turunu bekleyerek görüldü.
3. Deneme logunun replay'de üzerine yazılması (§2.4) — yalnızca Admin UI'daki
   teslimat geçmişi ekranına bakarak görüldü.

Üçü de "kod doğru görünüyordu" kategorisindeydi. **Üretilen kodu okumadan ve
çalıştırıp gözlemlemeden kabul etmenin bedeli tam olarak budur.**

Ayrıca her mimari kararda "neden diğeri değil?" sorusunu ayrıca sordum —
`docs/03-tech-stack.md`'deki 3 alternatiflik karşılaştırmalar bu yüzden var.
Bir seçeneği savunabilmek için reddedilenleri de bilmek gerekiyor.

---

## 6. Bu projeden çıkardığım tek cümlelik ders

> Dağıtık sistemlerde asıl zorluk, mutlu yolu çalıştırmak değil; **her
> bileşenin ayrı ayrı çökebileceğini varsayıp sistemin yine de doğru
> davranmasını sağlamaktır** — ve bunun doğru olduğunu ancak bileşenleri
> gerçekten çökerterek anlayabilirsin.

Bu yüzden `mock-subscriber`'daki "her N istekten birini fail et" anahtarı,
projenin en küçük ama en değerli parçalarından biri: arızayı isteğe bağlı ve
tekrarlanabilir hâle getiriyor.
