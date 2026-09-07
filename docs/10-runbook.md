# 10 — Operasyon Runbook

> "Servis çöktü, ne yaparım?" dokümanı. Her bölüm **belirti → tanı → çözüm**
> sırasını izler.

## Hızlı komutlar

```bash
docker compose ps                          # kim ayakta, kim healthy?
docker compose logs -f --tail=100 <servis> # log takibi
docker stats --no-stream                   # kaynak kullanımı
docker compose restart <servis>            # tek servis yeniden başlat
```

| Adres | Ne için |
| :--- | :--- |
| http://localhost:3000 | Admin UI — ilk bakılacak yer |
| http://localhost:3001 | Grafana — metrik ve log |
| http://localhost:9090/targets | Prometheus — hangi servis scrape edilemiyor |
| http://localhost:8085 | Kafka UI — topic, consumer lag |
| http://localhost:8092 | Webhook Sink — platform ne gönderdi |

---

## 1. Bir servis sürekli yeniden başlıyor

**Belirti:** `docker compose ps` → `Restarting (1)`

**Tanı:**
```bash
docker compose logs <servis> --tail=50
```

**En sık üç sebep:**

| Log satırı | Sebep | Çözüm |
| :--- | :--- | :--- |
| `yapılandırma hatası: X tanımlı değil` | `.env`'de eksik değişken | `.env.example` ile karşılaştırın |
| `SECRET_ENCRYPTION_KEY 32 byte olmalı` | Anahtar 64 hex karakter değil | `openssl rand -hex 32` |
| `postgres: bağlantı kurulamadı` | Postgres henüz hazır değil | Normal — 12 deneme yapar. Sürerse §2 |

Servisler **fail-fast** tasarlandı: eksik yapılandırmayla yarım çalışmaktansa
açılmamayı tercih ederler.

---

## 2. Postgres erişilemiyor

**Belirti:** `/readyz` → 503, loglarda `connection refused`

```bash
docker compose ps postgres
docker compose logs postgres --tail=40
docker compose exec postgres pg_isready -U hub_admin -d integration_hub
```

| Durum | Çözüm |
| :--- | :--- |
| Container yok | `docker compose up -d postgres` |
| `FATAL: password authentication failed` | `.env` değişti ama volume eski şifreyle kaldı → **§9 tam sıfırlama** |
| `too many connections` | `DB_MAX_CONNS`'u düşürün (servis × instance × MaxConns < 200) |
| Disk dolu | `docker system df` → §8 |

**Etki:** Tüm servisler durur. Ancak Kafka'daki mesajlar kaybolmaz; Postgres
döndüğünde tüketim kaldığı yerden devam eder.

---

## 3. Kafka erişilemiyor

```bash
docker compose logs kafka --tail=50
docker compose exec kafka /opt/kafka/bin/kafka-broker-api-versions.sh \
  --bootstrap-server localhost:9092
```

**Etki — kritik nokta:** Event kabulü **durmaz.** Outbox deseni sayesinde
event'ler Postgres'te birikir ve Kafka döndüğünde yayınlanır. Bu bilinçli bir
tasarım kararıdır.

```bash
# Ne kadar birikti?
docker compose exec postgres psql -U hub_admin -d integration_hub -c \
  "SELECT count(*) FROM hub.outbox_messages WHERE published_at IS NULL;"
```

| Log | Sebep | Çözüm |
| :--- | :--- | :--- |
| `Permission denied /var/lib/kafka/data` | Volume sahipliği | `docker compose up -d kafka-perms` sonra `kafka` |
| `InconsistentClusterIdException` | Volume eski `CLUSTER_ID` ile | `docker compose down -v` (**veri kaybı**) |
| OOM | Heap yetersiz | `KAFKA_HEAP_OPTS` ve container limitini artırın |

---

## 4. Consumer lag yükseliyor

**Alert:** `KafkaConsumerLagHigh`

```bash
docker compose exec kafka /opt/kafka/bin/kafka-consumer-groups.sh \
  --bootstrap-server localhost:9092 --all-groups --describe
```

**Tanı ağacı:**

```
Lag yüksek mi?
├── Consumer ayakta mı?  → docker compose ps
│   └── Hayır → başlat
├── Consumer hata mı basıyor?  → logs
│   └── Evet → hatayı çöz
├── Sürekli yeni event mi geliyor?  → Grafana ingest rate
│   └── Evet → ÖLÇEKLE:
│       docker compose up -d --scale routing-engine=3
└── Tek partition'da mı birikiyor?
    └── Evet → partition key dağılımı bozuk (Grafana 02 dashboard)
```

> Ölçekleme üst sınırı partition sayısıdır (varsayılan 6). Fazlası boşta bekler.

---

## 5. DLQ birikiyor

**Alert:** `DlqBacklogGrowing` (>10 kayıt, 2 dk)

**1. Neden düştüğüne bakın** — Admin UI → DLQ → `reason` sütunu:

| Sebep | Anlam | Aksiyon |
| :--- | :--- | :--- |
| `hedef 500 döndü` | Subscriber bozuk | Subscriber ekibiyle iletişime geçin |
| `hedef 404 döndü` | Yanlış URL | Abonelik URL'ini düzeltin |
| `hedef 401 döndü` | Subscriber imzayı doğrulayamıyor | `WEBHOOK_SIGNING_KEY` eşleşmesini kontrol edin |
| `http isteği başarısız: ... timeout` | Subscriber yavaş/erişilemez | `timeout_ms` artırın veya ağı kontrol edin |
| `maksimum deneme sayısı aşıldı` | Kalıcı hata | Kök nedeni bulun |

**2. Kök nedeni düzeltin.**

**3. Replay edin:**
- Admin UI → DLQ → seç → "Yeniden gönder"
- Veya toplu: aynı subscriber'ın tüm kayıtları

```bash
curl -X POST http://localhost:8080/api/v1/dlq/replay \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"subscription_id":"<id>","limit":500}'
```

> **Önce düzeltin, sonra replay edin.** Bozuk hedefe replay yalnızca DLQ'yu
> yeniden doldurur.

**4. İşlenmeyecek mesajlar:** "Yok say" (discard) ile işaretleyin.

---

## 6. Bir subscriber hiç webhook almıyor

**Sırayla kontrol edin:**

```
1. Event platforma ulaştı mı?
   Admin UI → Event Akışı → event görünüyor mu?
   Hayır → publisher tarafı sorunu (§7)

2. Event'in durumu ne?
   'no_match'  → abonelik veya kural eşleşmiyor (adım 3)
   'accepted'  → routing-engine işlememiş (logs)
   'routed'    → teslimat aşamasında (adım 5)

3. Abonelik var mı ve aktif mi?
   Admin UI → Abonelikler → doğru event tipi + is_active

4. Kural engelliyor mu?
   Admin UI → Yönlendirme Kuralları → "Kuralı test et"
   Örnek belgeyle deneyin: eşleşiyor mu?

5. Teslimat denendi mi?
   Admin UI → Teslimatlar → yanıt kodu ne?
   Hiç kayıt yok → circuit breaker açık olabilir:
     curl -s http://localhost:8083/internal/breakers | jq
```

---

## 7. Publisher event gönderemiyor

| Kod | Sebep | Çözüm |
| :--- | :--- | :--- |
| `401 invalid_api_key` | Anahtar yanlış/iptal/grace bitti | Yeni anahtar üretin |
| `401 invalid_signature` | İmza tutmuyor | **En sık sebep:** gövde imzalandıktan sonra yeniden serileştiriliyor |
| `401 stale_timestamp` | Saat kayması > 5 dk | Publisher sistemini NTP ile senkronize edin |
| `403 source_tool_mismatch` | `source_tool` ≠ anahtar sahibi slug'ı | Zarftaki değeri düzeltin |
| `400 unknown_event_type` | Tip tanımlı değil / henüz yüklenmedi | Admin'den tanımlayın, ≤30 sn bekleyin |
| `400 schema_validation_failed` | Payload şemaya uymuyor | Yanıttaki `details` alanına bakın |
| `429 rate_limited` | Kota aşıldı | `Retry-After` kadar bekleyin veya limiti artırın |

Anahtar önbelleği 30 sn TTL'lidir — yeni/iptal edilmiş anahtar en geç 30
saniyede etkili olur.

---

## 8. Disk doluyor

```bash
docker system df
docker compose exec postgres psql -U hub_admin -d integration_hub -c "
  SELECT relname, pg_size_pretty(pg_total_relation_size(relid)) AS size
  FROM pg_catalog.pg_statio_user_tables
  WHERE schemaname='hub' ORDER BY pg_total_relation_size(relid) DESC LIMIT 8;"
```

| Kaynak | Çözüm |
| :--- | :--- |
| Docker build cache | `docker builder prune -af` (genelde en büyük kalem) |
| Kullanılmayan image | `docker image prune -a -f` |
| `delivery_attempts` büyük | `ATTEMPT_RETENTION_DAYS` düşürün |
| `event_instances` büyük | `EVENT_RETENTION_DAYS` düşürün |
| Prometheus | `--storage.tsdb.retention.time` (varsayılan 7d) |
| Loki | `retention_period` (varsayılan 168h) |
| Container logları | Zaten sınırlı: 10 MB × 3 dosya |

Retention job saatte bir çalışır; hemen tetiklemek için:
`docker compose restart dlq-handler` (ilk tur 1 dk sonra).

---

## 9. Tam sıfırlama

```bash
docker compose down -v --remove-orphans   # ⚠️ TÜM VERİ SİLİNİR
docker compose up -d --build
```

Mock tool'lar yeniden kaydolur, event tipleri seed edilir, admin kullanıcısı
`.env`'deki bilgilerle yeniden oluşturulur.

Yalnızca image'ları tazelemek için (veri korunur):
```bash
docker compose up -d --build --force-recreate
```

---

## 10. Şifre / erişim sorunları

| Sorun | Çözüm |
| :--- | :--- |
| Admin şifresi bilinmiyor | `grep ADMIN_PASSWORD .env` |
| Admin şifresi çalışmıyor | Kullanıcı ilk açılışta oluşturuldu; `.env`'i sonradan değiştirmek mevcut kullanıcıyı güncellemez. UI'dan değiştirin veya §9 |
| Grafana girişi | `grep GRAFANA_ADMIN_PASSWORD .env` |
| pgAdmin açılmıyor | `PGADMIN_EMAIL` geçerli bir TLD olmalı (`.local` reddedilir) |
| Tüm oturumlar kapansın | `JWT_SECRET`'i değiştirip `docker compose up -d admin-api` |

---

## 11. Performans düşük

**Ölçün, tahmin etmeyin:** Grafana → "03 · Teslimat Boru Hattı" → aşama
gecikmeleri.

| Yavaş aşama | Olası sebep | Çözüm |
| :--- | :--- | :--- |
| Ingest p95 > 50 ms | Redis yavaş / DB havuzu dolu | `docker stats`, `DB_MAX_CONNS` |
| Routing gecikmesi yüksek | Consumer lag | Ölçekle (§4) |
| Webhook gecikmesi yüksek | Subscriber yavaş | `timeout_ms`, `DELIVERY_MAX_PER_SUBSCRIBER` |
| Retry oranı yüksek | Subscriber kararsız | DLQ sebeplerini inceleyin |

**Yük testi:**
```bash
docker run --rm --network integration-hub-net -v "$PWD/tests/load:/scripts" \
  -e INGEST_URL=http://event-ingest:8081/api/v1/events \
  grafana/k6:0.55.0 run /scripts/k6-ingest.js
```

---

## 12. Sağlık kontrolü (günlük)

```bash
docker compose ps                    # hepsi healthy mi?
curl -s localhost:8084/internal/status | jq   # DLQ bekleyen sayısı
curl -s "localhost:9090/api/v1/query?query=hub_outbox_pending_messages" | jq
```

Grafana → "01 · Sistem Sağlığı" → tek ekranda genel durum.

| Metrik | Sağlıklı | Dikkat | Kritik |
| :--- | :--- | :--- | :--- |
| Teslimat başarı oranı | > %98 | %90-98 | < %90 |
| Consumer lag | < 100 | 100-1000 | > 1000 |
| DLQ bekleyen | 0 | 1-10 | > 10 |
| Outbox bekleyen | < 10 | 10-500 | > 500 |
| Ingest p95 | < 50 ms | 50-200 ms | > 200 ms |
