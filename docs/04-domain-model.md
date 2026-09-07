# 04 — Domain Modeli ve Veritabanı Şeması

Bu doküman, platformun domain varlıklarını, aralarındaki ilişkileri ve
veritabanı tasarım kararlarının **gerekçelerini** içerir.

Şemanın çalıştırılabilir hâli: `migrations/0001_init.up.sql`

---

## 1. ER Diyagramı

![ER Diyagramı](diagrams/erd.png)

> Kaynak: `docs/diagrams/erd.mmd` (Mermaid). Yeniden üretmek için:
> `npx -y @mermaid-js/mermaid-cli -i docs/diagrams/erd.mmd -o docs/diagrams/erd.png`

---

## 2. Varlıklar

### 2.1 Tool

Ekosisteme kayıtlı her yazılım ürünü. Bir tool publisher, subscriber veya
her ikisi olabilir.

| Alan | Tip | Not |
| :--- | :--- | :--- |
| `id` | UUID (PK) | |
| `slug` | TEXT UNIQUE | Makine-dostu kimlik. Event zarfındaki `source_tool` bununla eşleşir. |
| `name`, `description` | TEXT | İnsan-dostu bilgi |
| `kind` | ENUM | `publisher` \| `subscriber` \| `both` |
| `status` | ENUM | `active` \| `suspended` \| `disabled` |
| `metadata` | JSONB | Serbest alan (ekip, repo, ortam) |

**Neden `slug` ayrı bir alan?** Event zarfında UUID taşımak, payload'ı insan
için okunamaz kılardı. `slug` hem okunabilir hem benzersizdir. Ayrıca bir
tool silinip yeniden oluşturulduğunda UUID değişir ama slug korunabilir.

**Neden `kind` bir enum, iki boolean değil?** `is_publisher` + `is_subscriber`
ikilisi "ikisi de false" gibi anlamsız bir durum üretebilirdi. Enum bu durumu
şema seviyesinde imkânsız kılar.

### 2.2 ApiKey

Bir tool'un kimliğini kanıtladığı anahtar.

| Alan | Tip | Not |
| :--- | :--- | :--- |
| `key_prefix` | TEXT UNIQUE | `ih_<12 hex>` — **düz metin**, lookup anahtarı |
| `key_hash` | BYTEA | Tam anahtarın SHA-256 özeti |
| `hmac_secret_enc` | BYTEA | AES-256-GCM ile **şifreli** HMAC secret |
| `status` | ENUM | `active` \| `rotating` \| `revoked` |
| `grace_until` | TIMESTAMPTZ | Rotasyonda eski anahtarın son geçerliliği |

**Neden prefix düz metin saklanıyor?** Anahtar hash'lenerek saklandığı için
"bu isteği kim gönderdi" sorusunu yanıtlamanın tek yolu tüm satırları
hash'leyip karşılaştırmak olurdu — O(n). Prefix, hash'i açığa çıkarmayan ama
tekil satırı O(1) bulmayı sağlayan bir indeks anahtarıdır. Aynı desen Stripe,
GitHub ve AWS'de kullanılır.

**Neden secret hash'lenmiyor da şifreleniyor?** API key doğrulamasında yalnızca
"eşleşiyor mu" sorusunu yanıtlarız → hash yeterli. Ancak HMAC imzasını
HESAPLAMAK için ham secret gerekir → hash geri döndürülemez. Bu yüzden secret
şifrelenir; anahtar `SECRET_ENCRYPTION_KEY` ile runtime'da verilir ve DB yedeği
çalınsa bile secret kullanılamaz.

**Neden anahtar için SHA-256, admin şifresi için argon2id?**
API key doğrulaması her ingest isteğinde çalışır; argon2 (~80 ms) saniyede
~12 isteklik bir tavan yaratırdı. Anahtar 192 bit rastgele üretildiği için
sözlük saldırısına kapalıdır — yavaş hash'in koruduğu risk (zayıf parola)
burada mevcut değildir. Admin şifresi ise insan seçimidir ve nadiren
doğrulanır; orada argon2id doğru tercihtir.

### 2.3 EventType

Bir event tipinin kontratı.

| Alan | Not |
| :--- | :--- |
| `name`, `version` | Birlikte UNIQUE. `user.created@1` |
| `json_schema` | JSONB — JSON Schema draft 2020-12 |
| `is_active` | Pasif tipler yeni event kabul etmez |

**Neden versiyon ayrı bir sütun, adın parçası değil?** `user.created.v2` gibi
bir isimlendirme, "aynı event'in farklı versiyonu" ile "farklı event" ayrımını
kaybettirirdi. Ayrı sütun sayesinde "bu event tipinin tüm versiyonları"
sorgusu doğal olarak yazılabilir.

### 2.4 Subscription

Hangi tool'un hangi event tipini, hangi adreste almak istediği.

`UNIQUE (subscriber_tool_id, event_type_id, endpoint_url)` — aynı hedefe
mükerrer abonelik açılamaz.

`max_retries` ve `timeout_ms` abonelik bazındadır: yavaş bir subscriber daha
uzun timeout isteyebilirken, kritik olmayan bir hedef için deneme sayısı
düşürülebilir.

### 2.5 RoutingRule

Aboneliğe bağlı filtre. `condition` JSONB'de saklanan koşul ağacıdır.

**Anlambilim:**
- Abonelik var, kural yok → **koşulsuz eşleşir.** Abonelik zaten "bu tipi
  istiyorum" beyanıdır.
- Birden fazla kural → **OR.** Kurallar alternatiftir. AND davranışı tek kural
  içinde `{"op":"and", ...}` ile ifade edilir.

### 2.6 EventInstance

Platforma ulaşmış tek bir event. **Durable inbox.**

| Alan | Not |
| :--- | :--- |
| `event_id` | UNIQUE — idempotency anahtarı |
| `payload`, `metadata` | JSONB |
| `response_snapshot` | JSONB — mükerrer istekte aynı yanıtı dönmek için |
| `status` | `accepted` → `routed` \| `no_match` \| `failed` |

**Neden `response_snapshot`?** FR-2.5 "önceki yanıtın aynısı dönmeli" diyor.
Yanıtı yeniden üretmek yerine saklamak, ilk istekten bu yana yapılandırma
değişse bile birebir aynı yanıtı garanti eder.

### 2.7 OutboxMessage

Kafka'ya yayınlanmayı bekleyen mesaj. `event_instances` ile **aynı
transaction'da** yazılır.

| Alan | Not |
| :--- | :--- |
| `topic`, `partition_key` | Hedef topic mesaj başına belirlenir |
| `published_at` | NULL = bekliyor |
| `attempts`, `last_error` | Sürekli başarısız mesaj operasyonel olarak görünür |

### 2.8 DeliveryTask

Bir event'in bir subscriber'a teslim edilme görevi.

`UNIQUE (event_instance_id, subscription_id)` — bu kısıt at-least-once
tüketimin mükerrer webhook'a dönüşmesini **veritabanı seviyesinde** engeller.
Kilit veya dağıtık koordinasyon gerekmez.

`next_attempt_at` gecikmeli kuyruk anahtarıdır (bkz. §5).

### 2.9 DeliveryAttempt

Her tekil HTTP denemesinin logu. `UNIQUE (delivery_task_id, attempt_no)`.

`response_body` yalnızca ilk 512 bayttır: hata ayıklamaya yeter, diski
şişirmez.

### 2.10 DlqEvent

Teslim edilemeyen mesaj. Durum makinesi:

```
new ──(admin replay)──> replay_requested ──(dlq-handler)──> replayed
 └──(admin discard)──> discarded
```

`UNIQUE (delivery_task_id)` — replay sonrası yine başarısız olan görev yeni
satır açmaz, mevcut kayıt güncellenir. Operatör tek kayıt üzerinden tüm
geçmişi izler.

### 2.11 AuditLog

`before_state` / `after_state` JSONB olarak saklanır → "ne değişti" sorusu
diff alarak yanıtlanır.

### 2.12 AdminUser & RefreshToken

Refresh token'lar **hash'lenerek** saklanır ve tek kullanımlıktır (rotation):
çalınan bir token ikinci kullanımda reddedilir ve hırsızlık tespit edilebilir
hâle gelir.

---

## 3. Şema stratejisi: tek DB, tek şema, rol başına yetki

**Karar:** Tek veritabanı, tek `hub` şeması — ancak **servis başına ayrı
Postgres rolü** ve tablo bazlı GRANT.

**Değerlendirilen alternatifler:**

| Yaklaşım | Artı | Eksi | Karar |
| :--- | :--- | :--- | :--- |
| Servis başına ayrı **veritabanı** | Tam izolasyon | FK bütünlüğü kaybolur; `delivery_tasks → event_instances` referansı uygulama katmanında elle korunmak zorunda kalır | Reddedildi |
| Servis başına ayrı **şema** | Kısmi izolasyon | Şemalar arası FK mümkün ama karmaşık; ortak tablolar (tools) nereye ait? | Reddedildi |
| Tek şema + **rol bazlı yetki** | FK bütünlüğü korunur, least privilege sağlanır, operasyon basit | Fiziksel izolasyon yok | **Seçildi** |

Gerekçe: Bu platformun tabloları yoğun biçimde birbirine referans verir
(event → task → attempt → dlq). Bu zinciri veritabanı dışına taşımak,
kazanılan izolasyondan çok daha büyük bir tutarlılık riski yaratırdı.
İzolasyon ihtiyacı **yetki** seviyesinde karşılanıyor: `event-ingest`
`delivery_attempts` tablosuna yazamaz, `routing-engine` `api_keys` tablosunu
okuyamaz (`migrations/0003_grants.up.sql`).

Ölçek büyüdüğünde ayrıştırma yolu açıktır: her servisin dokunduğu tablo kümesi
zaten belgelenmiş durumda.

---

## 4. Index stratejisi

Her index bir **sorguya** karşılık gelir; "olsun da dursun" index yoktur —
her index yazma maliyetini artırır.

| Index | Hizmet ettiği sorgu |
| :--- | :--- |
| `api_keys(key_prefix)` UNIQUE | Her ingest isteğindeki anahtar lookup'ı |
| `subscriptions(event_type_id) WHERE is_active` | Routing engine'in sıcak yolu |
| `event_instances(event_id)` UNIQUE | Idempotency kontrolü |
| `event_instances(received_at DESC)` | Admin UI event listesi (yeniden eskiye) |
| `event_instances USING GIN (payload jsonb_path_ops)` | Payload içinde alan bazlı arama |
| `outbox_messages(created_at) WHERE published_at IS NULL` | **Partial** — poller yalnızca bekleyenlerle ilgilenir |
| `delivery_tasks(next_attempt_at) WHERE status='pending' AND next_attempt_at IS NOT NULL` | **Partial** — retry zamanlayıcısı |
| `delivery_attempts(delivery_task_id, attempt_no DESC)` | Bir görevin deneme geçmişi |
| `dlq_events(status, created_at DESC)` | DLQ sayfası |
| `audit_logs(created_at DESC)` | Denetim listesi |

### Partial index'ler neden kritik?

`outbox_messages` tablosu milyonlarca satıra ulaşabilir, ama poller her zaman
yalnızca **yayınlanmamış** birkaç satırla ilgilenir. Tam index bu tablonun
tamamını kapsar ve zamanla büyür; partial index yalnızca aktif kuyruğu
kapsadığı için boyutu sabit kalır — tablo 10 milyon satır olsa bile.

Aynı mantık `delivery_tasks(next_attempt_at)` için de geçerli.

### `jsonb_path_ops` neden `jsonb_ops` değil?

`jsonb_path_ops` yalnızca `@>` (contains) operatörünü destekler ama indeksi
~%40 daha küçüktür ve bu sorgu tipinde daha hızlıdır. Admin UI'ın payload
araması tam olarak bu operatörü kullanır.

---

## 5. JSONB kararları

**JSONB kullanılan alanlar:** `payload`, `metadata`, `json_schema`,
`condition`, `http_headers`, `before_state`/`after_state`, `tools.metadata`.

**Neden JSONB, normalize edilmiş sütunlar değil?**

`payload` için şeması **çalışma zamanında tanımlanan** veri söz konusu.
Normalize etmek, her yeni event tipi için `ALTER TABLE` demek olurdu — bu da
projenin temel hedefiyle ("yeni bir tool eklemek için kod değiştirmen
gerekmemeli") doğrudan çelişirdi.

**Neden JSON değil JSONB?**
- JSON metni olduğu gibi saklar; her okumada yeniden ayrıştırılır.
- JSONB ikili ayrıştırılmış hâlde saklar → sorgu hızlı, **indekslenebilir**.
- Bedeli: yazma sırasında ayrıştırma maliyeti ve alan sırasının korunmaması.
  İkisi de bizim için önemsiz.

**JSONB kullanılMAYAN alanlar:** `status`, `event_type`, `attempt_count` gibi
platformun kendi mantığında kullandığı alanlar normal sütundur. Bunlar
kısıtlanabilir (CHECK), indekslenebilir ve tip güvenlidir.

---

## 6. Retention stratejisi

| Tablo | Süre | Env | Gerekçe |
| :--- | :--- | :--- | :--- |
| `event_instances` | 14 gün | `EVENT_RETENTION_DAYS` | Hata ayıklama penceresi; uzun süreli analiz veri ambarının işidir |
| `delivery_attempts` | 14 gün | `ATTEMPT_RETENTION_DAYS` | En hızlı büyüyen tablo (event × subscriber × deneme) |
| `dlq_events` | 30 gün | `DLQ_RETENTION_DAYS` | Operatörün inceleyip replay etmesi için daha uzun süre |
| `outbox_messages` | 24 saat (yayınlanmış) | sabit | Yayınlandıktan sonra yalnızca yakın geçmiş hata ayıklama değeri taşır |
| `audit_logs` | **Silinmez** | — | Denetim kaydının silinmesi denetlenebilirliği yok eder |

Temizlik `dlq-handler` içindeki retention job tarafından batch'ler hâlinde
yapılır (varsayılan 5000 satır/tur). Tek dev `DELETE`, tabloyu uzun süre
kilitler ve replikasyon gecikmesi yaratır.

**Önemli koruma:** `event_instances` silinirken bekleyen teslimatı veya açık
DLQ kaydı olan event'ler **atlanır**. Aksi halde `ON DELETE CASCADE` aktif bir
teslimat görevini ortadan kaldırırdı.

Her temizlik turu `audit_logs`'a `retention.purge` olarak yazılır: veri silme
işlemi de denetlenebilir olmalıdır.

---

## 7. "Şu senaryoda ne olur?"

**Publisher aynı event'i 3 kez gönderirse?**
İlk istek satırı yazar. 2. ve 3. istekler `ON CONFLICT (event_id) DO NOTHING`
nedeniyle satır üretmez; saklanan `response_snapshot` aynen döner ve yanıta
`X-Idempotent-Replay: true` başlığı eklenir. Kafka'ya tek mesaj gider.

**Kafka 2 saat kapalı kalırsa?**
Ingest çalışmaya devam eder (Kafka'ya senkron yazmıyoruz). `outbox_messages`
birikir, `hub_outbox_pending_messages` metriği yükselir, `OutboxBacklog`
alert'i tetiklenir. Kafka döndüğünde poller birikmiş mesajları sırayla
yayınlar. **Sıfır veri kaybı.**

**Routing engine görevi DB'ye yazıp Kafka'ya basamadan çökerse?**
Görev `pending` + `next_attempt_at = now() + 30s` durumundadır.
Delivery worker'ın retry zamanlayıcısı 30 saniye sonra onu bulur ve yayınlar.
Boru hattının tek bir noktası bile kalıcı kayba yol açmaz.

**İki delivery worker aynı görevi alırsa?**
Redis kilidi ikincisini eler. Kilit alınamazsa bile `BeginAttempt`'in koşullu
UPDATE'i (`WHERE status IN ('pending','in_flight')`) ikinci kez `succeeded`
bir görevi işlemeyi engeller. İki savunma hattı vardır.

**Subscriber kalıcı olarak çökerse?**
5 deneme → DLQ. Ardından circuit breaker açılır ve o subscriber'a giden yeni
görevler ağa çıkmadan ertelenir; diğer subscriber'lar etkilenmez.

**Redis tamamen düşerse?**
Rate limit fail-open'a düşer (yapılandırılabilir), routing kuralları doğrudan
Postgres'ten okunur, dağıtık kilit devre dışı kalır (mükerrer gönderim riski
artar ama at-least-once zaten bunu kabul eder). **Sistem çalışmaya devam
eder** — Redis bilinçli olarak hiçbir yerde tek doğruluk kaynağı değildir.
