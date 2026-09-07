package main

const uiHTML = `<!DOCTYPE html>
<html lang="tr">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Webhook Sink · Integration Hub</title>
<style>
  :root{--bg:#0e1117;--panel:#161b22;--line:#232a35;--text:#e6edf3;--muted:#8b949e;
        --accent:#4f9dff;--ok:#3fb950;--warn:#d29922;--err:#f85149}
  *{box-sizing:border-box}
  body{margin:0;font:14px/1.6 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;
       background:var(--bg);color:var(--text);height:100vh;display:flex;flex-direction:column}
  header{padding:16px 20px;border-bottom:1px solid var(--line);display:flex;gap:12px;
         align-items:center;flex-wrap:wrap;flex:0 0 auto}
  h1{font-size:16px;margin:0;font-weight:650}
  .badge{font-size:11px;padding:3px 9px;border-radius:99px;border:1px solid var(--line);color:var(--muted)}
  .badge.on{color:var(--ok);border-color:#1f6f34;background:#0d2a16}
  .grow{flex:1}
  input,select{padding:8px 11px;background:#0d1117;color:var(--text);border:1px solid var(--line);
               border-radius:8px;font:inherit}
  input[type=search]{width:280px}
  button{padding:8px 14px;border-radius:8px;border:1px solid var(--line);background:#21262d;
         color:var(--text);font:inherit;cursor:pointer}
  button:hover{background:#2d333b}
  .layout{flex:1;display:grid;grid-template-columns:minmax(360px,1fr) minmax(400px,1.2fr);
          overflow:hidden}
  .list{overflow:auto;border-right:1px solid var(--line)}
  .detail{overflow:auto;padding:20px}
  .item{padding:12px 16px;border-bottom:1px solid #1b212b;cursor:pointer;transition:.12s}
  .item:hover{background:#161b22}
  .item.sel{background:#1c2331;border-left:3px solid var(--accent);padding-left:13px}
  .item .top{display:flex;justify-content:space-between;gap:10px;align-items:center}
  .item .type{font-weight:600;font-size:13px}
  .item .meta{color:var(--muted);font-size:11.5px;margin-top:3px;
              display:flex;gap:10px;flex-wrap:wrap}
  .pill{font-size:10.5px;padding:2px 7px;border-radius:99px;border:1px solid var(--line)}
  .pill.valid{color:var(--ok);border-color:#1f6f34}
  .pill.invalid{color:var(--err);border-color:#6f2320}
  .pill.missing{color:var(--warn);border-color:#6b5121}
  pre{background:#0d1117;border:1px solid var(--line);border-radius:10px;padding:14px;
      overflow:auto;font-family:ui-monospace,Menlo,monospace;font-size:12px;line-height:1.6;
      white-space:pre-wrap;word-break:break-word;margin:0 0 16px}
  h3{font-size:12px;color:var(--muted);text-transform:uppercase;letter-spacing:.08em;
     margin:0 0 8px;font-weight:600}
  .empty{padding:60px 20px;text-align:center;color:var(--muted)}
  code{font-family:ui-monospace,Menlo,monospace}
</style>
</head>
<body>
<header>
  <h1>Webhook Sink</h1>
  <span id="statusBadge" class="badge">…</span>
  <span class="grow"></span>
  <input id="q" type="search" placeholder="event_id, trace_id veya gövdede ara…" oninput="load()">
  <select id="sig" onchange="load()">
    <option value="">tüm imzalar</option>
    <option value="valid">geçerli</option>
    <option value="invalid">geçersiz</option>
    <option value="missing">imzasız</option>
  </select>
  <button onclick="clearAll()">Temizle</button>
</header>

<div class="layout">
  <div class="list" id="list"></div>
  <div class="detail" id="detail">
    <div class="empty">Soldan bir webhook seçin.</div>
  </div>
</div>

<script>
const $ = id => document.getElementById(id);
let selected = null;

async function loadStatus() {
  try {
    const s = await (await fetch('/api/status')).json();
    $('statusBadge').textContent = s.captured + ' / ' + s.capacity + ' kayıt';
    $('statusBadge').className = 'badge ' + (s.registered ? 'on' : '');
  } catch (e) {}
}

async function load() {
  try {
    const params = new URLSearchParams();
    if ($('q').value.trim()) params.set('q', $('q').value.trim());
    if ($('sig').value) params.set('signature', $('sig').value);

    const r = await (await fetch('/api/captures?' + params)).json();
    const items = r.items || [];

    if (items.length === 0) {
      $('list').innerHTML = '<div class="empty">Henüz webhook yakalanmadı.<br>' +
        'Mock Publisher üzerinden event fırlatın.</div>';
      return;
    }

    $('list').innerHTML = items.map(i =>
      '<div class="item' + (i.id === selected ? ' sel' : '') + '" onclick="show(\'' + i.id + '\')">' +
        '<div class="top">' +
          '<span class="type">' + (i.event_type || i.path) + '</span>' +
          '<span class="pill ' + i.signature_status + '">' + i.signature_status + '</span>' +
        '</div>' +
        '<div class="meta">' +
          '<span>' + new Date(i.at).toLocaleTimeString('tr-TR') + '</span>' +
          '<span>deneme ' + (i.attempt || '?') + '</span>' +
          '<span>' + i.size_bytes + ' B</span>' +
          '<span><code>' + (i.event_id || '').slice(0, 10) + '</code></span>' +
        '</div>' +
      '</div>'
    ).join('');
  } catch (e) {}
}

async function show(id) {
  selected = id;
  try {
    const c = await (await fetch('/api/captures/' + id)).json();

    let body = c.body_text;
    try { body = JSON.stringify(JSON.parse(c.body_text), null, 2); } catch (e) {}

    $('detail').innerHTML =
      '<h3>Özet</h3><pre>' + esc(
        'zaman      : ' + new Date(c.at).toISOString() + '\n' +
        'event_id   : ' + (c.event_id || '—') + '\n' +
        'event_type : ' + (c.event_type || '—') + '\n' +
        'deneme     : ' + (c.attempt || '—') + '\n' +
        'trace_id   : ' + (c.trace_id || '—') + '\n' +
        'imza       : ' + c.signature_status + '\n' +
        'kaynak ip  : ' + c.source_ip
      ) + '</pre>' +
      '<h3>Başlıklar</h3><pre>' + esc(
        Object.entries(c.headers).sort().map(([k, v]) => k + ': ' + v).join('\n')
      ) + '</pre>' +
      '<h3>Gövde</h3><pre>' + esc(body) + '</pre>';
    load();
  } catch (e) {}
}

function esc(s) {
  return String(s).replace(/[&<>]/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;'}[c]));
}

async function clearAll() {
  await fetch('/api/captures', { method: 'DELETE' });
  selected = null;
  $('detail').innerHTML = '<div class="empty">Soldan bir webhook seçin.</div>';
  load(); loadStatus();
}

loadStatus(); load();
setInterval(() => { load(); loadStatus(); }, 2000);
</script>
</body>
</html>`
