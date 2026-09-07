# 03 — Teknoloji Seçimleri ve Mimari Gerekçeler

> Bu dokümanın kuralı: her satırda **"neden bu?"** değil, **"neden diğerleri
> değil?"** sorusu da yanıtlanır. "Trendde olduğu için" bir gerekçe değildir.

---

## Teknoloji Matrisi

| Katman | Teknoloji | Sürüm |
| :--- | :--- | :--- |
| Backend | Go | 1.23 |
| HTTP router | go-chi/v5 | 5.1.0 |
| DB sürücüsü | pgx/v5 (ORM yok) | 5.7.1 |
| Message broker | Apache Kafka (KRaft) | 3.9.0 |
| Veritabanı | PostgreSQL | 16.6 |
| Cache / kilit | Redis | 7.4 |
| Kafka istemcisi | segmentio/kafka-go | 0.4.47 |
| Şema doğrulama | santhosh-tekuri/jsonschema | v5 |
| Migration | golang-migrate | v4.18.1 |
| Frontend | React + TypeScript + Vite | 18.3 / 5.7 / 6.0 |
| Stil | Tailwind CSS | 3.4 |
| Veri katmanı (FE) | TanStack Query | 5.62 |
| Grafik | Recharts | 2.14 |
| i18n | i18next | 24.2 |
| Metrik | Prometheus | 2.55 |
| Log | Loki + Promtail | 3.3 |
| Görselleştirme | Grafana | 11.4 |

---

## 1. Backend dili — 3 alternatif

| Kriter | **Go** | Node.js (NestJS) | Python (FastAPI) |
| :--- | :--- | :--- | :--- |
| Eşzamanlılık modeli | Goroutine (M:N) — binlerce eşzamanlı webhook ucuz | Tek thread + event loop; CPU işi bloke eder | asyncio; GIL nedeniyle CPU paralelliği yok |
| Bellek (idle servis) | **~12 MB** (ölçüldü) | ~60-90 MB | ~50-80 MB |
| Docker image | **~30 MB** (distroless, statik) | ~150 MB (node runtime) | ~120 MB |
| Soğuk başlangıç | <100 ms | ~1-2 sn | ~1-2 sn |
| Tip güvenliği | Derleme zamanı, zorunlu | TypeScript (opsiyonel, runtime'da silinir) | Type hints (runtime'da zorlanmaz) |
| Kafka istemcisi | Olgun (kafka-go, franz-go) | kafkajs — olgun | confluent-kafka (C bağımlılığı) |
| Ekosistem | Küçük ama yeterli | En geniş | En geniş |
| Öğrenme eğrisi | Orta | Düşük (JS bilgisiyle) | Düşük |

**Seçim: Go.**

Belirleyici gerekçeler:

1. **İş yükü profili I/O-bound ve yüksek eşzamanlı.** Delivery worker aynı
   anda onlarca yavaş HTTP isteği tutar. Goroutine başına ~2 KB stack ile bu
   ucuzdur; Node'da her bekleyen istek event loop'ta promise zinciri, Python'da
   asyncio task'ıdır ve ikisi de daha pahalıdır.
2. **10+ container'lık bir yığın.** Servis başına 60 MB yerine 12 MB, toplamda
   ~400 MB fark demek — geliştirici makinesinde hissedilir.
3. **Statik binary → distroless image.** İçinde shell, paket yöneticisi ve
   libc olmayan bir image, Trivy taramasında **sıfır OS paketi CVE'si** verir.
   Node/Python'da base image CVE'leri sürekli takip gerektirir.
4. **Derleme zamanı tip güvenliği.** TypeScript tipleri runtime'da silinir;
   Go'da `interface{}` dönüşümleri bile derleyici tarafından denetlenir.

**Bedeli:** Daha fazla boilerplate (hata yönetimi, jenerik eksikliği).
Kabul edildi — bu maliyet okunabilirlik ve öngörülebilirlik olarak geri dönüyor.

---

## 2. Frontend framework — 3 alternatif

| Kriter | **React + Vite** | Next.js | Vue 3 |
| :--- | :--- | :--- | :--- |
| Gerekli mi SSR? | Hayır | Evet (ana özelliği) | Nuxt ile |
| Üretim çıktısı | Statik dosya → nginx | Node.js sunucusu gerekir | Statik dosya |
| Docker image | **~75 MB** (nginx + statik) | ~200 MB (node runtime) | ~75 MB |
| Dev sunucu başlangıcı | <1 sn (esbuild) | ~3-5 sn | <1 sn |
| Ekosistem | En geniş | Geniş | Orta |
| Takım tanıdıklığı | En yaygın | Yaygın | Daha az |

**Seçim: React + TypeScript + Vite.**

Belirleyici gerekçe: Bu bir **kimlik doğrulaması arkasındaki iç yönetim
paneli**. SEO gereksinimi yok, ilk boyama süresi kritik değil, sunucu tarafı
render edilecek public sayfa yok. Next.js'in getirdiği her şey (SSR, ISR,
route handlers, ayrı Node runtime) burada **kullanılmayan karmaşıklıktır** —
ama Docker image'ını 3 katına çıkarır ve bir Node sürecini üretimde ayakta
tutmayı gerektirir.

Vite ile üretilen çıktı saf statik dosyadır; nginx tarafından servis edilir,
çalışan bir JS runtime'ı yoktur → saldırı yüzeyi ve kaynak kullanımı minimum.

Vue teknik olarak eşdeğerdi; React'in daha geniş ekosistemi (TanStack Query,
Recharts, lucide) ve yaygınlığı belirleyici oldu.

---

## 3. ORM kullanılacak mı?

**Karar: Hayır. pgx/v5 ile elle yazılmış SQL.**

| Kriter | **pgx (raw SQL)** | GORM | sqlc | ent |
| :--- | :--- | :--- | :--- | :--- |
| `FOR UPDATE SKIP LOCKED` | Doğal | Raw'a düşer | Destekler | Zor |
| JSONB operatörleri (`@>`, `jsonb_path_ops`) | Doğal | Raw'a düşer | Destekler | Sınırlı |
| `ON CONFLICT ... RETURNING` | Doğal | Kısmi | Destekler | Kısmi |
| Partial index'e uyan sorgu kontrolü | Tam | Yok | Tam | Yok |
| Kod üretimi gerekir mi? | Hayır | Hayır | **Evet** | **Evet** |
| Öğrenme/bakım maliyeti | SQL bilmek | ORM API'si + SQL | SQL + araç zinciri | DSL |
| N+1 riski | Görünür | Gizli | Görünür | Gizli |

**Gerekçe:** Bu projenin **en kritik üç sorgusu** ORM'lerin iyi ifade
edemediği sorgulardır:

```sql
-- 1. Outbox poller — kuyruk deseni
SELECT ... FROM hub.outbox_messages
WHERE published_at IS NULL ORDER BY id LIMIT $1
FOR UPDATE SKIP LOCKED;

-- 2. Idempotent ingest — atomik "varsa ekleme"
INSERT INTO hub.event_instances (...) VALUES (...)
ON CONFLICT (event_id) DO NOTHING RETURNING id;

-- 3. Koşullu durum geçişi — yarış korumalı
UPDATE hub.delivery_tasks SET status = 'in_flight', attempt_count = attempt_count + 1
WHERE id = $1 AND status IN ('pending', 'in_flight') RETURNING attempt_count;
```

Bir ORM kullansaydık üçünü de "raw query" olarak yazacaktık — yani ORM'in
soyutlamasından **hiçbir fayda görmeden** onun ağırlığını taşıyacaktık.

`sqlc` (SQL'den tip güvenli Go üretir) güçlü bir alternatifti. Kod üretim
adımının CI'a ve geliştirici akışına eklediği sürtünme, bu ölçekte kazancı
aşıyor. Tip güvenliği elle yazılmış `Scan` çağrılarıyla ve testlerle sağlanıyor.

**SQL injection riski?** Yok. pgx **her zaman** prepared statement kullanır;
`$1, $2` parametreleri asla string olarak birleştirilmez. Kod tabanında SQL'in
string birleştirmeyle kurulduğu tek bir satır bulunmuyor.

---

## 4. Servisler arası iletişim protokolü

| Kriter | **REST/HTTP + JSON** | gRPC | GraphQL |
| :--- | :--- | :--- | :--- |
| Dış tool entegrasyonu | Her dilde `curl` ile çalışır | Proto dosyası + kod üretimi gerekir | Şema + istemci gerekir |
| Tarayıcıdan erişim | Doğal | grpc-web proxy gerekir | Doğal |
| Hata ayıklama | `curl`, tarayıcı, Postman | `grpcurl` (daha az yaygın) | Playground |
| Performans | Orta | **En yüksek** (HTTP/2 + protobuf) | Düşük (parse + resolve) |
| Sözleşme | OpenAPI | .proto (daha güçlü) | SDL |

**Karar: Servisler arası senkron iletişim için REST/HTTP + JSON.**

Ancak asıl mimari karar şudur: **servisler arası senkron çağrı neredeyse hiç
yoktur.**

```
Senkron  (HTTP): tarayıcı → admin-api,  tool → event-ingest,  worker → subscriber
Asenkron (Kafka): event-ingest → routing-engine → delivery-worker → dlq-handler
```

Veri düzlemindeki tüm servis-servis iletişimi Kafka üzerindendir. Bu yüzden
gRPC'nin performans avantajı ortaya çıkacağı yer neredeyse yok.

Kalan senkron uçlar **dış dünyaya bakar** — publisher tool'lar, admin
tarayıcısı, subscriber webhook'ları. Bu sınırda evrensel erişilebilirlik
performanstan daha değerlidir: bir dış geliştirici `curl` ile deneyebilmelidir.

**Ne zaman gRPC'ye geçilir?** İki iç servis arasında yüksek hacimli senkron
çağrı ihtiyacı doğarsa (ör. bir "enrichment" servisi). O zaman yalnızca o
bağlantı gRPC olur; dış API REST kalır.

---

## 5. Kafka istemcisi — 3 alternatif

| Kriter | **segmentio/kafka-go** | confluent-kafka-go | twmb/franz-go |
| :--- | :--- | :--- | :--- |
| Uygulama | Saf Go | librdkafka (CGO) | Saf Go |
| Statik binary | **Evet** | Hayır (CGO) | Evet |
| Distroless image | **Evet** | Hayır (libc gerekir) | Evet |
| Performans | İyi | En yüksek | En yüksek |
| API sadeliği | En sade | Orta | Geniş yüzey |
| Consumer group | Yerleşik | Yerleşik | Yerleşik |

**Seçim: kafka-go.** CGO gerektirmemesi belirleyici: `CGO_ENABLED=0` ile
üretilen statik binary, `distroless/static` image'ında çalışır. Bu, güvenlik
ve image boyutu hedeflerimizin ön koşuluydu.

Bu projenin hacminde performans farkı belirleyici değil; taşınabilirlik ve
okunabilirlik belirleyici.

---

## 6. Neden Kafka? (RabbitMQ / NATS / SQS değil)

| Kriter | **Kafka** | RabbitMQ | NATS JetStream | AWS SQS |
| :--- | :--- | :--- | :--- | :--- |
| Mesaj **replay** | Retention süresince istenen offset'ten | Hayır (tüketilen silinir) | Kısmi | Hayır |
| Consumer group ile ölçekleme | Partition tabanlı, yerleşik | Competing consumers | Queue group | Yerleşik |
| Sıralama garantisi | Partition içinde | Kuyruk içinde | Kısmi | FIFO kuyrukta |
| Kalıcılık | Diske append-only log | Opsiyonel | Opsiyonel | Yönetilen |
| Operasyonel yük | Yüksek | Orta | **Düşük** | Yok (yönetilen) |
| Bulut bağımsızlığı | Evet | Evet | Evet | **Hayır** |

**Seçim: Kafka.**

Belirleyici: **replay yeteneği.** Bir routing hatası fark edildiğinde son 7
günün event'lerini yeniden işleyebilmek, bu platformun operasyonel değerinin
merkezinde. RabbitMQ'da tüketilen mesaj yok olur — hatayı düzeltseniz bile
geçmişi kurtaramazsınız.

İkincil: Consumer group + partition modeli, yatay ölçeklemeyi uygulama kodu
yazmadan sağlıyor (`--scale routing-engine=2` ile doğrulandı).

**Bedeli:** En yüksek operasyonel yük. KRaft modu bunu belirgin biçimde
azaltıyor (Zookeeper yok).

---

## 7. Neden PostgreSQL + Transactional Outbox?

**Problem:** HTTP isteği başarıyla alındı, DB'ye yazıldı — ama Kafka o an
erişilemez. Event kayboldu mu?

**Çözüm:** Event ve outbox kaydı **aynı ACID transaction'ında** yazılır.
Kafka'ya publish ayrı bir poller tarafından asenkron yapılır. Ya ikisi de olur
ya hiçbiri.

**Neden ters sıra değil ("önce Kafka, sonra DB")?** Broker yazımı başarılı
olup DB yazımı çökerse, sistemde kaydı olmayan bir event dolaşırdı ve
idempotency kontrolü imkânsız hâle gelirdi.

**Neden Debezium/CDC değil?** Debezium ayrı bir Kafka Connect kümesi, WAL
yapılandırması ve replication slot yönetimi gerektirir. Bu ölçekte altyapı
maliyeti faydasını aşıyor. Poller ~500 ms gecikme ekler; teslimat zaten
asenkron olduğu için kabul edilebilir. Geçiş yolu açık: outbox tablosu aynen
kalır, yalnızca okuyucu değişir.

**Neden PostgreSQL?** ACID + JSONB + `SKIP LOCKED` + partial index üçlüsü.
MySQL'de JSONB indeksleme daha zayıf, MongoDB'de çok satırlı transaction
karmaşık ve outbox deseninin temeli tam olarak **transaction**.

---

## 8. Neden Redis?

Beş ayrı kullanım ve her birinin gerekçesi: **[`05-redis-usage.md`](05-redis-usage.md)**

Özet: Sıcak yolda (her ingest isteğinde) API key doğrulaması ve rate limit
kontrolü yapılıyor. Bunu Postgres'e sormak, p95 < 50 ms hedefini tek başına
imkânsız kılardı. Redis alt-milisaniye yanıt verir.

**Kritik ilke:** Redis hiçbir yerde tek doğruluk kaynağı değildir. Düştüğünde
sistem yavaşlar ama durmaz.

---

## 9. Neden JSON Schema? (Avro / Protobuf değil)

Karşılaştırma tablosu ve versiyonlama stratejisi:
**[`api-contracts/README.md`](api-contracts/README.md)**

Özet: Avro/Protobuf ikili biçimlerdir ve şema kaydı + kod üretimi gerektirir.
Bu, projenin temel hedefiyle ("yeni tool eklemek için kod değiştirme")
doğrudan çelişir. JSON Schema, şemanın kendisini JSONB olarak saklamayı ve
**admin panelinden çalışma zamanında tanımlamayı** mümkün kılıyor.

---

## 10. Neden golang-migrate? (düz SQL script değil)

| Kriter | **golang-migrate** | Düz `.sql` + init script | Flyway | Atlas |
| :--- | :--- | :--- | :--- | :--- |
| Versiyon takibi | `schema_migrations` tablosu | Yok | Var | Var |
| Geri alma (down) | Var | Yok | Pro sürümde | Var |
| Dirty state tespiti | Var | Yok | Var | Var |
| Bağımlılık | Tek binary | Yok | JVM | Tek binary |

Postgres'in `docker-entrypoint-initdb.d` mekanizması yalnızca **veri dizini
boşken** çalışır. İkinci bir şema değişikliği yapıldığında hiçbir şey olmaz —
mevcut kurulumlar sessizce geride kalır. golang-migrate her `up` komutunda
yalnızca uygulanmamış migration'ları çalıştırır ve yarım kalan bir migration'ı
"dirty" olarak işaretleyip devam etmeyi reddeder.

Flyway JVM gerektirdiği için elendi (ekstra ~700 MB image).

---

## 11. Neden Prometheus + Loki + Grafana?

| İhtiyaç | Araç | Neden bu? |
| :--- | :--- | :--- |
| Metrik | Prometheus | Pull modeli → servis izleme altyapısını tanımaz (gevşek bağlılık). Go client kütüphanesi birinci sınıf. |
| Log | Loki | Log **içeriğini** indekslemez, yalnızca etiketleri → Elasticsearch'e göre çok daha düşük kaynak. 10+ servisin JSON logu için ideal. |
| Görselleştirme | Grafana | Metrik (PromQL) ve log (LogQL) **aynı ekranda** korele edilebilir. |

Alternatif olarak Elasticsearch + Kibana değerlendirildi: bir geliştirici
makinesinde tek başına 2 GB+ RAM istiyor. Loki aynı işi ~70 MB ile yapıyor
(ölçüldü).

Pull modelinin ek faydası: `up` metriği bedava gelir — servis kapandığında
Prometheus bunu anında bilir (`ServiceDown` alert'i).

---

## 12. Mimari desenler ve gerekçeleri

| Desen | Çözdüğü problem | Uygulama |
| :--- | :--- | :--- |
| **Transactional Outbox** | "DB'ye yazdım, Kafka'ya basamadım" → veri kaybı | `services/event-ingest/outbox.go` |
| **Idempotent Consumer** | At-least-once teslimatın mükerrer yan etkisi | `event_instances(event_id)` UNIQUE, `delivery_tasks` UNIQUE |
| **Exponential Backoff + Jitter** | Geçici hata; eşzamanlı retry'ların hedefi tekrar çökertmesi | `internal/backoff/` |
| **Dead Letter Queue** | Kalıcı hatanın sonsuz retry ile kaynak yakması | `services/dlq-handler/` |
| **Circuit Breaker** | Çökmüş hedefe boşuna istek atıp worker tıkamak | `internal/breaker/` |
| **Bulkhead** | Yavaş komşunun tüm havuzu işgal etmesi | `services/delivery-worker/bulkhead.go` |
| **Distributed Lock** | Aynı görevin iki worker'da işlenmesi | `internal/lock/` |
| **Delayed Queue (DB)** | Kafka'da yerleşik gecikme olmaması | `delivery_tasks.next_attempt_at` |
| **Cache-Aside + Generation** | Kural okumanın sıcak yolda DB'ye gitmesi | `services/routing-engine/cache.go` |
| **Claim Check** | Büyük payload'ın kuyruktan geçmesi | Belge içeriği değil `storage_url` taşınır |

---

## 13. Bilinçli olarak KULLANILMAYANLAR

| Teknoloji | Neden değil? |
| :--- | :--- |
| Kubernetes | Görev tanımı Docker Compose istiyor; K8s bonus adım. Compose 20 servis için yeterli. |
| Service mesh (Istio/Linkerd) | mTLS ve trafik yönetimi bu ölçekte kazancından fazla karmaşıklık |
| Debezium / CDC | Poller yeterli, kurulum maliyeti yüksek — §7 |
| Schema Registry | JSON Schema DB'de saklanıyor; registry ek altyapı — bonus adım |
| Elasticsearch | Loki aynı işi ~30× daha az bellekle yapıyor — §11 |
| GraphQL | İstemci tek (Admin UI); esnek sorgu ihtiyacı yok, ek karmaşıklık |
| WebSocket | Akış tek yönlü (sunucu→tarayıcı); SSE yeterli ve daha basit. Görev tanımında da kapsam dışı. |
| Redis Cluster / Sentinel | Redis kritik yol değil; tek instance kabul edilebilir — `05-redis-usage.md` §SPOF |
| ORM | §3 |
