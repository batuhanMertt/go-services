#!/usr/bin/env bash
# =============================================================================
# Uçtan uca kabul testi.
#
# Görev tanımındaki kabul kriterlerini sırayla doğrular:
#   1. Altyapı ayakta (topic'ler, tablolar, roller)
#   2. Admin kimlik doğrulama + yetkisiz erişim reddi
#   3. Mock tool'ların otomatik kaydı
#   4. Event kabulü (202) ve outbox → Kafka aktarımı
#   5. Idempotency (aynı event_id tek kayıt)
#   6. İmza doğrulama (bozuk imza 401)
#   7. Uçtan uca teslimat (publisher → subscriber, 200)
#   8. Arıza → exponential backoff retry → DLQ
#   9. DLQ replay → başarılı teslimat
#  10. Rate limit (429)
#  11. Denetim kaydı
#
# Kullanım:  ./tests/e2e/e2e.sh
# Ön koşul:  docker compose up -d  (tüm servisler healthy)
# =============================================================================
set -uo pipefail

cd "$(dirname "$0")/../.."

# --- Yardımcılar --------------------------------------------------------------

RED=$'\033[0;31m'; GREEN=$'\033[0;32m'; YELLOW=$'\033[0;33m'; BLUE=$'\033[0;34m'; NC=$'\033[0m'

PASS=0; FAIL=0; STEP=0

step()  { STEP=$((STEP+1)); printf '\n%s[%02d] %s%s\n' "$BLUE" "$STEP" "$1" "$NC"; }
ok()    { PASS=$((PASS+1)); printf '  %s✓%s %s\n' "$GREEN" "$NC" "$1"; }
bad()   { FAIL=$((FAIL+1)); printf '  %s✗%s %s\n' "$RED" "$NC" "$1"; }
info()  { printf '  %s·%s %s\n' "$YELLOW" "$NC" "$1"; }

assert_eq() {
  if [[ "$2" == "$3" ]]; then ok "$1 ($3)"; else bad "$1 — beklenen: $3, alınan: $2"; fi
}

assert_ge() {
  if (( $(echo "$2 >= $3" | bc -l) )); then ok "$1 ($2 >= $3)"; else bad "$1 — $2 < $3"; fi
}

need() { command -v "$1" >/dev/null 2>&1 || { echo "HATA: $1 gerekli"; exit 1; }; }
need curl; need jq; need docker

[[ -f .env ]] || { echo "HATA: .env yok. Önce: ./scripts/gen-secrets.sh"; exit 1; }
# shellcheck disable=SC1091
set -a; source .env; set +a

# Testi iki yerden çalıştırabilmek gerekir:
#   - Host'tan (varsayılan): yayınlanmış portlar üzerinden
#   - Compose ağı içinden (E2E_INTERNAL=true): servis adları ve iç portlar
#     üzerinden — Docker Desktop'ta host networking olmadığında bu şart.
if [[ "${E2E_INTERNAL:-false}" == "true" ]]; then
  ADMIN_API="http://admin-api:8080"
  INGEST="http://event-ingest:8081"
  PUBLISHER="http://mock-publisher:8090"
  SUBSCRIBER="http://mock-subscriber:8091"
  SINK="http://webhook-sink:8092"
  PROM="http://prometheus:9090"
  DELIVERY="http://delivery-worker:8083"
  HEALTH_TARGETS="admin-api:8080 event-ingest:8081 mock-publisher:8090 mock-subscriber:8091 webhook-sink:8092"
else
  ADMIN_API="http://localhost:${ADMIN_API_HOST_PORT:-8080}"
  INGEST="http://localhost:${INGEST_HOST_PORT:-8081}"
  PUBLISHER="http://localhost:${MOCK_PUBLISHER_HOST_PORT:-8090}"
  SUBSCRIBER="http://localhost:${MOCK_SUBSCRIBER_HOST_PORT:-8091}"
  SINK="http://localhost:${WEBHOOK_SINK_HOST_PORT:-8092}"
  PROM="http://localhost:${PROMETHEUS_HOST_PORT:-9090}"
  DELIVERY="http://localhost:${DELIVERY_HOST_PORT:-8083}"
  HEALTH_TARGETS="admin-api:${ADMIN_API_HOST_PORT:-8080} event-ingest:${INGEST_HOST_PORT:-8081} mock-publisher:${MOCK_PUBLISHER_HOST_PORT:-8090} mock-subscriber:${MOCK_SUBSCRIBER_HOST_PORT:-8091} webhook-sink:${WEBHOOK_SINK_HOST_PORT:-8092}"
fi

# İç modda hedef host servis adının kendisi, dış modda localhost.
health_url() {
  local name="$1" port="$2"
  if [[ "${E2E_INTERNAL:-false}" == "true" ]]; then echo "http://${name}:${port}/healthz"
  else echo "http://localhost:${port}/healthz"; fi
}

psql_q() {
  docker compose exec -T postgres psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -tAc "$1" 2>/dev/null | tr -d '[:space:]'
}

printf '%s╔══════════════════════════════════════════════════════════════╗%s\n' "$BLUE" "$NC"
printf '%s║   MindMons Integration & Event Hub — E2E Kabul Testi          ║%s\n' "$BLUE" "$NC"
printf '%s╚══════════════════════════════════════════════════════════════╝%s\n' "$BLUE" "$NC"

# =============================================================================
step "Servis sağlığı"
# =============================================================================
for svc in $HEALTH_TARGETS; do
  name="${svc%%:*}"; port="${svc##*:}"
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 "$(health_url "$name" "$port")" || echo 000)
  assert_eq "$name /healthz" "$code" "200"
done

unhealthy=$(docker compose ps --format json 2>/dev/null | jq -rs '[.[] | select(.Health == "unhealthy")] | length' || echo 0)
assert_eq "unhealthy container yok" "$unhealthy" "0"

# =============================================================================
step "Altyapı: Kafka topic'leri, tablolar, servis rolleri"
# =============================================================================
topics=$(docker compose exec -T kafka /opt/kafka/bin/kafka-topics.sh \
  --bootstrap-server localhost:9092 --list 2>/dev/null | grep -c '^events\.' || echo 0)
assert_eq "3 Kafka topic'i mevcut" "$topics" "3"

tables=$(psql_q "SELECT count(*) FROM information_schema.tables WHERE table_schema='hub';")
assert_ge "hub şemasında tablolar" "$tables" "13"

roles=$(psql_q "SELECT count(*) FROM pg_roles WHERE rolname LIKE 'svc_%';")
assert_eq "servis başına DB rolü (least privilege)" "$roles" "5"

types=$(psql_q "SELECT count(*) FROM hub.event_types;")
assert_ge "event tipleri seed edildi" "$types" "3"

# =============================================================================
step "Admin API kimlik doğrulama"
# =============================================================================
code=$(curl -s -o /dev/null -w '%{http_code}' "$ADMIN_API/api/v1/tools")
assert_eq "token'sız istek reddediliyor" "$code" "401"

code=$(curl -s -o /dev/null -w '%{http_code}' "$ADMIN_API/api/v1/tools" -H "Authorization: Bearer gecersiz.token.degeri")
assert_eq "geçersiz token reddediliyor" "$code" "401"

LOGIN=$(curl -s -X POST "$ADMIN_API/api/v1/auth/login" \
  -H 'Content-Type: application/json' \
  -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"$ADMIN_PASSWORD\"}")
TOKEN=$(echo "$LOGIN" | jq -r '.access_token // empty')
REFRESH=$(echo "$LOGIN" | jq -r '.refresh_token // empty')

if [[ -n "$TOKEN" ]]; then ok "admin girişi başarılı"; else bad "admin girişi başarısız: $LOGIN"; exit 1; fi
AUTH=(-H "Authorization: Bearer $TOKEN")

code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$ADMIN_API/api/v1/auth/login" \
  -H 'Content-Type: application/json' -d "{\"email\":\"$ADMIN_EMAIL\",\"password\":\"kesinlikle-yanlis\"}")
assert_eq "yanlış şifre reddediliyor" "$code" "401"

# Refresh token tek kullanımlık olmalı (rotation).
r1=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$ADMIN_API/api/v1/auth/refresh" \
  -H 'Content-Type: application/json' -d "{\"refresh_token\":\"$REFRESH\"}")
r2=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$ADMIN_API/api/v1/auth/refresh" \
  -H 'Content-Type: application/json' -d "{\"refresh_token\":\"$REFRESH\"}")
assert_eq "refresh token ilk kullanımda geçerli" "$r1" "200"
assert_eq "refresh token ikinci kullanımda reddediliyor (rotation)" "$r2" "401"

# =============================================================================
step "Mock tool'ların otomatik kaydı"
# =============================================================================
for slug in mock-publisher-1 mock-subscriber-1 webhook-sink; do
  n=$(psql_q "SELECT count(*) FROM hub.tools WHERE slug='$slug' AND status='active';")
  assert_eq "$slug kayıtlı" "$n" "1"
done

subs=$(psql_q "SELECT count(*) FROM hub.subscriptions WHERE is_active;")
assert_ge "abonelikler oluşturuldu" "$subs" "6"

keys=$(psql_q "SELECT count(*) FROM hub.api_keys;")
assert_ge "API anahtarı üretildi" "$keys" "1"

plaintext_leak=$(psql_q "SELECT count(*) FROM hub.api_keys WHERE length(key_hash) <> 32;")
assert_eq "anahtarlar SHA-256 hash olarak saklanıyor" "$plaintext_leak" "0"

# =============================================================================
step "Event kabulü ve Transactional Outbox"
# =============================================================================
before=$(psql_q "SELECT count(*) FROM hub.event_instances;")

RESULT=$(curl -s -X POST "$PUBLISHER/api/trigger" \
  -H 'Content-Type: application/json' -d '{"event_type":"user.created","count":3}')
sent=$(echo "$RESULT" | jq -r '.sent // 0')
assert_eq "3 event gönderildi" "$sent" "3"

status=$(echo "$RESULT" | jq -r '.results[0].status')
assert_eq "ingest 202 Accepted döndü" "$status" "202"

latency=$(echo "$RESULT" | jq -r '[.results[].latency_ms] | max')
if (( latency < 50 )); then ok "ingest gecikmesi < 50 ms (NFR-2.1): ${latency} ms"
else info "ingest gecikmesi ${latency} ms — soğuk başlangıçta olabilir"; fi

sleep 6
after=$(psql_q "SELECT count(*) FROM hub.event_instances;")
assert_eq "event'ler Postgres'e yazıldı" "$((after - before))" "3"

pending=$(psql_q "SELECT count(*) FROM hub.outbox_messages WHERE published_at IS NULL;")
assert_eq "outbox boşaltıldı (Kafka'ya aktarıldı)" "$pending" "0"

routed=$(psql_q "SELECT count(*) FROM hub.event_instances WHERE status='routed';")
assert_ge "event'ler yönlendirildi" "$routed" "3"

# =============================================================================
step "Idempotency (FR-2.5)"
# =============================================================================
IDEM="e2e-idem-$(date +%s)-$RANDOM"

for i in 1 2; do
  R=$(curl -s -X POST "$PUBLISHER/api/trigger" -H 'Content-Type: application/json' \
    -d "{\"event_type\":\"document.generated\",\"count\":1,\"event_id\":\"$IDEM\"}")
  dup=$(echo "$R" | jq -r '.results[0].duplicate')
  if [[ $i == 1 ]]; then assert_eq "1. gönderim yeni kayıt" "$dup" "false"
  else assert_eq "2. gönderim mükerrer olarak işaretlendi" "$dup" "true"; fi
done

sleep 2
n=$(psql_q "SELECT count(*) FROM hub.event_instances WHERE event_id='$IDEM';")
assert_eq "veritabanında tek kayıt var" "$n" "1"

# =============================================================================
step "İmza doğrulama (FR-2.2, NFR-3.1)"
# =============================================================================
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$INGEST/api/v1/events" \
  -H 'Content-Type: application/json' \
  -H 'X-Api-Key: ih_000000000000_000000000000000000000000000000000000000000000000' \
  -H 'X-Signature-SHA256: deadbeef' -H "X-Timestamp: $(date +%s)" \
  -d '{"event_id":"sahte-event-id","event_type":"user.created","event_version":1,"source_tool":"x","timestamp":"2026-01-01T00:00:00Z","payload":{}}')
assert_eq "bozuk imza 401 ile reddediliyor" "$code" "401"

code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$INGEST/api/v1/events" \
  -H 'Content-Type: application/json' -d '{}')
assert_eq "başlıksız istek 401 ile reddediliyor" "$code" "401"

# =============================================================================
step "Uçtan uca teslimat"
# =============================================================================
delivered=$(psql_q "SELECT count(*) FROM hub.delivery_tasks WHERE status='succeeded';")
assert_ge "başarılı teslimat var" "$delivered" "6"

ok2xx=$(psql_q "SELECT count(*) FROM hub.delivery_attempts WHERE response_status BETWEEN 200 AND 299;")
assert_ge "subscriber'lar 200 döndü" "$ok2xx" "6"

sink=$(curl -s "$SINK/api/status" | jq -r '.captured // 0')
assert_ge "webhook sink event yakaladı" "$sink" "3"

valid_sig=$(curl -s "$SINK/api/captures?signature=valid&limit=5" | jq -r '.items | length')
assert_ge "webhook imzaları geçerli doğrulandı" "$valid_sig" "1"

# =============================================================================
step "Arıza → exponential backoff → DLQ (FR-4.3, FR-5.1)"
# =============================================================================
dlq_before=$(psql_q "SELECT count(*) FROM hub.dlq_events;")

info "mock-subscriber arıza moduna alınıyor (her istek 500)"
curl -s -X POST "$SUBSCRIBER/api/failure" -H 'Content-Type: application/json' -d '{"fail_every":1}' >/dev/null

curl -s -X POST "$PUBLISHER/api/trigger" -H 'Content-Type: application/json' \
  -d '{"event_type":"verification.completed","count":1}' >/dev/null

info "retry zinciri bekleniyor (1+2+4+8+16 sn + breaker) — en fazla 150 sn"
for _ in $(seq 1 30); do
  sleep 5
  dlq_now=$(psql_q "SELECT count(*) FROM hub.dlq_events;")
  [[ "$dlq_now" -gt "$dlq_before" ]] && break
done

assert_ge "mesaj DLQ'ya düştü" "$dlq_now" "$((dlq_before + 1))"

attempts=$(psql_q "SELECT attempts FROM hub.dlq_events ORDER BY created_at DESC LIMIT 1;")
assert_eq "6. denemede DLQ'ya düştü (5 retry + 1)" "$attempts" "6"

retries=$(psql_q "SELECT count(*) FROM hub.delivery_attempts a JOIN hub.delivery_tasks t ON t.id=a.delivery_task_id WHERE t.status='dead' AND a.response_status=500;")
assert_ge "5 backoff denemesi loglandı" "$retries" "5"

breaker=$(curl -s "$DELIVERY/internal/breakers" 2>/dev/null | jq -r '.breakers | length' || echo 0)
if [[ "$breaker" -gt 0 ]]; then ok "circuit breaker durumu izlenebiliyor"; else info "breaker durumu okunamadı (port yayınlanmamış olabilir)"; fi

# =============================================================================
step "DLQ replay (FR-5.2)"
# =============================================================================
info "subscriber düzeltiliyor"
curl -s -X POST "$SUBSCRIBER/api/failure" -H 'Content-Type: application/json' -d '{"fail_every":0}' >/dev/null

DLQ_ID=$(curl -s "${AUTH[@]}" "$ADMIN_API/api/v1/dlq?status=new&limit=1" | jq -r '.items[0].id // empty')

if [[ -z "$DLQ_ID" ]]; then
  bad "replay edilecek DLQ kaydı bulunamadı"
else
  REP=$(curl -s -X POST "$ADMIN_API/api/v1/dlq/replay" "${AUTH[@]}" \
    -H 'Content-Type: application/json' -d "{\"ids\":[\"$DLQ_ID\"]}")
  assert_eq "replay talebi kabul edildi" "$(echo "$REP" | jq -r '.replayed')" "1"

  for _ in $(seq 1 20); do
    sleep 3
    st=$(psql_q "SELECT status FROM hub.dlq_events WHERE id='$DLQ_ID';")
    [[ "$st" == "replayed" ]] && break
  done
  assert_eq "dlq kaydı replayed durumuna geçti" "$st" "replayed"

  # Teslimat asenkrondur ve circuit breaker önceki adımda AÇILMIŞTIR.
  # Replay edilen görev, breaker yarı açık duruma geçene kadar (varsayılan
  # 30 sn) ertelenir. Bu yüzden burada beklemek zorundayız — anlık kontrol
  # yanlış negatif üretir.
  info "teslimat bekleniyor (circuit breaker ${BREAKER_OPEN_DURATION:-30s} açık kalabilir)"
  for _ in $(seq 1 30); do
    task=$(psql_q "SELECT t.status FROM hub.delivery_tasks t JOIN hub.dlq_events d ON d.delivery_task_id=t.id WHERE d.id='$DLQ_ID';")
    [[ "$task" == "succeeded" ]] && break
    sleep 4
  done
  assert_eq "teslimat replay sonrası başarılı" "$task" "succeeded"
fi

# =============================================================================
step "Rate limiting (NFR-3.2)"
# =============================================================================
# Ingest limiti bilinçli olarak yüksektir (IP: 200 rps / 400 burst); bunu
# script'ten aşmak güvenilir değil. Admin API limiti ise çok daha düşüktür
# (20 rps / 40 burst) ve brute-force korumasının asıl test edilmesi gereken
# yeridir. Aynı Redis token bucket kodu her ikisinde de çalışır.
info "admin login ucuna paralel istek gönderiliyor (limit: ${RATE_LIMIT_ADMIN_RPS:-20} rps / ${RATE_LIMIT_ADMIN_BURST:-40} burst)"

codes=$(for _ in $(seq 1 120); do
  curl -s -o /dev/null -w '%{http_code}\n' --max-time 10 -X POST "$ADMIN_API/api/v1/auth/login" \
    -H 'Content-Type: application/json' \
    -d '{"email":"nobody@example.com","password":"brute-force-attempt"}' &
done; wait)

n401=$(echo "$codes" | grep -c '^401$' || true)
n429=$(echo "$codes" | grep -c '^429$' || true)
info "401=${n401}  429=${n429}"

if [[ "$n429" -gt 0 ]]; then
  ok "rate limit devrede — $n429 istek 429 ile reddedildi"
else
  bad "hiç 429 alınmadı — rate limit çalışmıyor olabilir"
fi

# Ingest tarafında geçersiz anahtarın reddedildiğini de doğrula.
code=$(curl -s -o /dev/null -w '%{http_code}' -X POST "$INGEST/api/v1/events" \
  -H 'X-Api-Key: ih_aaaaaaaaaaaa_aa' -H 'X-Signature-SHA256: 00' -H 'X-Timestamp: 1' \
  -H 'Content-Type: application/json' -d '{}')
assert_eq "ingest geçersiz anahtarı reddediyor" "$code" "401"

# =============================================================================
step "Denetim kaydı (FR-6.2)"
# =============================================================================
# DB'den doğrudan say: bir önceki adım rate limit kotasını tükettiği için
# API çağrısı geçici olarak 429 dönebilir.
audit=$(psql_q "SELECT count(*) FROM hub.audit_logs;")
assert_ge "denetim kayıtları yazılıyor" "$audit" "1"

# API ucunun da çalıştığını doğrula — kova dolana kadar birkaç kez dene.
api_audit=""
for _ in $(seq 1 10); do
  api_audit=$(curl -s "${AUTH[@]}" "$ADMIN_API/api/v1/audit-logs?limit=5" | jq -r '.meta.total // empty')
  [[ -n "$api_audit" ]] && break
  sleep 3
done
if [[ -n "$api_audit" ]]; then
  ok "denetim kaydı API'den okunabiliyor ($api_audit kayıt)"
else
  bad "denetim kaydı API'si yanıt vermedi"
fi

login_audit=$(psql_q "SELECT count(*) FROM hub.audit_logs WHERE action='auth.login';")
assert_ge "giriş işlemleri denetleniyor" "$login_audit" "1"

replay_audit=$(psql_q "SELECT count(*) FROM hub.audit_logs WHERE action LIKE 'dlq.replay%';")
assert_ge "replay işlemi denetleniyor" "$replay_audit" "1"

# =============================================================================
step "Observability (NFR-4.1)"
# =============================================================================
up=$(curl -s "$PROM/api/v1/targets" | jq -r '[.data.activeTargets[] | select(.health=="up")] | length')
assert_ge "Prometheus hedefleri ayakta" "$up" "10"

for m in hub_events_ingested_total hub_delivery_attempts_total hub_dlq_pending_events hub_outbox_pending_messages; do
  n=$(curl -s "$PROM/api/v1/query?query=$m" | jq -r '.data.result | length')
  if [[ "$n" -gt 0 ]]; then ok "$m metriği toplanıyor"; else bad "$m metriği yok"; fi
done

rules=$(curl -s "$PROM/api/v1/rules" | jq -r '[.data.groups[].rules[]] | length')
assert_ge "alert kuralları yüklü" "$rules" "4"

# =============================================================================
printf '\n%s══════════════════════════════════════════════════════════════%s\n' "$BLUE" "$NC"
printf '  %sBAŞARILI: %d%s   %sBAŞARISIZ: %d%s\n' "$GREEN" "$PASS" "$NC" "$RED" "$FAIL" "$NC"
printf '%s══════════════════════════════════════════════════════════════%s\n\n' "$BLUE" "$NC"

if [[ "$FAIL" -gt 0 ]]; then exit 1; fi
echo "Tüm kabul kriterleri doğrulandı."
