# 08 — Tehdit Modeli

Bu doküman, platformun **hangi tehditlere karşı korunduğunu** ve — en az onun
kadar önemlisi — **hangilerine karşı korunmadığını** açıkça listeler.

Yöntem: STRIDE.

---

## 1. Sistem sınırları ve güven bölgeleri

```
┌─ GÜVENİLMEYEN ────────────────────────────────────────────────┐
│  İnternet: publisher tool'lar, admin tarayıcısı               │
└───────────────────────┬───────────────────────────────────────┘
                        │  ← GÜVEN SINIRI 1
┌─ YARI GÜVENİLİR ──────▼───────────────────────────────────────┐
│  event-ingest (API key + HMAC)                                │
│  admin-api    (JWT)                                           │
└───────────────────────┬───────────────────────────────────────┘
                        │  ← GÜVEN SINIRI 2
┌─ GÜVENİLİR (iç ağ) ───▼───────────────────────────────────────┐
│  Kafka · PostgreSQL · Redis                                   │
│  routing-engine · delivery-worker · dlq-handler               │
└───────────────────────┬───────────────────────────────────────┘
                        │  ← GÜVEN SINIRI 3 (giden trafik)
┌─ GÜVENİLMEYEN ────────▼───────────────────────────────────────┐
│  Subscriber webhook uç noktaları                              │
└───────────────────────────────────────────────────────────────┘
```

---

## 2. STRIDE analizi

### S — Spoofing (kimlik sahteciliği)

| Tehdit | Korunma | Kod |
| :--- | :--- | :--- |
| Saldırgan başka bir tool adına event yayınlar | API key doğrulaması + `source_tool` ile anahtar sahibinin **eşleşme kontrolü** | `services/event-ingest/handler.go` |
| Saldırgan sahte webhook'la subscriber'ı kandırır | Platform her webhook'u HMAC ile imzalar; subscriber doğrular | `internal/cryptox/hmac.go` |
| Çalınan admin token'ı ile giriş | Access token 15 dk; refresh token **tek kullanımlık** (rotation) | `internal/auth/jwt.go` |
| `alg: none` JWT saldırısı | İmza algoritması `WithValidMethods([HS256])` ile sabitlenmiş | `internal/auth/jwt.go` |

**`source_tool` kontrolü neden kritik?** Bu olmadan, herhangi bir geçerli
anahtara sahip tool başka bir tool adına event yayınlayabilir ve ekosistemdeki
güven zincirini kırabilirdi.

### T — Tampering (veri değiştirme)

| Tehdit | Korunma |
| :--- | :--- |
| Aktarım sırasında event gövdesi değiştirilir | HMAC-SHA256 imzası gövdenin tamamını kapsar |
| Webhook gövdesi değiştirilir | `X-Hub-Signature-256` |
| SQL injection | **Her sorgu** pgx parametreli sorgu kullanır; string birleştirme ile SQL kurulan tek satır yoktur |
| Kontrat dışı alan enjeksiyonu | `DisallowUnknownFields` — bilinmeyen JSON alanı 400 döner |
| DB yedeği çalınırsa HMAC secret'ları okunur | Secret'lar AES-256-GCM ile şifreli saklanır; anahtar runtime env'de |

### R — Repudiation (inkâr)

| Tehdit | Korunma |
| :--- | :--- |
| Admin "ben silmedim" der | `audit_logs`: aktör, işlem, önce/sonra durumu, IP, user-agent, trace_id |
| Tool "ben göndermedim" der | `event_instances`: `source_tool_id` + imza doğrulanmış kayıt |
| Veri sessizce silinir | Retention purge işlemi de `audit_logs`'a yazılır |

`audit_logs` **hiçbir zaman otomatik silinmez** — denetlenebilirliğin temeli.

### I — Information Disclosure (bilgi ifşası)

| Tehdit | Korunma |
| :--- | :--- |
| Kullanıcı numaralandırma (user enumeration) | Login'de "kullanıcı yok" ve "şifre yanlış" **aynı** yanıt; ayrıca sahte argon2 doğrulamasıyla zamanlama farkı kapatılır |
| Timing attack ile imza tahmini | `subtle.ConstantTimeCompare` (NFR-3.1) |
| API key DB'den okunur | Yalnızca SHA-256 hash saklanır |
| İç hata detayı istemciye sızar | 5xx yanıtlarında generic mesaj; detay yalnızca loglanır |
| İmza logda görünür | `redactHeaders` ile maskelenir |
| Secret `docker inspect` ile görünür | Build arg değil, **runtime env** kullanılır |
| Tarayıcıda XSS ile token çalınır | Access token yalnızca **bellekte**; localStorage'da değil. CSP başlıkları aktif. |

### D — Denial of Service

| Tehdit | Korunma |
| :--- | :--- |
| Event seli | Redis token bucket: IP + tool bazlı (NFR-3.2) |
| Login brute-force | Admin uçlarında IP + kullanıcı bazlı rate limit |
| Dev payload ile bellek tüketimi | `INGEST_MAX_BODY_BYTES` (1 MiB) + `http.MaxBytesReader` |
| Yavaş subscriber tüm worker'ları tıkar | **Bulkhead** (subscriber başına slot) + **circuit breaker** |
| Yanıt vermeyen subscriber goroutine tutar | HTTP client timeout + `MaxConnsPerHost` |
| Uzun sorgu DB havuzunu tıkar | `statement_timeout=30s` |
| Log/disk şişmesi | Log rotation (10 MB × 3), retention job, Prometheus/Loki retention |
| Container kaynak tüketimi | Her serviste CPU + bellek limiti |
| Slowloris | `ReadHeaderTimeout: 5s` |

### E — Elevation of Privilege

| Tehdit | Korunma |
| :--- | :--- |
| Bir servis başka servisin tablosuna yazar | **Servis başına Postgres rolü** + tablo bazlı GRANT |
| Container escape sonrası root erişimi | Tüm container'lar **non-root** (distroless nonroot / nginx-unprivileged) |
| Publisher, subscriber yetkisi kullanır | `kind` kontrolü: publisher olmayan tool event yayınlayamaz |
| Yatay yetki yükseltme | JWT `role` claim'i + `requireRole` middleware'i (genişlemeye hazır) |

**Least privilege doğrulaması:**

```sql
-- event-ingest, teslimat loglarına yazamaz:
SET ROLE svc_event_ingest;
INSERT INTO hub.delivery_attempts (...) VALUES (...);
-- ERROR: permission denied for table delivery_attempts
```

---

## 3. Korunan tehditlerin özeti

| # | Tehdit | Mekanizma |
| :-- | :--- | :--- |
| 1 | Kimlik sahteciliği | API key + HMAC + source_tool eşleşmesi |
| 2 | Veri değiştirme | HMAC-SHA256 (gövde + zaman damgası) |
| 3 | Replay saldırısı | Zaman damgası imzaya dahil, ±5 dk pencere |
| 4 | Timing attack | Sabit süreli karşılaştırma |
| 5 | Şifre kırma | argon2id (64 MiB, t=3, p=2) |
| 6 | Token çalma | Kısa access TTL + tek kullanımlık refresh |
| 7 | SQL injection | Parametreli sorgular (istisnasız) |
| 8 | DoS / abuse | Çok katmanlı rate limit |
| 9 | Kaynak tükenmesi | Bulkhead, breaker, timeout, container limitleri |
| 10 | Yetki yükseltme | Servis başına DB rolü, non-root container |
| 11 | Bilgi ifşası | Hash/şifreleme, maskeleme, generic hatalar |
| 12 | İnkâr | Kapsamlı denetim kaydı |
| 13 | XSS | CSP, `X-Content-Type-Options`, token bellekte |
| 14 | Açık yönlendirme / SSRF (kısmi) | Webhook'larda `3xx` **takip edilmez** |

---

## 4. KORUNMAYAN tehditler (kabul edilen riskler)

Bu bölüm bilinçli olarak açık yazılmıştır. Bir tehdit modelinin değeri,
kapsamadıklarını da söylemesindedir.

### 4.1 Aktarım şifrelemesi (TLS) yok

**Durum:** Servisler arası ve dışa açık trafik **düz HTTP**.

**Risk:** Ağı dinleyen biri API anahtarlarını ve payload'ları okuyabilir.

**Neden kabul edildi:** Bu bir yerel geliştirme/değerlendirme ortamıdır; tüm
trafik tek bir Docker bridge network'ünde kalır.

**Üretimde ne yapılmalı:**
- Dış trafik için TLS sonlandıran reverse proxy (nginx/Traefik) + Let's Encrypt.
- Servisler arası mTLS (service mesh: Linkerd/Istio) veya en azından TLS.
- Kafka: `SASL_SSL` + ACL.
- Postgres: `sslmode=require`.
- HSTS başlığı.

### 4.2 SSRF koruması yok

**Durum:** Admin, webhook URL'i olarak **herhangi bir adres** girebilir —
`http://169.254.169.254/` (bulut metadata servisi) dahil.

**Risk:** Yetkili bir admin, platformu iç ağa istek atmaya zorlayabilir.

**Neden kabul edildi:** Mock subscriber'lara teslimat için Docker servis
adlarına (`http://mock-subscriber:8091`) erişim **zorunludur**. Özel IP
aralıklarını engellemek bu ortamı çalışmaz hâle getirirdi. Ayrıca webhook
URL'i yalnızca kimliği doğrulanmış admin tarafından girilebilir.

**Üretimde ne yapılmalı:** `validateWebhookURL` içinde DNS çözümlemesi sonrası
IP kontrolü — `10/8`, `172.16/12`, `192.168/16`, `127/8`, `169.254/16`,
`::1`, `fc00::/7` reddedilmeli. DNS rebinding'e karşı çözümlenen IP'ye bağlanılmalı.

### 4.3 Multi-tenancy izolasyonu yok

**Durum:** Tüm tool'lar tek bir mantıksal alanı paylaşır. Admin herkesi görür.

**Neden kabul edildi:** Görev tanımında multi-tenancy bonus adım olarak
listelenmiştir.

**Üretimde:** `organization_id` her tabloya, row-level security (RLS)
politikaları, JWT'de tenant claim'i.

### 4.4 Secret rotasyonu otomatik değil

**Durum:** `JWT_SECRET`, `SECRET_ENCRYPTION_KEY`, `WEBHOOK_SIGNING_KEY` elle
yönetilir. `SECRET_ENCRYPTION_KEY` değişirse **mevcut HMAC secret'ları
çözülemez hâle gelir.**

**Üretimde:** Vault/KMS ile zarf şifreleme (envelope encryption), anahtar
versiyonlama, kademeli geçiş (`key_version` sütunu).

### 4.5 İçeriden tehdit (insider threat)

**Durum:** DB'ye doğrudan erişimi olan biri her şeyi okuyabilir/değiştirebilir.
Postgres superuser audit log'u silebilir.

**Üretimde:** Ayrı audit veritabanı veya append-only depolama, DB erişim
denetimi, PII için sütun bazlı şifreleme.

### 4.6 DDoS (ağ katmanı)

**Durum:** Uygulama seviyesi rate limit var; L3/L4 hacimsel saldırıya karşı
koruma yok.

**Üretimde:** CDN/WAF (Cloudflare), upstream DDoS koruması.

### 4.7 Tedarik zinciri saldırısı

**Durum:** `govulncheck` ve Trivy CI'da çalışır ama bağımlılıklar imzalı
doğrulanmaz.

**Üretimde:** SBOM üretimi, Sigstore/cosign ile image imzalama, admission
controller ile imza doğrulama.

### 4.8 Payload içerik denetimi yok

**Durum:** JSON Schema yapısal doğrulama yapar; içerikte PII veya kötü amaçlı
veri olup olmadığına bakmaz.

**Üretimde:** PII tespiti/maskeleme, alan bazlı şifreleme, veri sınıflandırma.

---

## 5. Güvenlik kontrol listesi

| Kontrol | Durum | Nerede |
| :--- | :--- | :--- |
| Secret'lar `.env`'de, `.gitignore` içinde | ✅ | `.gitignore` |
| Şifre hashing argon2id (OWASP parametreleri) | ✅ | `internal/auth/password.go` |
| JWT kısa access + rotate edilen refresh | ✅ | `internal/auth/jwt.go` |
| HMAC sabit süreli karşılaştırma | ✅ | `internal/cryptox/hmac.go` |
| Replay penceresi (timestamp imzada) | ✅ | `internal/cryptox/hmac.go` |
| Rate limit: IP + tool + admin user | ✅ | `internal/ratelimit/` |
| CORS whitelist (`*` yok) | ✅ | `internal/httpx/middleware.go` |
| SQL: yalnızca parametreli sorgu | ✅ | tüm `store*.go` |
| Input validation + boyut limiti | ✅ | `httpx.MaxBody`, `Envelope.Validate` |
| Non-root container | ✅ | `build/Dockerfile` (distroless nonroot) |
| Servis başına DB rolü | ✅ | `migrations/0003_grants.up.sql` |
| Build arg ile secret geçilmiyor | ✅ | `build/Dockerfile` |
| Güvenlik başlıkları + CSP | ✅ | `httpx.SecurityHeaders`, `admin-ui/nginx.conf` |
| Webhook'larda redirect takip edilmiyor | ✅ | `delivery-worker/main.go` |
| Redis yıkıcı komutları kapalı | ✅ | `infra/redis/redis.conf` |
| Bağımlılık taraması (`govulncheck`) | ✅ | `.github/workflows/ci.yml` |
| Image taraması (Trivy) | ✅ | `.github/workflows/ci.yml` |
| CSRF token | ➖ | Gerekmiyor: Bearer token, cookie yok |
| TLS | ❌ | Kabul edilen risk — §4.1 |
| SSRF koruması | ❌ | Kabul edilen risk — §4.2 |

---

## 6. Olay müdahale (incident response)

| Olay | İlk adım |
| :--- | :--- |
| API anahtarı sızdı | Admin UI → Tools → ilgili anahtarı **iptal et** (etki ≤30 sn) |
| Admin şifresi sızdı | Settings → şifre değiştir (tüm oturumlar iptal olur) |
| `JWT_SECRET` sızdı | `.env`'de değiştir → admin-api restart (tüm oturumlar geçersizleşir) |
| `WEBHOOK_SIGNING_KEY` sızdı | Değiştir + tüm subscriber'ları bilgilendir (koordinasyon gerekir) |
| `SECRET_ENCRYPTION_KEY` sızdı | ⚠️ Değiştirmeden önce tüm HMAC secret'ları yeniden şifrelenmeli; aksi halde tüm tool'lar erişimini kaybeder |
| Bir tool kötüye kullanıyor | Tool `status`'ünü `suspended` yap (anında etkili) |
| DLQ patladı | `docs/10-runbook.md` → "DLQ birikmesi" |

Her olaydan sonra `audit_logs` incelenmeli: saldırgan hangi işlemleri yaptı?
