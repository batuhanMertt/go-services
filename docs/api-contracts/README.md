# API Kontratları (Contract-First)

Bu klasör, **kod yazılmadan önce** tanımlanmış servis sözleşmelerini içerir.
Kod bu dosyalara uyar; tersi değil.

| Dosya | İçerik |
| :--- | :--- |
| `admin-api.openapi.yaml` | Kontrol düzlemi REST API'si (OpenAPI 3.1) |
| `event-ingest.openapi.yaml` | Event kabul API'si (OpenAPI 3.1) |
| `webhook-delivery.md` | Platformun subscriber'lara gönderdiği webhook kontratı |
| `events/envelope.schema.json` | Tüm event'lerin ortak zarfı |
| `events/*.v1.json` | Event tipi bazlı payload şemaları |

## Görüntüleme

Swagger UI, admin-api ile birlikte gelir:

```
http://localhost:8080/docs
```

Ham spec: `http://localhost:8080/openapi.yaml`

---

## Neden JSON Schema? (Avro / Protobuf değil)

| Kriter | JSON Schema | Avro | Protobuf |
| :--- | :--- | :--- | :--- |
| Şema çalışma zamanında eklenebilir mi? | **Evet** (admin panelinden) | Registry gerekir | Kod üretimi gerekir |
| Yeni tool bağlanırken kod üretimi | Gerekmez | Gerekir | Gerekir |
| İnsan tarafından okunabilirlik | Yüksek | Orta | Orta |
| Mesaj boyutu | Büyük | Küçük | Küçük |
| Otomatik uyumluluk kontrolü | Yok | **Var** | Kısmi |

**Karar:** JSON Schema.

Belirleyici gerekçe, projenin temel hedefidir: *"Yeni bir tool eklemek için kod
değiştirmen gerekmemeli, sadece admin paneli ya da API kullanılmalı."*
Avro/Protobuf seçilseydi her yeni event tipi bir kod üretimi ve dağıtım
döngüsü gerektirirdi — tak-çıkart mimarisiyle doğrudan çelişirdi.

Ayrıca event'ler platforma zaten HTTP/JSON ile geliyor; ikili biçime çevirmek
gereksiz bir dönüşüm katmanı olurdu.

**Bedeli ve telafisi:** Avro'nun sağladığı otomatik şema evrimi kontrolü yok.
Bunu aşağıdaki versiyonlama disipliniyle telafi ediyoruz. Ölçek büyüdüğünde
Confluent Schema Registry eklemek bonus adım olarak planlanmıştır.

---

## Versiyonlama stratejisi

Event tipleri `<ad>@<versiyon>` ile adreslenir: `user.created@1`.
Veritabanında `(name, version)` ikilisi benzersizdir.

### Versiyon artırmayı GEREKTİRMEYEN değişiklikler (additive)

- Yeni **opsiyonel** alan eklemek
- Bir `enum`'a yeni değer eklemek *(alıcılar bilinmeyen değeri tolere ediyorsa)*
- Açıklama/örnek eklemek
- Bir alanı zorunludan opsiyonele çevirmek

Bu değişiklikler mevcut şemanın `PUT` ile güncellenmesiyle yapılır.

### Versiyon artırmayı GEREKTİREN değişiklikler (breaking)

- Alan silmek veya yeniden adlandırmak
- Alan tipini değiştirmek (`string` → `integer`)
- Yeni **zorunlu** alan eklemek
- `enum`'dan değer çıkarmak
- Kısıt daraltmak (`maxLength` küçültmek)

Bu durumda `user.created@2` olarak **yeni bir event tipi** oluşturulur.

### Geçiş süreci

1. `user.created@2` tanımlanır.
2. Subscriber'lar v2 için ayrı abonelik açar; v1 aboneliği çalışmaya devam eder.
3. Publisher, hazır olduğunda `event_version: 2` göndermeye başlar.
4. Tüm publisher'lar geçtikten sonra v1 event tipi `is_active: false` yapılır.
5. Retention süresi dolduğunda v1 tamamen kaldırılır.

**Hiçbir aşamada kesinti olmaz** — iki versiyon bir süre yan yana yaşar.

---

## Kontrat değişikliği yaparken

1. Önce bu klasördeki dosyayı güncelle.
2. Değişikliği `docs/` altında gerekçelendir.
3. Kodu güncelle.
4. Testleri güncelle.

Kod ile spec arasında fark oluştuğunda **spec doğru kabul edilir.**
