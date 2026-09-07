package main

const uiHTML = `<!DOCTYPE html>
<html lang="tr">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Mock Subscriber · Integration Hub</title>
<style>
  :root{--bg:#0e1117;--panel:#161b22;--line:#232a35;--text:#e6edf3;--muted:#8b949e;
        --accent:#4f9dff;--ok:#3fb950;--warn:#d29922;--err:#f85149}
  *{box-sizing:border-box}
  body{margin:0;font:14px/1.6 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;
       background:var(--bg);color:var(--text)}
  header{padding:20px 24px;border-bottom:1px solid var(--line);display:flex;align-items:center;
         gap:12px;flex-wrap:wrap}
  h1{font-size:17px;margin:0;font-weight:650}
  .badge{font-size:11px;padding:3px 9px;border-radius:99px;border:1px solid var(--line);
         color:var(--muted);text-transform:uppercase;letter-spacing:.06em}
  .badge.on{color:var(--ok);border-color:#1f6f34;background:#0d2a16}
  .badge.off{color:var(--err);border-color:#6f2320;background:#2a0f0e}
  .badge.warn{color:var(--warn);border-color:#6b5121;background:#2b220c}
  main{padding:24px;display:grid;gap:20px;max-width:1200px}
  .card{background:var(--panel);border:1px solid var(--line);border-radius:12px;padding:18px}
  .card h2{font-size:13px;margin:0 0 14px;color:var(--muted);text-transform:uppercase;
           letter-spacing:.08em;font-weight:600}
  label{display:block;font-size:12px;color:var(--muted);margin:12px 0 5px}
  input{width:180px;padding:9px 11px;background:#0d1117;color:var(--text);
        border:1px solid var(--line);border-radius:8px;font:inherit}
  .row{display:flex;gap:10px;flex-wrap:wrap;margin-top:16px;align-items:flex-end}
  button{padding:9px 16px;border-radius:8px;border:1px solid var(--line);background:#21262d;
         color:var(--text);font:inherit;font-weight:550;cursor:pointer}
  button:hover{background:#2d333b}
  button.primary{background:var(--accent);border-color:var(--accent);color:#04121f}
  button.danger{background:#3d1a19;border-color:#6f2320;color:#ff9c96}
  table{width:100%;border-collapse:collapse;font-size:12.5px}
  th{text-align:left;color:var(--muted);font-weight:600;padding:7px 8px;
     border-bottom:1px solid var(--line);font-size:11px;text-transform:uppercase}
  td{padding:7px 8px;border-bottom:1px solid #1b212b}
  code{font-family:ui-monospace,Menlo,monospace;font-size:12px}
  .ok{color:var(--ok)} .warn{color:var(--warn)} .err{color:var(--err)}
  .hint{color:var(--muted);font-size:12px;margin-top:10px}
  .kv{display:flex;justify-content:space-between;padding:6px 0;border-bottom:1px solid #1b212b}
  .kv:last-child{border:0}
  .kv span:first-child{color:var(--muted)}
</style>
</head>
<body>
<header>
  <h1>Mock Subscriber 1</h1>
  <span id="regBadge" class="badge">kontrol ediliyor…</span>
  <span id="failBadge" class="badge">arıza: ?</span>
</header>

<main>
  <section class="card">
    <h2>Arıza Simülasyonu</h2>
    <div id="status"></div>

    <div class="row">
      <div>
        <label for="every">Her N istekten birini fail et</label>
        <input id="every" type="number" min="0" max="1000" value="0">
      </div>
      <button class="primary" onclick="apply()">Uygula</button>
      <button class="danger" onclick="setEvery(1)">Hepsini fail et</button>
      <button onclick="setEvery(0)">Kapat</button>
    </div>
    <p class="hint">
      Bu anahtar açıkken subscriber 500 döner. Delivery worker exponential backoff ile
      (1s, 2s, 4s, 8s, 16s) yeniden dener; 5 deneme sonunda mesaj DLQ'ya düşer ve
      circuit breaker açılır. Admin panelindeki DLQ sayfasından replay edebilirsiniz.
    </p>
  </section>

  <section class="card">
    <h2>Alınan Webhook'lar</h2>
    <table>
      <thead>
        <tr><th>Zaman</th><th>Tip</th><th>event_id</th><th>Deneme</th>
            <th>İmza</th><th>Sonuç</th><th>Yanıt</th></tr>
      </thead>
      <tbody id="rows"></tbody>
    </table>
  </section>
</main>

<script>
const $ = id => document.getElementById(id);

async function loadStatus() {
  try {
    const s = await (await fetch('/api/status')).json();

    $('regBadge').textContent = s.registered ? 'abone · ' + s.tool_slug : 'kaydolmadı';
    $('regBadge').className = 'badge ' + (s.registered ? 'on' : 'off');

    $('failBadge').textContent = s.fail_every > 0
      ? 'arıza: her ' + s.fail_every + ' istekte 1'
      : 'arıza: kapalı';
    $('failBadge').className = 'badge ' + (s.fail_every > 0 ? 'warn' : '');

    $('status').innerHTML =
      kv('Webhook URL', s.webhook_url) +
      kv('Fail durumu', s.fail_every > 0 ? 'her ' + s.fail_every + ' istekten 1 → HTTP ' + s.fail_status : 'kapalı') +
      kv('Toplam istek', s.processed);
  } catch (e) {}
}

function kv(k, v) {
  return '<div class="kv"><span>' + k + '</span><span><code>' + v + '</code></span></div>';
}

async function loadRows() {
  try {
    const r = await (await fetch('/api/received')).json();
    $('rows').innerHTML = (r.items || []).map(i => {
      const sigCls = i.signature_status === 'valid' ? 'ok' : 'err';
      const outCls = i.outcome === 'processed' ? 'ok' : (i.outcome === 'rejected' ? 'err' : 'warn');
      const replay = i.replay ? ' <span class="warn">(replay)</span>' : '';
      return '<tr>' +
        '<td>' + new Date(i.at).toLocaleTimeString('tr-TR') + '</td>' +
        '<td>' + (i.event_type || '—') + '</td>' +
        '<td><code>' + (i.event_id || '—').slice(0, 8) + '…</code></td>' +
        '<td>' + (i.attempt || '—') + replay + '</td>' +
        '<td class="' + sigCls + '">' + i.signature_status.split(':')[0] + '</td>' +
        '<td class="' + outCls + '">' + i.outcome + '</td>' +
        '<td>' + i.status + '</td>' +
      '</tr>';
    }).join('');
  } catch (e) {}
}

async function setEvery(n) {
  $('every').value = n;
  await apply();
}

async function apply() {
  await fetch('/api/failure', {
    method: 'POST',
    headers: {'Content-Type': 'application/json'},
    body: JSON.stringify({ fail_every: parseInt($('every').value, 10) || 0 })
  });
  loadStatus();
}

loadStatus(); loadRows();
setInterval(loadStatus, 4000);
setInterval(loadRows, 2000);
</script>
</body>
</html>`
