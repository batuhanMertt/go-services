# Outbound Webhook Kontratı

Platformun subscriber tool'lara gönderdiği HTTP webhook'unun tam tanımı.
Bu doküman, bir dış geliştiricinin **hiçbir şey sormadan** subscriber
yazabilmesi için yeterli olacak şekilde yazılmıştır.

---

## 1. İstek

```
POST <subscription.endpoint_url>
Content-Type: application/json
```

### 1.1 Başlıklar

| Başlık | Örnek | Açıklama |
| :--- | :--- | :--- |
| `X-Hub-Signature-256` | `sha256=9f86d081884c7d65…` | HMAC-SHA256 imzası. **Doğrulanması zorunludur.** |
| `X-Hub-Timestamp` | `1735689600` | İmzaya dahil edilen Unix saniye. Replay penceresi kontrolü için. |
| `X-Event-Id` | `3f9a1c72-5b0e-…` | Publisher'ın ürettiği event kimliği. Subscriber tarafında idempotency anahtarı olarak kullanılmalıdır. |
| `X-Event-Type` | `user.created` | Event tipi. |
| `X-Delivery-Id` | `b7c1e2d3-…` | Bu teslimat görevinin kimliği. Aynı event farklı subscriber'lara farklı `X-Delivery-Id` ile gider. |
| `X-Delivery-Attempt` | `3` | Kaçıncı deneme (1'den başlar). |
| `X-Delivery-Max-Attempts` | `5` | Toplam deneme bütçesi. |
| `X-Trace-Id` | `8c1d2e3f-…` | Uçtan uca izleme kimliği. Kendi loglarınıza yazmanız hata ayıklamayı çok kolaylaştırır. |
| `User-Agent` | `MindMons-IntegrationHub/1.0` | Sabit. |

> Aboneliğe tanımlanan ek başlıklar (`subscription.http_headers`) da gönderilir,
> ancak yukarıdaki platform başlıklarının **üzerine yazamaz**.

### 1.2 Gövde

```json
{
  "event_id": "3f9a1c72-5b0e-4a1d-9f6c-7c2e8d5b1a34",
  "event_type": "user.created",
  "event_version": 1,
  "source_tool": "mock-publisher-1",
  "timestamp": "2026-08-31T10:15:00Z",
  "trace_id": "8c1d2e3f-4a5b-6c7d-8e9f-0a1b2c3d4e5f",
  "payload": {
    "user_id": "usr_9a8b7c6d",
    "email": "ada@mindmons.local",
    "status": "active"
  },
  "metadata": { "emitted_by": "mock-publisher-1" },
  "delivery": {
    "delivery_id": "b7c1e2d3-4f5a-6b7c-8d9e-0f1a2b3c4d5e",
    "subscription_id": "c8d2f3e4-…",
    "attempt": 3,
    "max_retries": 5,
    "sent_at": "2026-08-31T10:15:07Z",
    "replay": false
  }
}
```

`delivery` bloğu başlıklardaki bilgiyi gövdede de sunar; bazı istemciler
(webhook alan serverless fonksiyonlar gibi) başlıklara kolay erişemez.

`delivery.replay = true`, mesajın DLQ'dan manuel olarak yeniden gönderildiğini
belirtir — subscriber isterse bunu ayrı işleyebilir.

---

## 2. İmza doğrulama (zorunlu)

İmzalanan metin, gövdenin **ham** hâliyle üretilir:

```
imzalanan = "<X-Hub-Timestamp>" + "." + "<ham istek gövdesi>"
imza      = "sha256=" + hex(HMAC_SHA256(WEBHOOK_SIGNING_KEY, imzalanan))
```

### 2.1 Doğru doğrulama sırası

1. `X-Hub-Timestamp` şimdiye göre ±5 dakika içinde mi? Değilse **reddet**
   (yakalanan bir isteğin sonsuza dek yeniden oynatılmasını engeller).
2. İmzayı ham gövde üzerinden yeniden hesapla.
3. **Sabit süreli** karşılaştırma yap.

### 2.2 Kritik uyarılar

- **Gövdeyi parse edip yeniden serileştirmeyin.** JSON alan sırası veya boşluk
  farkı imzayı bozar. Ham byte dizisi üzerinden imzalayın.
- **`==` ile karşılaştırmayın.** Sıradan string karşılaştırması ilk farklı
  byte'ta döner; saldırgan yanıt süresini ölçerek imzayı byte byte tahmin
  edebilir (timing attack). Sabit süreli karşılaştırma kullanın:
  Go `subtle.ConstantTimeCompare`, Node.js `crypto.timingSafeEqual`,
  Python `hmac.compare_digest`.

### 2.3 Örnek — Go

```go
mac := hmac.New(sha256.New, []byte(signingKey))
mac.Write([]byte(timestamp + "." + string(rawBody)))
expected := "sha256=" + hex.EncodeToString(mac.Sum(nil))

if subtle.ConstantTimeCompare([]byte(expected), []byte(received)) != 1 {
    http.Error(w, "invalid signature", http.StatusUnauthorized)
    return
}
```

### 2.4 Örnek — Node.js

```js
const expected = 'sha256=' + crypto
  .createHmac('sha256', signingKey)
  .update(`${timestamp}.${rawBody}`)
  .digest('hex');

const ok = crypto.timingSafeEqual(Buffer.from(expected), Buffer.from(received));
```

> Express kullanıyorsanız `express.json({ verify: (req, res, buf) => { req.rawBody = buf } })`
> ile ham gövdeyi saklamayı unutmayın.

---

## 3. Yanıt sözleşmesi

| Durum kodu | Platform davranışı |
| :--- | :--- |
| `2xx` | Başarılı. Görev kapatılır. |
| `5xx` | Geçici hata → exponential backoff ile yeniden denenir. |
| `429` | Hız limiti → yeniden denenir. `Retry-After` şu an dikkate alınmaz, platform kendi backoff'unu uygular. |
| `408`, `425` | Geçici → yeniden denenir. |
| Diğer `4xx` | **Kalıcı hata.** Yeniden denenmez, doğrudan DLQ'ya gider. |
| Zaman aşımı / bağlantı hatası | Geçici → yeniden denenir. |

Yanıt gövdesinin ilk 512 baytı `delivery_attempts` tablosuna kaydedilir ve
admin panelinde görünür — hata mesajı yazarsanız hata ayıklamak kolaylaşır.

### 3.1 Yönlendirme takip edilmez

Platform `3xx` yanıtlarını **takip etmez** (`CheckRedirect` kapalıdır).
Bir webhook'un başka bir adrese yönlendirilmesi SSRF vektörüdür.

---

## 4. Retry politikası

| Deneme | Bekleme (yaklaşık) |
| :--- | :--- |
| 1 → 2 | 1 sn |
| 2 → 3 | 2 sn |
| 3 → 4 | 4 sn |
| 4 → 5 | 8 sn |
| 5 → 6 | 16 sn |

Her gecikmeye ±%20 rastgele **jitter** eklenir; aynı anda başarısız olan
yüzlerce teslimatın aynı saniyede geri dönüp hedefi tekrar çökertmesi
("thundering herd") böylece önlenir.

Varsayılan `max_retries = 5` aşıldığında mesaj DLQ'ya alınır ve admin
panelinden manuel replay edilebilir.

### 4.1 Circuit breaker

Bir subscriber üst üste 5 kez hata verirse, o subscriber'a giden teslimatlar
30 saniye boyunca **ağa çıkmadan** ertelenir. Süre sonunda tek bir deneme
gönderilir; başarılıysa devre kapanır. Bu, çökmüş bir hedefin tüm worker
havuzunu işgal etmesini engeller.

---

## 5. Subscriber tarafında idempotency

Platform **at-least-once** teslimat garantisi verir: bir webhook, ağ hatası
veya yeniden dengeleme (rebalance) durumunda birden fazla kez ulaşabilir.

Bu nedenle subscriber'ın `X-Event-Id` (veya `event_id`) değerini saklayıp
daha önce işlenmiş event'leri **atlaması gerekir**:

```sql
INSERT INTO processed_events (event_id) VALUES ($1) ON CONFLICT DO NOTHING;
-- Satır eklenmediyse: bu event zaten işlenmiş, işlemi tekrarlama.
```

Exactly-once teslimat dağıtık sistemlerde pratikte mümkün değildir;
"at-least-once + idempotent alıcı" endüstri standardı çözümdür.

---

## 6. Hızlı kontrol listesi

- [ ] Ham gövdeyi imza doğrulaması için sakladım.
- [ ] `X-Hub-Timestamp` tolerans penceresini kontrol ediyorum.
- [ ] Sabit süreli karşılaştırma kullanıyorum.
- [ ] `event_id` ile mükerrer işlemeyi engelliyorum.
- [ ] Geçici hatalarda `5xx`, kalıcı hatalarda `4xx` dönüyorum.
- [ ] Uzun işleri arka plana atıp hızlıca `200` dönüyorum (10 sn timeout var).
- [ ] `X-Trace-Id` değerini kendi loglarıma yazıyorum.
