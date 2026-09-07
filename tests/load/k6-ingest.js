import http from 'k6/http'
import crypto from 'k6/crypto'
import { check, sleep } from 'k6'
import { Counter, Rate, Trend } from 'k6/metrics'
import { randomIntBetween, uuidv4 } from 'https://jslib.k6.io/k6-utils/1.4.0/index.js'

// =============================================================================
// Event Ingest yük testi.
//
// Yanıtladığı soru: "Sistem saniyede kaç event kaldırabiliyor ve darboğaz nerede?"
//
// Çalıştırma:
//   docker run --rm --network integration-hub-net \
//     -v "$PWD/tests/load:/scripts" \
//     -e INGEST_URL=http://event-ingest:8081/api/v1/events \
//     -e API_KEY=... -e HMAC_SECRET=... -e SOURCE_TOOL=mock-publisher-1 \
//     grafana/k6:0.55.0 run /scripts/k6-ingest.js
//
// API_KEY verilmezse test yalnızca kimlik doğrulama yolunu (401) ölçer;
// bu bile ingest servisinin HTTP + Redis rate-limit katmanının kapasitesini
// gösterir.
// =============================================================================

const INGEST_URL = __ENV.INGEST_URL || 'http://event-ingest:8081/api/v1/events'
const API_KEY = __ENV.API_KEY || ''
const HMAC_SECRET = __ENV.HMAC_SECRET || ''
const SOURCE_TOOL = __ENV.SOURCE_TOOL || 'mock-publisher-1'
const EVENT_TYPE = __ENV.EVENT_TYPE || 'user.created'

const accepted = new Counter('events_accepted')
const duplicated = new Counter('events_duplicated')
const rateLimited = new Counter('events_rate_limited')
const rejected = new Counter('events_rejected')
const acceptRate = new Rate('accept_rate')
const ingestLatency = new Trend('ingest_latency_ms', true)

export const options = {
  // Kademeli yük: sistemin hangi noktada bozulmaya başladığını görmek için.
  stages: [
    { duration: '20s', target: 20 },  // ısınma
    { duration: '40s', target: 100 }, // normal yük
    { duration: '40s', target: 250 }, // yüksek yük
    { duration: '20s', target: 0 },   // soğuma
  ],

  thresholds: {
    // NFR-2.1: ingest isteklerinin %95'i 50 ms altında yanıtlanmalı.
    'http_req_duration{expected_response:true}': ['p(95)<50', 'p(99)<200'],
    // Rate limit nedeniyle reddedilenler hariç, hata oranı düşük olmalı.
    http_req_failed: ['rate<0.05'],
    // Anlamlı bir yük üretildiğini garanti et.
    http_reqs: ['count>1000'],
  },

  // Özet çıktısında yüzdelikler görünsün.
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
}

function samplePayload(type) {
  switch (type) {
    case 'document.generated':
      return {
        document_id: `doc_${uuidv4().slice(0, 8)}`,
        document_type: 'invoice',
        status: 'final',
        size_bytes: randomIntBetween(1000, 5_000_000),
      }
    case 'verification.completed':
      return {
        verification_id: `ver_${uuidv4().slice(0, 8)}`,
        subject_id: `usr_${uuidv4().slice(0, 8)}`,
        subject_type: 'user',
        result: 'passed',
        score: randomIntBetween(0, 100),
      }
    default:
      return {
        user_id: `usr_${uuidv4().slice(0, 8)}`,
        email: `load-${randomIntBetween(1, 1e6)}@mindmons.local`,
        status: 'active',
        tenant_id: `tenant-${randomIntBetween(1, 5)}`,
        created_at: new Date().toISOString(),
      }
  }
}

export default function () {
  const envelope = {
    event_id: uuidv4(),
    event_type: EVENT_TYPE,
    event_version: 1,
    source_tool: SOURCE_TOOL,
    timestamp: new Date().toISOString(),
    trace_id: uuidv4(),
    payload: samplePayload(EVENT_TYPE),
    metadata: { emitted_by: 'k6-load-test' },
  }

  // Gövde BİR KEZ serileştirilir; imza tam olarak gönderilen byte dizisi
  // üzerinden hesaplanır. Yeniden serileştirmek imzayı bozar.
  const body = JSON.stringify(envelope)
  const ts = Math.floor(Date.now() / 1000).toString()

  const headers = {
    'Content-Type': 'application/json',
    'X-Api-Key': API_KEY,
    'X-Timestamp': ts,
    'X-Signature-SHA256': HMAC_SECRET
      ? crypto.hmac('sha256', HMAC_SECRET, `${ts}.${body}`, 'hex')
      : 'no-secret-provided',
    'X-Trace-Id': envelope.trace_id,
  }

  const res = http.post(INGEST_URL, body, { headers, tags: { name: 'ingest' } })

  ingestLatency.add(res.timings.duration)

  const isAccepted = res.status === 202
  acceptRate.add(isAccepted)

  if (isAccepted) {
    if (res.headers['X-Idempotent-Replay'] === 'true') duplicated.add(1)
    else accepted.add(1)
  } else if (res.status === 429) {
    rateLimited.add(1)
  } else {
    rejected.add(1)
  }

  check(res, {
    'yanıt alındı': (r) => r.status !== 0,
    'sunucu hatası yok (5xx)': (r) => r.status < 500,
    'gecikme < 200ms': (r) => r.timings.duration < 200,
  })

  sleep(randomIntBetween(10, 60) / 1000)
}

export function handleSummary(data) {
  const m = data.metrics
  const num = (path, fallback = 0) => {
    const parts = path.split('.')
    let cur = m
    for (const p of parts) {
      if (cur === undefined || cur === null) return fallback
      cur = cur[p]
    }
    return cur ?? fallback
  }

  const lines = [
    '',
    '════════════════════════════════════════════════════════════',
    '  Event Ingest — Yük Testi Sonucu',
    '════════════════════════════════════════════════════════════',
    '',
    `  Toplam istek        : ${num('http_reqs.values.count')}`,
    `  Ortalama throughput : ${num('http_reqs.values.rate').toFixed(1)} req/s`,
    '',
    `  Kabul edilen (202)  : ${num('events_accepted.values.count')}`,
    `  Mükerrer (idempotent): ${num('events_duplicated.values.count')}`,
    `  Rate limit (429)    : ${num('events_rate_limited.values.count')}`,
    `  Reddedilen          : ${num('events_rejected.values.count')}`,
    '',
    '  Gecikme (ms)',
    `    ortalama : ${num('http_req_duration.values.avg').toFixed(2)}`,
    `    medyan   : ${num('http_req_duration.values.med').toFixed(2)}`,
    `    p95      : ${num('http_req_duration.values["p(95)"]').toFixed(2)}   ← NFR-2.1 hedefi: < 50`,
    `    p99      : ${num('http_req_duration.values["p(99)"]').toFixed(2)}`,
    `    maks     : ${num('http_req_duration.values.max').toFixed(2)}`,
    '',
    '  Eşik sonuçları:',
  ]

  for (const [name, t] of Object.entries(data.thresholds ?? {})) {
    lines.push(`    ${t.ok ? '✓' : '✗'} ${name}`)
  }

  lines.push('', '════════════════════════════════════════════════════════════', '')

  return {
    stdout: lines.join('\n'),
    '/scripts/k6-summary.json': JSON.stringify(data, null, 2),
  }
}
