# 07 — Tool Onboarding Rehberi

> Bu doküman, ekosisteme yeni bir tool bağlamak isteyen **dış geliştirici**
> için yazılmıştır. Platform ekibine hiçbir şey sormadan entegrasyonu
> tamamlayabilmeniz hedeflenmiştir.
>
> Çalıştırılabilir referans uygulamalar:
> `mock-tools/mock-publisher-1/` (publisher) ve
> `mock-tools/mock-subscriber-1/` (subscriber).

---

## Hızlı bakış

```
        ┌─────────────┐   1. kaydol            ┌───────────┐
        │  Sizin      │ ─────────────────────► │ Admin API │
        │  Tool'unuz  │ ◄───────────────────── │           │
        └─────────────┘   2. API key + secret  └───────────┘
              │
              │ 3. imzalı event
              ▼
        ┌──────────────┐
        │ Event Ingest │  ──► platform ──► diğer tool'lara webhook
        └──────────────┘
```

| Rolünüz | Yapmanız gereken |
| :--- | :--- |
| **Publisher** | Kaydol → API key al → event tipi tanımla → imzalı POST at |
| **Subscriber** | Kaydol → webhook endpoint'i aç → abone ol → imza doğrula |

---

## Adım 1 — Yönetici oturumu

Tüm yönetim çağrıları JWT ister.

```bash
ADMIN_API=http://localhost:8080

TOKEN=$(curl -s -X POST "$ADMIN_API/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"email":"admin@mindmons.local","password":"<.env icindeki ADMIN_PASSWORD>"}' \
  | jq -r .access_token)
```

> Access token 15 dakika geçerlidir. Uzun süren script'lerde
> `/api/v1/auth/refresh` ile yenileyin.

---

## Adım 2 — Tool kaydı

```bash
curl -s -X POST "$ADMIN_API/api/v1/tools" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "slug": "billing-service",
    "name": "Billing Service",
    "kind": "publisher",
    "description": "Fatura yasam dongusu event'leri",
    "contact_email": "billing-team@example.com"
  }' | jq
```

| Alan | Kural |
| :--- | :--- |
| `slug` | Küçük harf, rakam, tire. 3-64 karakter. **Değiştirilemez.** |
| `kind` | `publisher` \| `subscriber` \| `both` |

`slug` önemlidir: event zarfındaki `source_tool` alanı **birebir** bununla
eşleşmelidir; aksi halde platform `403 source_tool_mismatch` döner.

---

## Adım 3 — API anahtarı üretimi

```bash
TOOL_ID=<onceki adimdan gelen id>

curl -s -X POST "$ADMIN_API/api/v1/tools/$TOOL_ID/keys" \
  -H "Authorization: Bearer $TOKEN" | jq
```

Yanıt:

```json
{
  "key_prefix": "ih_a1b2c3d4e5f6",
  "api_key": "ih_a1b2c3d4e5f6_9f8e7d6c5b4a...",
  "hmac_secret": "4f3e2d1c0b9a8f7e...",
  "status": "active",
  "warning": "Bu değerler yalnızca BİR KEZ gösterilir."
}
```

### ⚠️ Kritik

`api_key` ve `hmac_secret` **yalnızca bu yanıtta** görünür.

Platform tarafında:
- `api_key` → SHA-256 **hash'i** saklanır, geri döndürülemez.
- `hmac_secret` → AES-256-GCM ile **şifreli** saklanır.

Kaybederseniz tek çözüm yeni anahtar üretmektir.

### Nasıl saklamalısınız?

| Ortam | Öneri |
| :--- | :--- |
| Yerel geliştirme | `.env` dosyası (`.gitignore` içinde) |
| Kubernetes | Secret |
| Bulut | AWS Secrets Manager / GCP Secret Manager / Vault |
| **Asla** | Kod deposu, log, hata mesajı, konteyner build arg'ı |

---

## Adım 4 — Event tipi tanımı (yalnızca publisher)

Yayınlayacağınız her event tipinin bir JSON Schema kontratı olmalıdır.

```bash
curl -s -X POST "$ADMIN_API/api/v1/event-types" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "invoice.paid",
    "version": 1,
    "owner_tool_id": "'"$TOOL_ID"'",
    "description": "Fatura odemesi tamamlandiginda yayinlanir",
    "json_schema": {
      "$schema": "https://json-schema.org/draft/2020-12/schema",
      "type": "object",
      "required": ["invoice_id", "amount", "currency"],
      "additionalProperties": true,
      "properties": {
        "invoice_id": { "type": "string", "minLength": 1 },
        "amount":     { "type": "number", "minimum": 0 },
        "currency":   { "type": "string", "enum": ["TRY", "USD", "EUR"] },
        "paid_at":    { "type": "string", "format": "date-time" }
      }
    }
  }' | jq
```

Şema derlenemezse `400` alırsınız — bozuk şema veritabanına asla girmez.

Yeni tip en geç **30 saniye** içinde event-ingest tarafından tanınır
(şema yenileme aralığı). Kontrol:

```bash
curl -s http://localhost:8081/api/v1/event-types | jq
```

---

## Adım 5A — Publisher: event gönderme

### İmzalama kuralı

```
imzalanan_metin = "<timestamp>" + "." + "<ham JSON gövde>"
imza            = hex( HMAC_SHA256(hmac_secret, imzalanan_metin) )
```

| Başlık | Değer |
| :--- | :--- |
| `X-Api-Key` | Tam API anahtarı |
| `X-Timestamp` | Unix saniye (veya RFC3339) |
| `X-Signature-SHA256` | Yukarıdaki hex imza |
| `X-Trace-Id` | *(opsiyonel)* uçtan uca izleme kimliği |

### ⚠️ En sık yapılan hata

**Gövdeyi imzaladıktan sonra yeniden serileştirmeyin.** JSON alan sırası veya
boşluk farkı imzayı bozar. Aynı byte dizisini hem imzalayın hem gönderin.

### Örnek — bash

```bash
API_KEY="ih_a1b2c3d4e5f6_..."
HMAC_SECRET="4f3e2d1c0b9a8f7e..."
TS=$(date +%s)

BODY=$(cat <<JSON
{"event_id":"$(uuidgen)","event_type":"invoice.paid","event_version":1,"source_tool":"billing-service","timestamp":"$(date -u +%Y-%m-%dT%H:%M:%SZ)","payload":{"invoice_id":"INV-2026-001","amount":1499.90,"currency":"TRY"}}
JSON
)

SIG=$(printf '%s.%s' "$TS" "$BODY" | openssl dgst -sha256 -hmac "$HMAC_SECRET" -hex | awk '{print $2}')

curl -s -X POST http://localhost:8081/api/v1/events \
  -H "X-Api-Key: $API_KEY" \
  -H "X-Timestamp: $TS" \
  -H "X-Signature-SHA256: $SIG" \
  -H 'Content-Type: application/json' \
  -d "$BODY" | jq
```

### Örnek — Go

```go
body, _ := json.Marshal(envelope)          // BİR KEZ serileştir
ts := strconv.FormatInt(time.Now().Unix(), 10)

mac := hmac.New(sha256.New, []byte(hmacSecret))
mac.Write([]byte(ts + "." + string(body))) // AYNI byte dizisi
sig := hex.EncodeToString(mac.Sum(nil))

req, _ := http.NewRequest("POST", ingestURL, bytes.NewReader(body))
req.Header.Set("X-Api-Key", apiKey)
req.Header.Set("X-Timestamp", ts)
req.Header.Set("X-Signature-SHA256", sig)
req.Header.Set("Content-Type", "application/json")
```

### Örnek — Node.js

```js
const body = JSON.stringify(envelope);      // BİR KEZ
const ts = Math.floor(Date.now() / 1000).toString();

const sig = crypto.createHmac('sha256', hmacSecret)
  .update(`${ts}.${body}`)
  .digest('hex');

await fetch(ingestUrl, {
  method: 'POST',
  headers: {
    'X-Api-Key': apiKey,
    'X-Timestamp': ts,
    'X-Signature-SHA256': sig,
    'Content-Type': 'application/json',
  },
  body,                                      // AYNI string
});
```

### Örnek — Python

```python
body = json.dumps(envelope, separators=(',', ':'))   # BİR KEZ
ts = str(int(time.time()))

sig = hmac.new(hmac_secret.encode(), f"{ts}.{body}".encode(), hashlib.sha256).hexdigest()

requests.post(ingest_url, data=body, headers={
    "X-Api-Key": api_key,
    "X-Timestamp": ts,
    "X-Signature-SHA256": sig,
    "Content-Type": "application/json",
})
```

### Başarılı yanıt

```
HTTP/1.1 202 Accepted
X-Trace-Id: 8c1d2e3f-...

{"event_id":"...","event_instance_id":"...","status":"accepted","received_at":"..."}
```

`202` "kabul edildi ve kaybolmayacak" demektir — teslimat asenkrondur.

### Idempotency

`event_id` sizin ürettiğiniz benzersiz kimliktir (UUID v4 önerilir).
Ağ hatası aldığınızda **aynı `event_id` ile güvenle tekrar gönderin**:

- İkinci istek yeni kayıt oluşturmaz.
- Kafka'ya ikinci kez basılmaz.
- İlk isteğin yanıtı aynen döner + `X-Idempotent-Replay: true` başlığı.

### Hata kodları

| Kod | Anlam | Yapılacak |
| :--- | :--- | :--- |
| `400 invalid_envelope` | Zarf alanları eksik/hatalı | İsteği düzeltin, tekrar denemeyin |
| `400 schema_validation_failed` | Payload şemaya uymuyor | `details` alanına bakın |
| `400 unknown_event_type` | Tip tanımlı değil | Adım 4'ü tamamlayın |
| `401 invalid_api_key` | Anahtar geçersiz/iptal | Anahtarı kontrol edin |
| `401 invalid_signature` | İmza tutmuyor | Gövdeyi yeniden serileştiriyor olabilirsiniz |
| `401 stale_timestamp` | Saat kayması > 5 dk | Sisteminizi NTP ile senkronize edin |
| `403 source_tool_mismatch` | `source_tool` ≠ anahtar sahibi | `slug`'ı düzeltin |
| `413 payload_too_large` | Gövde > 1 MiB | Büyük veriyi referansla taşıyın (Claim Check) |
| `429 rate_limited` | Kota aşıldı | `Retry-After` kadar bekleyin |

---

## Adım 5B — Subscriber: webhook alma

### 1. Endpoint açın

```
POST /webhook
```

### 2. Abone olun

```bash
curl -s -X POST "$ADMIN_API/api/v1/subscriptions" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "subscriber_tool_id": "'"$TOOL_ID"'",
    "event_type_id": "<event tipi id>",
    "endpoint_url": "https://your-service.example.com/webhook",
    "max_retries": 5,
    "timeout_ms": 10000
  }' | jq
```

### 3. (Opsiyonel) Filtre ekleyin

Yalnızca belirli event'leri almak isterseniz:

```bash
curl -s -X POST "$ADMIN_API/api/v1/routing-rules" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "yalnizca-TRY-yuksek-tutar",
    "subscription_id": "<abonelik id>",
    "condition": {
      "op": "and",
      "rules": [
        { "path": "payload.currency", "op": "eq",  "value": "TRY" },
        { "path": "payload.amount",   "op": "gte", "value": 1000 }
      ]
    }
  }' | jq
```

Kural eklemezseniz **tüm** event'ler gönderilir.

> Kuralı canlıya almadan test etmek için `POST /api/v1/routing-rules/test`
> ucunu ya da Admin UI'daki "Kuralı test et" düğmesini kullanın.

### 4. İmzayı doğrulayın — ZORUNLU

Platform her webhook'u kendi anahtarıyla imzalar:

```
X-Hub-Signature-256: sha256=<hex>
X-Hub-Timestamp: <unix saniye>
```

Doğrulama sırası:
1. `X-Hub-Timestamp` ±5 dakika içinde mi? Değilse **reddet**.
2. İmzayı **ham gövde** üzerinden yeniden hesapla.
3. **Sabit süreli** karşılaştır (`crypto.timingSafeEqual` / `hmac.compare_digest`).

Tam kontrat, örnek kodlar ve kontrol listesi:
**[`api-contracts/webhook-delivery.md`](api-contracts/webhook-delivery.md)**

### 5. Doğru yanıt kodunu dönün

| Durum | Kod | Platform davranışı |
| :--- | :--- | :--- |
| İşlendi | `200` | Görev kapanır |
| Geçici hata (DB down vb.) | `503` | Backoff ile yeniden denenir |
| Kalıcı hata (bozuk veri) | `400` | Denenmez, doğrudan DLQ |
| Meşgul | `429` | Yeniden denenir |

**Hızlı yanıt verin.** Timeout varsayılan 10 saniyedir. Uzun işleri kendi
kuyruğunuza alıp hemen `200` dönün.

### 6. Subscriber tarafında idempotency

Platform **at-least-once** garantisi verir. Aynı webhook birden fazla kez
gelebilir. `X-Event-Id` ile mükerrer işlemeyi engelleyin:

```sql
INSERT INTO processed_events (event_id) VALUES ($1) ON CONFLICT DO NOTHING;
-- Satır eklenmediyse: zaten işlenmiş, atla.
```

---

## Adım 6 — Anahtar rotasyonu

```bash
curl -s -X POST "$ADMIN_API/api/v1/tools/$TOOL_ID/keys/rotate" \
  -H "Authorization: Bearer $TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{"grace_period": "24h"}' | jq
```

Zaman çizelgesi:

```
t=0      Yeni anahtar üretilir. Eski anahtar 'rotating' olur.
         ▼ HER İKİ ANAHTAR DA GEÇERLİ ─────────────────► t=24h
t=0..24h Tool'un yapılandırmasını güncelleyin (kesintisiz).
t=24h    Eski anahtar reddedilmeye başlar (401).
```

Anahtar sızdıysa grace period beklemeden **anında iptal edin**:

```bash
curl -s -X DELETE "$ADMIN_API/api/v1/tools/$TOOL_ID/keys/<key_id>" \
  -H "Authorization: Bearer $TOKEN"
```

> Not: İptal, event-ingest'in anahtar önbelleği (30 sn TTL) nedeniyle en geç
> 30 saniye içinde etkili olur.

---

## Hata ayıklama

| Araç | Adres | Ne için? |
| :--- | :--- | :--- |
| **Webhook Sink** | http://localhost:8092 | Platformun ne gönderdiğini birebir görün — headers, body, imza durumu |
| **Admin UI → Event Akışı** | http://localhost:3000/events | Event'iniz kabul edildi mi? |
| **Admin UI → Teslimatlar** | http://localhost:3000/deliveries | Kaç deneme, hangi yanıt kodu, ne kadar sürdü |
| **Admin UI → DLQ** | http://localhost:3000/dlq | Teslim edilemeyen mesajlar + replay |
| **Grafana** | http://localhost:3001 | Uçtan uca gecikme, başarı oranı |
| **Swagger UI** | http://localhost:8080/docs | API kontratı |

**İpucu:** `X-Trace-Id` başlığını kendi loglarınıza yazın. Aynı değerle
Grafana → Loki üzerinde arama yaparak event'in publisher'dan subscriber'a
tüm yolculuğunu tek ekranda görebilirsiniz.

---

## Kontrol listesi

**Publisher**
- [ ] Tool kaydedildi, `slug` sabitlendi
- [ ] API key ve HMAC secret güvenli yerde
- [ ] Event tipi ve JSON Schema tanımlandı
- [ ] Her event için benzersiz `event_id` üretiliyor (UUID v4)
- [ ] Gövde **bir kez** serileştirilip aynı byte dizisi imzalanıyor
- [ ] `202` dışındaki kodlar için hata yönetimi yazıldı
- [ ] `429` durumunda `Retry-After` bekleniyor
- [ ] Ağ hatasında aynı `event_id` ile tekrar deneniyor

**Subscriber**
- [ ] Tool kaydedildi (`kind: subscriber`)
- [ ] Webhook endpoint'i erişilebilir
- [ ] Abonelik oluşturuldu
- [ ] `X-Hub-Signature-256` **doğrulanıyor**
- [ ] `X-Hub-Timestamp` tolerans penceresi kontrol ediliyor
- [ ] Sabit süreli karşılaştırma kullanılıyor
- [ ] `X-Event-Id` ile mükerrer işleme engelleniyor
- [ ] Geçici/kalıcı hatalar doğru HTTP kodlarıyla ayrılıyor
- [ ] Uzun işler arka plana alınıyor, hızlıca `200` dönülüyor
