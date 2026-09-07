# 01 - Project Overview, Requirements & Architecture

## 1. System Vision & Purpose
`integration-hub`, MindMons PaaS (Platform as a Service) ekosisteminde yer alan birbirinden bağımsız yazılım ürünlerinin ve servislerinin (tool'ların) birbirleriyle gevşek bağlı (loosely coupled), dinamik, güvenli ve ölçeklenebilir biçimde haberleşmesini sağlayan kurumsal bir entegrasyon ve event orkestrasyon platformudur.

Platform; hiçbir servisin diğerine doğrudan bağımlı (hardcoded) olmadığı bir tak-çıkart mimarisinin merkezinde yer alır[cite: 1]. Harici ve dahili sistemlerden gelen yüksek hacimli webhook ve API event'lerini HTTP üzerinden kabul eder (Ingestion), veri kaybını önlemek için kalıcı kılar (Transactional Outbox), dinamik filtre ve dönüşüm kurallarına göre yönlendirir (Routing), hedef sistemlere garantili teslimat sağlar (Delivery), geçici arızalarda üstel geri çekilme ile yeniden dener (Exponential Backoff Retry) ve kalıcı hatalı mesajları karantinaya alıp operasyonel kurtarma imkânı sunar (DLQ / Recovery)[cite: 1].

---

## 2. Architecture Diagram & System Boundaries

![Architecture Diagram](diagrams/architecture-diagram.png)

### 2.1 System Boundaries & Component Definitions
* **`integration-hub` Core Boundary:** Ingestion, Routing, Delivery, DLQ, Admin API mikroservisleri ile PostgreSQL, Redis, Apache Kafka ve Observability bileşenlerini barındıran çekirdek platform katmanıdır[cite: 1].
* **Publisher Boundary:** Sisteme güvenli kanaldan event fırlatan dahili MindMons tool'ları veya `Mock Publisher` simülatörüdür[cite: 1, 1].
* **Subscriber Boundary:** Platformun webhook gönderimi yaptığı harici servisler, müşteri uç noktaları (`Mock Subscriber`) ve test amaçlı `Webhook Sink` alıcılarıdır[cite: 1, 1].
* **Management Boundary:** Sistem yöneticilerinin sisteme JWT ile eriştiği Admin UI ve API kontrol düzlemidir[cite: 1, 1].

---

## 3. Actor Analysis & Communication Matrix

### 3.1 User & Actor Characteristics
* **Admin User (Human):** Web arayüzü üzerinden tool kayıtlarını, API key üretim ve rotasyonunu, yönlendirme kurallarını, rate limit tanımlarını yöneten, DLQ'daki hatalı mesajları inceleyip replay tetikleyen ve sistem denetim loglarını izleyen yönetici aktördür[cite: 1, 1].
* **Publisher Tool (System Actor):** Platforma kayıtlı olan ve kendi etki alanında bir değişiklik olduğunda (kullanıcı kaydı, belge üretimi, doğrulama vb.) HTTP POST ile platforma HMAC imzalı event fırlatan dış veya iç servistir[cite: 1].
* **Subscriber Tool (System Actor):** Platformda belirli event tiplerine abone olan, kendisine iletilen HTTP Webhook çağrılarını karşılayan, platformun HMAC imzasını doğrulayıp işlemi yürüten hedef servistir[cite: 1, 1].

### 3.2 Actor Input / Output Matrix

| Aktör | Tip | Sisteme Verdiği Girdi (Input) | Sistemden Aldığı Çıktı (Output) |
| :--- | :--- | :--- | :--- |
| **Admin User** | İnsan | Kimlik bilgileri (JWT Login), Tool/Kural tanımları, DLQ Replay komutu, Filtre sorguları[cite: 1, 1] | Yönetim Paneli (UI), Metrik grafikleri, Canlı event logları, Audit denetim raporları[cite: 1, 1] |
| **Publisher Tool** | Harici Servis | `POST /api/v1/events`, `X-Api-Key`, `X-Signature-SHA256`, Event JSON Payload[cite: 1] | HTTP Status (`202 Accepted` / `400` / `429`), `event_id`, Hata detayı[cite: 1] |
| **Subscriber Tool** | Harici Servis | Webhook URL kaydı, Webhook HTTP Yanıtı (`200 OK` / 5xx / 4xx)[cite: 1] | HMAC-SHA256 imzalı HTTP POST Webhook yükü (`X-Hub-Signature`)[cite: 1] |
| **Event Ingest** | İç Servis | HTTP Event İstekleri, Redis Rate Limit durumu[cite: 1] | PostgreSQL Outbox kaydı, Kafka `events.ingested` topic mesajı[cite: 1] |
| **Routing Engine** | İç Servis | Kafka `events.ingested` mesajları, Redis Kural Önbelleği[cite: 1] | Kafka `events.delivery` teslimat görevleri[cite: 1] |
| **Delivery Worker** | İç Servis | Kafka `events.delivery` görevleri, Redis Dağıtık Kilit[cite: 1] | Subscriber'a HTTP POST, Postgres `delivery_attempts` logu, Kafka `events.dlq` mesajı[cite: 1] |
| **DLQ Handler** | İç Servis | Kafka `events.dlq` mesajları, Admin Replay komutları[cite: 1] | Postgres `dlq_events` kaydı, Kafka `events.delivery` replay mesajı[cite: 1] |

### 3.3 Protocol & Communication Matrix

| Kaynak | Hedef | Protokol / Model | İletişim Tipi | Açıklama |
| :--- | :--- | :--- | :--- | :--- |
| Mock Publisher | Event Ingest Service | HTTPS (API Key + HMAC) | Senkron | Event kabul ve anlık imza doğrulaması[cite: 1] |
| Event Ingest | Redis | Redis Protocol (TCP) | Senkron | Token Bucket algoritmasıyla rate limit kontrolü[cite: 1] |
| Event Ingest | PostgreSQL | SQL (Transactional Outbox) | Senkron | Event yükünün kalıcı saklanması (Durable Inbox/Outbox)[cite: 1] |
| Event Ingest | Kafka (`events.ingested`) | TCP (Kafka Producer) | Asenkron | Doğrulanmış ham event yayını[cite: 1] |
| Routing Engine | Kafka (`events.ingested`) | TCP (Kafka Consumer) | Asenkron | Ham event'lerin kuyruktan tüketilmesi[cite: 1] |
| Routing Engine | Redis | In-Memory Cache | Senkron | Aktif abonelik ve kural önbelleği okuma[cite: 1] |
| Routing Engine | Kafka (`events.delivery`) | TCP (Kafka Producer) | Asenkron | Hedefe özel teslimat görevlerinin yayını[cite: 1] |
| Delivery Worker | Kafka (`events.delivery`) | TCP (Kafka Consumer) | Asenkron | Teslimat işlerinin kuyruktan tüketilmesi[cite: 1] |
| Delivery Worker | Redis | Redlock / Distributed Lock | Senkron | Eşzamanlı mükerrer webhook gönderimini engelleme[cite: 1] |
| Delivery Worker | External Destinations | HTTPS Webhook + HMAC | Senkron | Hedef subscriber'a HTTP POST teslimatı[cite: 1] |
| Delivery Worker | PostgreSQL | SQL | Senkron | Deneme durumu, latency ve yanıtın kaydedilmesi[cite: 1] |
| Delivery Worker | Kafka (`events.dlq`) | TCP (Kafka Producer) | Asenkron | Max retry aşımı durumunda DLQ kuyruğuna aktarım[cite: 1] |
| DLQ Handler | Kafka (`events.dlq`) | TCP (Kafka Consumer) | Asenkron | Karantinaya alınan mesajların tüketilmesi[cite: 1] |
| DLQ Handler | PostgreSQL | SQL | Senkron | `dlq_events` tablosuna kalıcı kayıt[cite: 1] |
| DLQ Handler | Kafka (`events.delivery`) | TCP (Kafka Producer) | Asenkron | Replay tetiklendiğinde teslimat kuyruğuna geri basım[cite: 1] |
| Admin UI / API | PostgreSQL / Redis | REST / HTTPS / SQL | Senkron | Yönetim, konfigürasyon ve veri sorgulama[cite: 1] |

---

## 4. Operating Environment & Technical Constraints
* **Konteynerleştirme:** Tüm platform bileşenleri, veri tabanları ve test mock servisleri tek bir `docker-compose.yml` dosyası üzerinden ayağa kalkmalı; harici manuel kurulum gerektirmemelidir[cite: 1].
* **Resource Limits:** Her servis için CPU ve Memory limitleri tanımlanmalı, darboğazlar engellenmelidir[cite: 1].
* **Stable Versions:** PostgreSQL 16, Redis 7 ve Apache Kafka (KRaft mode) güncel stable/LTS versiyonları kullanılmalı; `latest` tag'inden kaçınılmalıdır[cite: 1].
* **Stateless Servisler:** Tüm Go mikroservisleri stateless tasarlanmalı; state tamamen PostgreSQL, Redis ve Kafka üzerinde tutulmalıdır[cite: 1].

---

## 5. Specific Requirements

### 5.1 Functional Requirements (FR)

#### Module 1: Tool & Credential Management
* **FR-1.1 (Tool Registration):** Sistem, ekosisteme katılacak yeni publisher ve subscriber araçlarının CRUD işlemlerini desteklemelidir[cite: 1].
* **FR-1.2 (Key Generation & Hashing):** Her tool için benzersiz bir API Key ve HMAC Secret üretilmelidir; açık metin anahtarlar veritabanında saklanmamalı, Argon2id veya SHA-256 hash'i tutulmalıdır[cite: 1].
* **FR-1.3 (Key Rotation):** Sistem, eski anahtarlar için geçiş süresi (grace period) tanıyan güvenli anahtar yenileme mekanizması sunmalıdır[cite: 1].

#### Module 2: Event Ingestion & Outbox Persistence
* **FR-2.1 (HTTP Ingest Endpoint):** `POST /api/v1/events` endpoint'i üzerinden harici sistemlerden gelen JSON event'leri kabul etmelidir[cite: 1].
* **FR-2.2 (Authentication & Signature Verification):** İsteklerdeki `X-Api-Key` ve `X-Signature-SHA256` başlıkları doğrulanmalı; yetkisiz veya bozuk imzalı istekler `401/403` ile reddedilmelidir[cite: 1, 1].
* **FR-2.3 (JSON Schema Validation):** Gelen event payload'ı, tanımlı `event_types` şemasına göre doğrulanmalı; şema dışı istekler `400 Bad Request` dönmelidir[cite: 1].
* **FR-2.4 (Transactional Outbox):** Doğrulanan event'ler önce `event_instances` tablosuna yazılmalı, ardından `events.ingested` topic'ine aktarılmalıdır[cite: 1].
* **FR-2.5 (Idempotency Enforcement):** Aynı `event_id` ile gelen mükerrer istekler veritabanına ve Kafka'ya tekrar yazılmamalı, önceki yanıtın aynısı dönmelidir[cite: 1].

#### Module 3: Routing Engine
* **FR-3.1 (Event Consumption):** `events.ingested` topic'inden ham event verilerini tüketmelidir[cite: 1].
* **FR-3.2 (Rule Evaluation):** Payload ve metadata alanlarını JSONPath/koşul ifadelerine göre değerlendirmelidir[cite: 1].
* **FR-3.3 (Delivery Task Generation):** Eşleşen her aktif abonelik için bağımsız bir teslimat görevi üreterek `events.delivery` topic'ine basmalıdır[cite: 1].

#### Module 4: Delivery Worker & Webhook Dispatch
* **FR-4.1 (Task Dispatch):** `events.delivery` kuyruğundaki görevleri alarak hedef subscriber URL'ine HTTP POST isteği atmalıdır[cite: 1].
* **FR-4.2 (HMAC Signature Header):** Gönderilen webhook'un `X-Hub-Signature` başlığına platformun HMAC-SHA256 imzası eklenmelidir[cite: 1].
* **FR-4.3 (Exponential Backoff Retry):** Geçici hatalarda (5xx, 429, timeout) katlanarak artan aralıklarla (1s, 2s, 4s, 8s, 16s) yeniden deneme yapılmalıdır[cite: 1].
* **FR-4.4 (Attempt Logging):** Her teslimat denemesinin HTTP durum kodu, yanıt gövdesi özeti, latency ve hata mesajı `delivery_attempts` tablosuna işlenmelidir[cite: 1].

#### Module 5: Dead Letter Queue (DLQ) & Recovery
* **FR-5.1 (DLQ Isolation):** Maksimum deneme limitini (max_retries = 5) aşan mesajlar `events.dlq` topic'ine ve `dlq_events` tablosuna yönlendirilmelidir[cite: 1].
* **FR-5.2 (Manual & Bulk Replay):** Admin API üzerinden seçilen tekil veya toplu DLQ kayıtları yeniden işlenmek üzere `events.delivery` topic'ine geri basılabilmelidir[cite: 1].

#### Module 6: Administration & Audit
* **FR-6.1 (Admin Auth):** Admin paneline erişim JWT tabanlı kimlik doğrulama ile sınırlandırılmalıdır[cite: 1].
* **FR-6.2 (Audit Logging):** Tüm kural tanımlama, silme, anahtar yenileme ve DLQ replay aksiyonları `audit_logs` tablosunda saklanmalıdır[cite: 1].

---

### 5.2 Non-Functional Requirements (NFR)

#### NFR-1: Reliability & Data Durability
* **NFR-1.1 (At-Least-Once Delivery):** Kafka çökmesi veya ağ kesintisi durumunda dahi Transactional Outbox deseni sayesinde sıfır veri kaybı (Zero Data Loss) sağlanmalıdır[cite: 1].
* **NFR-1.2 (Graceful Degradation):** Redis önbelleği erişilemez olduğunda routing kuralları doğrudan PostgreSQL'den okunabilmeli (failover) ve sistem çalışmaya devam etmelidir[cite: 1].

#### NFR-2: Performance & Scalability
* **NFR-2.1 (Ingestion Latency):** Ingest HTTP isteklerinin %95'i (p95) 50 milisaniyenin altında yanıtlanmalıdır[cite: 1].
* **NFR-2.2 (Horizontal Scalability):** `routing-engine` ve `delivery-worker` servisleri, Kafka consumer group mekanizması sayesinde veri bütünlüğü bozulmadan çoklu instance olarak çalışabilmelidir[cite: 1].

#### NFR-3: Security
* **NFR-3.1 (Timing Attack Protection):** HMAC imza doğrulamalarında sabit süreli karşılaştırma (`crypto/subtle.ConstantTimeCompare`) kullanılmalıdır[cite: 1].
* **NFR-3.2 (Rate Limiting):** Redis tabanlı Token Bucket algoritması ile hem IP hem de Tool API Key bazında sınırlandırma uygulanmalıdır[cite: 1].
* **NFR-3.3 (Least Privilege):** Docker container'ları root olmayan kullanıcılarla (`non-root user`) çalıştırılmalıdır[cite: 1].

#### NFR-4: Observability
* **NFR-4.1 (Metrics Scraping):** Her mikroservis `/metrics` endpoint'i üzerinden Prometheus formatında metrik sunmalıdır[cite: 1].
* **NFR-4.2 (Structured Logs):** Loglar `stdout` üzerinden standart JSON formatında ve `trace_id` içerecek biçimde basılmalıdır[cite: 1].

---

## 6. Verification & Acceptance Criteria Matrix

| Gereksinim Kodu | Doğrulama Yöntemi | Kabul Kriteri |
| :--- | :--- | :--- |
| **FR-2.1 / FR-2.5** | Integration Test | Mock publisher'dan atılan event Kafka'da görülmeli; aynı `event_id` mükerrer kayıt üretmemelidir[cite: 1]. |
| **FR-4.3 / FR-5.1** | Failure Simulation | Hedef uç nokta 500 dönerse 5 kez backoff ile denenmeli ve 6. adımda DLQ tablosuna düşmelidir[cite: 1]. |
| **NFR-3.2** | Load / Abuse Test | Tanımlı kota aşıldığında Event Ingest servisi anında `429 Too Many Requests` yanıtı üretmelidir[cite: 1]. |
| **NFR-4.1** | E2E Monitoring | Grafana üzerinde consumer lag ve end-to-end latency grafikleri canlı akmalıdır[cite: 1]. |

---

## 7. Scope & Out of Scope

### In Scope (Kapsam Dahilinde)
* Event Ingest, Routing Engine, Delivery Worker, DLQ Handler ve Admin API Go mikroservisleri[cite: 1].
* PostgreSQL 16, Redis 7 ve Apache Kafka (KRaft) kümeleme mimarisi[cite: 1].
* Prometheus, Loki ve Grafana gözlemlenebilirlik araçları[cite: 1].
* Mock Publisher, Mock Subscriber ve Webhook Sink test modülleri[cite: 1].
* React / TypeScript Admin Dashboard arayüzü[cite: 1].

### Out of Scope (Kapsam Dışı)
* Çift yönlü WebSocket istemci streaming desteği (yalnızca tek yönlü HTTP Webhook teslimatı yapılır)[cite: 1].
* Multi-region (çoklu coğrafi bölge) aktif-aktif veri replikasyonu.
* Dağıtık transaction orchestrator (2PC / SAGA) yönetimi.