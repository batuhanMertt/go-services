package main

// uiHTML, mock publisher'ın tek dosyalık kontrol arayüzü.
// Bilinçli olarak bağımlılıksız (framework/CDN yok): mock tool'un amacı
// ekosistemi simüle etmek, kendi başına bir ürün olmak değil.
const uiHTML = `<!DOCTYPE html>
<html lang="tr">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Mock Publisher · Integration Hub</title>
<style>
  :root{
    --bg:#0e1117; --panel:#161b22; --line:#232a35; --text:#e6edf3;
    --muted:#8b949e; --accent:#4f9dff; --ok:#3fb950; --warn:#d29922; --err:#f85149;
  }
  *{box-sizing:border-box}
  body{margin:0;font:14px/1.6 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;
       background:var(--bg);color:var(--text)}
  header{padding:20px 24px;border-bottom:1px solid var(--line);display:flex;
         align-items:center;gap:12px;flex-wrap:wrap}
  h1{font-size:17px;margin:0;font-weight:650;letter-spacing:-.01em}
  .badge{font-size:11px;padding:3px 9px;border-radius:99px;border:1px solid var(--line);
         color:var(--muted);text-transform:uppercase;letter-spacing:.06em}
  .badge.on{color:var(--ok);border-color:#1f6f34;background:#0d2a16}
  .badge.off{color:var(--err);border-color:#6f2320;background:#2a0f0e}
  main{padding:24px;display:grid;gap:20px;grid-template-columns:repeat(auto-fit,minmax(340px,1fr));
       max-width:1200px}
  .card{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:18px}
  .card h2{font-size:13px;margin:0 0 14px;color:var(--muted);text-transform:uppercase;
           letter-spacing:.08em;font-weight:600}
  label{display:block;font-size:12px;color:var(--muted);margin:12px 0 5px}
  select,input{width:100%;padding:9px 11px;background:#0d1117;color:var(--text);
               border:1px solid var(--line);border-radius:8px;font:inherit}
  select:focus,input:focus{outline:2px solid var(--accent);outline-offset:-1px;border-color:transparent}
  .row{display:flex;gap:10px;flex-wrap:wrap;margin-top:16px}
  button{padding:9px 16px;border-radius:8px;border:1px solid var(--line);background:#21262d;
         color:var(--text);font:inherit;font-weight:550;cursor:pointer;transition:.15s}
  button:hover{background:#2d333b;border-color:#3d444d}
  button.primary{background:var(--accent);border-color:var(--accent);color:#04121f}
  button.primary:hover{background:#6cb0ff}
  button:disabled{opacity:.5;cursor:not-allowed}
  table{width:100%;border-collapse:collapse;font-size:12.5px}
  th{text-align:left;color:var(--muted);font-weight:600;padding:7px 8px;
     border-bottom:1px solid var(--line);font-size:11px;text-transform:uppercase;letter-spacing:.05em}
  td{padding:7px 8px;border-bottom:1px solid #1b212b;vertical-align:top}
  code{font-family:ui-monospace,"SF Mono",Menlo,monospace;font-size:12px}
  .st-2{color:var(--ok)} .st-4{color:var(--warn)} .st-5{color:var(--err)} .st-0{color:var(--err)}
  .dup{color:var(--warn);font-size:11px}
  .hint{color:var(--muted);font-size:12px;margin-top:10px;line-height:1.55}
  .full{grid-column:1/-1}
  .kv{display:flex;justify-content:space-between;padding:6px 0;border-bottom:1px solid #1b212b;font-size:13px}
  .kv:last-child{border:0}
  .kv span:first-child{color:var(--muted)}
</style>
</head>
<body>
<header>
  <h1>Mock Publisher 1</h1>
  <span id="regBadge" class="badge">kayıt kontrol ediliyor…</span>
  <span id="autoBadge" class="badge">auto: ?</span>
</header>

<main>
  <section class="card">
    <h2>Event Fırlat</h2>
    <label for="etype">Event tipi</label>
    <select id="etype"></select>

    <label for="count">Adet (1-100)</label>
    <input id="count" type="number" value="1" min="1" max="100">

    <label for="eid">Sabit event_id (opsiyonel — idempotency testi)</label>
    <input id="eid" type="text" placeholder="boş bırakılırsa her seferinde yeni UUID">

    <div class="row">
      <button class="primary" onclick="trigger()">Event gönder</button>
      <button onclick="burst()">10 event burst</button>
      <button onclick="duplicate()">Aynı id'yi 2 kez gönder</button>
    </div>
    <p class="hint">
      Her event UUID v4 <code>event_id</code> ile üretilir, gövde HMAC-SHA256 ile
      imzalanır ve <code>POST /api/v1/events</code> ile platforma gönderilir.
      Aynı <code>event_id</code> ikinci kez gönderildiğinde platform
      <code>X-Idempotent-Replay: true</code> döner ve mükerrer kayıt oluşmaz.
    </p>
  </section>

  <section class="card">
    <h2>Durum & Otomatik Gönderim</h2>
    <div id="status"></div>

    <label for="interval">Otomatik gönderim aralığı</label>
    <input id="interval" type="text" value="5s" placeholder="örn. 5s">

    <div class="row">
      <button class="primary" onclick="setAuto(true)">Otomatiği başlat</button>
      <button onclick="setAuto(false)">Durdur</button>
    </div>
    <p class="hint">
      Otomatik mod, boru hattını sürekli beslemek için kullanılır; Grafana'da
      canlı grafik ve Kafka consumer lag gözlemlemek isterseniz açın.
    </p>
  </section>

  <section class="card full">
    <h2>Son Gönderimler</h2>
    <table>
      <thead>
        <tr><th>Zaman</th><th>Tip</th><th>event_id</th><th>Durum</th><th>Süre</th><th>Yanıt</th></tr>
      </thead>
      <tbody id="recent"></tbody>
    </table>
  </section>
</main>

<script>
const $ = id => document.getElementById(id);

async function api(path, opts) {
  const res = await fetch(path, opts);
  return res.json();
}

async function loadStatus() {
  try {
    const s = await api('/api/status');

    $('regBadge').textContent = s.registered ? 'kayıtlı · ' + s.tool_slug : 'kaydolmadı';
    $('regBadge').className = 'badge ' + (s.registered ? 'on' : 'off');

    $('autoBadge').textContent = 'auto: ' + (s.auto_fire ? 'açık (' + s.interval + ')' : 'kapalı');
    $('autoBadge').className = 'badge ' + (s.auto_fire ? 'on' : '');

    $('status').innerHTML =
      kv('Tool', s.tool_slug || '—') +
      kv('API key prefix', s.key_prefix || '—') +
      kv('Ingest URL', s.ingest_url) +
      kv('Otomatik gönderim', s.auto_fire ? 'açık' : 'kapalı');

    const sel = $('etype');
    if (sel.options.length === 0) {
      s.event_types.forEach(t => {
        const o = document.createElement('option');
        o.value = t; o.textContent = t;
        sel.appendChild(o);
      });
    }
  } catch (e) { /* servis yeniden başlıyor olabilir */ }
}

function kv(k, v) {
  return '<div class="kv"><span>' + k + '</span><span><code>' + v + '</code></span></div>';
}

async function loadRecent() {
  try {
    const r = await api('/api/recent');
    $('recent').innerHTML = (r.items || []).map(i => {
      const cls = 'st-' + String(i.status).charAt(0);
      const dup = i.duplicate ? ' <span class="dup">(mükerrer)</span>' : '';
      const resp = (i.error || i.response || '').slice(0, 110);
      return '<tr>' +
        '<td>' + new Date(i.at).toLocaleTimeString('tr-TR') + '</td>' +
        '<td>' + i.event_type + '</td>' +
        '<td><code>' + i.event_id.slice(0, 8) + '…</code></td>' +
        '<td class="' + cls + '">' + (i.status || 'ERR') + dup + '</td>' +
        '<td>' + i.latency_ms + ' ms</td>' +
        '<td><code>' + escapeHtml(resp) + '</code></td>' +
      '</tr>';
    }).join('');
  } catch (e) {}
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;'}[c]));
}

async function trigger() {
  await api('/api/trigger', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({
      event_type: $('etype').value,
      count: parseInt($('count').value, 10) || 1,
      event_id: $('eid').value.trim()
    })
  });
  loadRecent();
}

async function burst() {
  await api('/api/trigger', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({ event_type: $('etype').value, count: 10 })
  });
  loadRecent();
}

async function duplicate() {
  const id = 'dup-test-' + Date.now();
  for (let i = 0; i < 2; i++) {
    await api('/api/trigger', {
      method: 'POST',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify({ event_type: $('etype').value, count: 1, event_id: id })
    });
  }
  loadRecent();
}

async function setAuto(enabled) {
  await api('/api/auto', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({ enabled, interval: $('interval').value.trim() })
  });
  loadStatus();
}

loadStatus(); loadRecent();
setInterval(loadStatus, 5000);
setInterval(loadRecent, 2000);
</script>
</body>
</html>`
