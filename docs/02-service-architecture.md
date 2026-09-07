# 02 - Service Architecture & Component Specifications

## 1. Core Service Definitions

### Event Ingest Go Service

* **Görevi:** Dış sistemlerden (`Mock Publisher`) gelen HTTP POST isteklerini karşılar, API Key ve HMAC imzasını doğrular.
* **Bağımlılıklar & Akış:**
  * **Redis:** Anlık rate limit kontrolü (`Rate Limit Check`) yapar. Limit aşımında `429 Too Many Requests` döner.
  * **PostgreSQL:** Veri kaybını önlemek için event payload'unu Transactional Outbox tablosuna yazar.
  * **Kafka (`events.ingested`):** Doğrulanan event'i asenkron işlenmek üzere topic'e basar.

### Routing Engine Go Service

* **Görevi:** `events.ingested` topic'ini dinler, gelen event tipine ve payload içeriğine göre tanımlı yönlendirme kurallarını değerlendirir.
* **Bağımlılıklar & Akış:**
  * **Redis:** Aktif routing kurallarını in-memory önbellekten (`Rule Cache`) çeker.
  * **Kafka (`events.delivery`):** Kural eşleşmesi sağlanan her hedef uç nokta için ayrı bir teslimat görevi oluşturup topic'e iletir.

### Delivery Worker Go Service

* **Görevi:** `events.delivery` topic'inden gelen görevleri tüketir, hedef webhook uç noktalarına (`Mock Subscriber`, `Webhook Sink`) güvenli HTTP POST isteği gönderir.
* **Bağımlılıklar & Akış:**
  * **Redis:** Aynı event üzerinde birden fazla worker'ın çakışmasını engellemek için `Distributed Lock` mekanizmasını çalıştırır.
  * **PostgreSQL:** Her teslimat denemesinin durum kodunu, yanıt süresini ve hata detayını `delivery_attempts` tablosuna yazar.
  * **Failure Handling:** Başarısız isteklerde exponential backoff uygular. Maksimum yeniden deneme sayısı aşıldığında (`max retries exceeded`) mesajı `events.dlq` topic'ine iletir.

### DLQ Handler & Recovery Service

* **Görevi:** `events.dlq` topic'ini dinler. Başarısız teslimatları analiz ve denetim amacıyla `PostgreSQL (DLQ Events)` tablosunda saklar.
* **Replay / Publish:** Admin paneli veya API üzerinden tetiklenen yeniden deneme (replay) isteklerinde event'i doğrudan `events.delivery` topic'ine geri basar.

### Admin API & Admin UI Frontend

* **Admin UI:** Sistem yöneticisinin kuralları, teslimat geçmişini, DLQ mesajlarını ve metrikleri izlediği web arayüzüdür.
* **Admin API (Go Service):** UI'dan gelen REST isteklerini karşılar, PostgreSQL üzerinde CRUD operasyonlarını yürütür, Redis kural önbelleğini geçersiz kılar/tazeler ve DLQ replay komutlarını tetikler.

---

## 2. Kafka Topic Pipeline

| Topic Adı | Producer | Consumer | Mesaj İçeriği |
| :--- | :--- | :--- | :--- |
| `events.ingested` | Event Ingest Service | Routing Engine | Doğrulanmış ham event payload ve metadata |
| `events.delivery` | Routing Engine / DLQ Handler | Delivery Worker | Hedef URL, HMAC header ve teslimat konfigürasyonu |
| `events.dlq` | Delivery Worker | DLQ Handler | Hata detayı, deneme sayısı ve orijinal event yükü |

---

## 3. Observability & Monitoring Pipeline

* **Metrik Toplama:** Tüm Go servisleri (`/metrics` endpoint'i üzerinden) ve altyapı exporter'ları (Postgres, Kafka, Redis Exporters) Prometheus tarafından düzenli aralıklarla scrape edilir.
* **Log Yönetimi:** Mikroservislerin stdout'a bastığı structured JSON loglar Loki tarafından toplanır.
* **Görselleştirme:** Grafana Dashboard üzerinde PromQL ve LogQL sorguları birleştirilerek sistem sağlığı, consumer lag, teslimat başarı oranı ve DLQ alarmları anlık izlenir.
