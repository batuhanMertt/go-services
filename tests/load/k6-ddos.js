import http from 'k6/http'
import crypto from 'k6/crypto'
import { check } from 'k6'
import { Counter, Rate } from 'k6/metrics'
import { uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js'

// =============================================================================
// DDoS katmanı simülasyonu (internal/ddos).
//
// Yanıtladığı soru: "Tek bir IP'den gelen L7 flood/credential-stuffing
// saldırısı sırasında (a) saldırgan IP gerçekten banlanıyor mu ve (b) aynı
// anda gelen NORMAL trafik bu saldırıdan etkileniyor mu?"
//
// Bu, 200 Gbit/s'lik hacimsel bir saldırıyı SİMÜLE ETMEZ (o katman upstream/
// scrubbing sağlayıcısının işidir ve k6 gibi bir istemciyle test edilemez).
// Burada test edilen, tek bir kaynaktan gelen yoğun L7 istek trafiğine karşı
// uygulama katmanının (ConnLimiter + Detector + Guard) davranışıdır.
//
// İki senaryo eşzamanlı çalışır:
//   - attacker: sabit bir IP'den (X-Forwarded-For ile simüle edilir),
//     API key göndermeden yüksek hızda istek yağdırır. Beklenen: önce
//     rate limit (429), sonra Guard eşiği aşılınca ban (403).
//   - normal: 50 farklı simüle IP'den, gerçek kimlik bilgileriyle (varsa)
//     ölçülü istek gönderir. Beklenen: saldırıdan etkilenmeden yüksek kabul
//     oranı.
//
// Çalıştırma (API_KEY/HMAC_SECRET verilmezse "normal" senaryo da 401 alır;
// bu durumda sadece "saldırgan banlanıyor mu" sorusu anlamlı olur):
//   docker run --rm --network integration-hub-net \
//     -v "$PWD/tests/load:/scripts" \
//     -e INGEST_URL=http://event-ingest:8081/api/v1/events \
//     -e API_KEY=... -e HMAC_SECRET=... -e SOURCE_TOOL=mock-publisher-1 \
//     grafana/k6:0.55.0 run /scripts/k6-ddos.js
// =============================================================================

const INGEST_URL = __ENV.INGEST_URL || 'http://event-ingest:8081/api/v1/events'
const API_KEY = __ENV.API_KEY || ''
const HMAC_SECRET = __ENV.HMAC_SECRET || ''
const SOURCE_TOOL = __ENV.SOURCE_TOOL || 'mock-publisher-1'

// TEST-NET-3 (RFC 5737) — gerçek bir IP'ye çarpmaz, sadece dokümantasyon amaçlı.
const ATTACKER_IP = '203.0.113.66'

const attackRateLimited = new Counter('attack_rate_limited_429')
const attackBanned = new Counter('attack_banned_403')
const attackOther = new Counter('attack_other')
const normalAccepted = new Counter('normal_accepted')
const normalRejected = new Counter('normal_rejected')
const normalAcceptRate = new Rate('normal_accept_rate')

export const options = {
  scenarios: {
    attacker: {
      executor: 'constant-arrival-rate',
      rate: 200,
      timeUnit: '1s',
      duration: '30s',
      preAllocatedVUs: 50,
      maxVUs: 100,
      exec: 'attack',
    },
    normal: {
      executor: 'constant-arrival-rate',
      rate: 20,
      timeUnit: '1s',
      duration: '35s',
      preAllocatedVUs: 20,
      maxVUs: 50,
      exec: 'normal',
    },
  },

  thresholds: {
    // Saldırgan er ya da geç banlanmalı — hiç 403 görülmemesi Guard'ın
    // devreye girmediği anlamına gelir.
    attack_banned_403: ['count>0'],
    // Sunucu hiçbir zaman 5xx ile çökmemeli (rate limit/ban 4xx döner).
    http_req_failed: ['rate<0.01'],
  },

  summaryTrendStats: ['avg', 'med', 'p(95)', 'max'],
}

function post(headers) {
  const envelope = {
    event_id: uuidv4(),
    event_type: 'user.created',
    event_version: 1,
    source_tool: SOURCE_TOOL,
    timestamp: new Date().toISOString(),
    trace_id: uuidv4(),
    payload: { user_id: `usr_${uuidv4().slice(0, 8)}`, status: 'active' },
    metadata: { emitted_by: 'k6-ddos-sim' },
  }
  const body = JSON.stringify(envelope)
  return http.post(INGEST_URL, body, { headers, tags: { name: 'ingest' } })
}

// attack, kimlik bilgisi GÖNDERMEDEN ısrarla istek yollar — brute-force/
// credential-stuffing benzeri en ucuz saldırı şeklidir ve handler.go'daki
// guard.Strike çağrılarını (adım 2) tetikler.
export function attack() {
  const res = post({
    'Content-Type': 'application/json',
    'X-Forwarded-For': ATTACKER_IP,
  })

  if (res.status === 429) attackRateLimited.add(1)
  else if (res.status === 403) attackBanned.add(1)
  else attackOther.add(1)

  check(res, { 'sunucu çökmedi (5xx yok)': (r) => r.status < 500 })
}

// normal, farklı simüle IP'lerden ölçülü ve (varsa) doğru imzalı istek
// gönderir. __VU ile IP'yi sabitlemek, aynı VU'nun testin süresince aynı
// "kullanıcı" gibi davranmasını sağlar.
export function normal() {
  const ip = `198.51.100.${(__VU % 250) + 1}`
  const ts = Math.floor(Date.now() / 1000).toString()

  // Gövdeyi imzalamak için önce üretip sonra imzalıyoruz; post() içindeki
  // gövdeyle AYNI event_id'yi kullanmak imzayı tutarlı kılar.
  const envelope = {
    event_id: uuidv4(),
    event_type: 'user.created',
    event_version: 1,
    source_tool: SOURCE_TOOL,
    timestamp: new Date().toISOString(),
    trace_id: uuidv4(),
    payload: { user_id: `usr_${uuidv4().slice(0, 8)}`, status: 'active' },
    metadata: { emitted_by: 'k6-ddos-sim' },
  }
  const body = JSON.stringify(envelope)

  const res = http.post(INGEST_URL, body, {
    headers: {
      'Content-Type': 'application/json',
      'X-Forwarded-For': ip,
      'X-Api-Key': API_KEY,
      'X-Timestamp': ts,
      'X-Signature-SHA256': HMAC_SECRET
        ? crypto.hmac('sha256', HMAC_SECRET, `${ts}.${body}`, 'hex')
        : 'no-secret-provided',
    },
    tags: { name: 'ingest' },
  })

  const accepted = res.status === 202
  normalAcceptRate.add(accepted)
  if (accepted) normalAccepted.add(1)
  else normalRejected.add(1)

  check(res, { 'saldırıdan sızma yok (5xx yok)': (r) => r.status < 500 })
}

export function handleSummary(data) {
  const m = data.metrics
  const num = (name, fallback = 0) => m[name]?.values?.count ?? fallback

  const lines = [
    '',
    '════════════════════════════════════════════════════════════',
    '  DDoS Katmanı Simülasyonu — Sonuç',
    '════════════════════════════════════════════════════════════',
    '',
    '  Saldırgan (tek IP, kimliksiz istek):',
    `    429 (rate limit) : ${num('attack_rate_limited_429')}`,
    `    403 (banlandı)   : ${num('attack_banned_403')}  ← Guard devreye girdi mi?`,
    `    diğer            : ${num('attack_other')}`,
    '',
    '  Normal trafik (50 farklı simüle IP):',
    `    kabul (202)      : ${num('normal_accepted')}`,
    `    reddedilen       : ${num('normal_rejected')}`,
    `    kabul oranı      : ${((m.normal_accept_rate?.values?.rate ?? 0) * 100).toFixed(1)}%`,
    '',
    '  Eşik sonuçları:',
  ]

  for (const [name, t] of Object.entries(data.thresholds ?? {})) {
    lines.push(`    ${t.ok ? '✓' : '✗'} ${name}`)
  }

  lines.push('', '════════════════════════════════════════════════════════════', '')

  return {
    stdout: lines.join('\n'),
    '/scripts/k6-ddos-summary.json': JSON.stringify(data, null, 2),
  }
}
