// Service worker do ISO Builder: existe para o painel poder ser INSTALADO como
// app (PWA). Não guarda o painel em cache (os dados são sempre ao vivo); só
// mostra uma página "sem conexão" quando a internet cai ao abrir o app.
// Downloads (ISO de ~4 GB, gravador), API e o terminal nunca passam por aqui.
const OFFLINE = "offline-v1";
const OFFLINE_HTML = `<!doctype html><html lang="pt-BR"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1"><title>ISO Builder</title>
<style>body{margin:0;min-height:100vh;display:grid;place-items:center;background:#0f1115;color:#eef0f3;
font:16px system-ui,sans-serif;text-align:center;padding:24px}button{margin-top:16px;padding:12px 20px;border:0;
border-radius:10px;background:#e95420;color:#fff;font:inherit;font-weight:600}</style></head>
<body><div><h1>Sem conexão</h1><p>Não foi possível falar com o servidor do ISO Builder.</p>
<button onclick="location.reload()">Tentar de novo</button></div></body></html>`;

self.addEventListener("install", (event) => {
  event.waitUntil(caches.open(OFFLINE).then((c) => c.put("/__offline", new Response(OFFLINE_HTML, {
    headers: { "Content-Type": "text/html; charset=utf-8" } }))).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (event) => {
  event.waitUntil(caches.keys().then((ks) => Promise.all(ks.filter((k) => k !== OFFLINE).map((k) => caches.delete(k))))
    .then(() => self.clients.claim()));
});

self.addEventListener("fetch", (event) => {
  const req = event.request;
  if (req.mode !== "navigate") return;               // só a abertura das páginas
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  if (/^\/(download|t|api)\//.test(url.pathname)) return;  // downloads: direto, sem o SW
  event.respondWith(fetch(req).catch(() => caches.match("/__offline")));
});
