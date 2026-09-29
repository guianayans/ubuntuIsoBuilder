/* ISO Builder — front-end (sem dependências). */
"use strict";

const $ = (s, el = document) => el.querySelector(s);
const $$ = (s, el = document) => [...el.querySelectorAll(s)];
const esc = s => String(s ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
const fmtBytes = n => { if (!n) return "—"; const u = ["B", "KB", "MB", "GB", "TB"]; let i = 0; while (n >= 1000 && i < 4) { n /= 1000; i++; } return (i ? n.toFixed(1) : n) + " " + u[i]; };
const fmtDate = t => t ? new Date(t * 1000).toLocaleString("pt-BR", { dateStyle: "short", timeStyle: "short" }) : "—";
const view = $("#view");
let ME = null;
let timer = null;

function toast(msg) {
  const t = document.createElement("div");
  t.className = "toast"; t.textContent = msg; document.body.appendChild(t);
  setTimeout(() => t.remove(), 2600);
}

async function api(path, opts = {}) {
  const o = { method: opts.method || "GET", headers: { "X-Requested-With": "uab" }, credentials: "same-origin" };
  if (opts.body !== undefined) { o.headers["Content-Type"] = "application/json"; o.body = JSON.stringify(opts.body); }
  if (o.method !== "GET" && opts.body === undefined) { o.headers["Content-Type"] = "application/json"; o.body = "{}"; }
  const r = await fetch(path, o);
  const ct = r.headers.get("content-type") || "";
  const data = ct.includes("json") ? await r.json() : await r.text();
  if (r.status === 401 && path !== "/api/login" && path !== "/api/me") { ME = null; go("#/login"); throw new Error("Sessão expirada. Entre novamente."); }
  if (!r.ok) throw new Error((data && data.error) || "Erro " + r.status);
  return data;
}

// Endereços limpos (iso.gvtserver.online/maquinas). Por dentro as telas continuam
// identificadas por "#/…"; /remoto é do MeshCentral, então a página dele é /acesso-remoto.
const ROUTE_ALIAS = { "#/remoto": "/acesso-remoto" };
function toPath(h) { return ROUTE_ALIAS[h] || (String(h).replace(/^#/, "") || "/"); }
function curRoute() {
  const p = location.pathname || "/";
  const alias = Object.entries(ROUTE_ALIAS).find(([, v]) => v === p);
  return alias ? alias[0] : "#" + p;
}
function go(h) { const p = toPath(h); if (location.pathname !== p || location.hash) history.pushState(null, "", p); route(); }
// links internos (href="#/…") navegam sem recarregar e sem "#" no endereço
document.addEventListener("click", e => {
  const a = e.target.closest && e.target.closest('a[href^="#/"]');
  if (!a || e.defaultPrevented || e.button !== 0 || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || a.target === "_blank") return;
  e.preventDefault();
  go(a.getAttribute("href"));
});
// links antigos com "#/…" (favoritos, app instalado, mensagens já enviadas)
function legacyHash() { if (location.hash.startsWith("#/")) history.replaceState(null, "", toPath(location.hash)); }
legacyHash();

/* -------------------------------------------------- detecção do sistema */
function detectOS() {
  const ua = navigator.userAgent || "";
  const p = (navigator.userAgentData && navigator.userAgentData.platform) || navigator.platform || "";
  if (/Win/i.test(p) || /Windows/i.test(ua)) return /ARM64|aarch64/i.test(ua) ? "windows-arm64" : "windows";
  if (/Mac/i.test(p) || /Mac OS X/i.test(ua)) return /iPhone|iPad/i.test(ua) ? "mobile" : "macos";
  if (/Android|iPhone|iPad/i.test(ua)) return "mobile";
  if (/Linux/i.test(p) || /Linux/i.test(ua)) return "linux";
  return "windows";
}
const OS_NAME = { windows: "Windows", "windows-arm64": "Windows (ARM)", macos: "Mac", linux: "Linux", mobile: "celular" };

/* -------------------------------------------------- atalho do TerminalSec (header) */
// Só aparece para admin e com TERMINALSEC_CODE configurado; verde = o painel respondeu à chave da
// integração, vermelho = sem conexão (fora do ar, chave revogada ou rede). Confere a cada minuto.
let tsecTimer = null, tsecState = null;
async function tsecHeader() {
  const b = $("#tsecBtn");
  if (!b) return;
  if (!(ME && ME.role === "admin" && FEATURES.terminalsec)) { b.classList.add("hidden"); return; }
  const check = async () => {
    let st = null;
    try { st = await api("/api/terminalsec/status"); } catch (e) { st = null; }
    const wasHidden = b.classList.contains("hidden");
    if (!st || !st.configured) { b.classList.add("hidden"); if (!wasHidden) fitHeader(); return; }
    b.classList.remove("hidden");
    if (wasHidden) fitHeader();  // o atalho aparece depois do encaixe inicial: reencaixa o menu
    b.href = st.url;
    b.classList.toggle("on", !!st.connected);
    b.classList.toggle("off", !st.connected);
    b.title = st.connected ? "TerminalSec conectado — abrir o painel" : "TerminalSec sem conexão (painel fora do ar ou chave revogada) — abrir o painel";
    tsecState = st;
    tsecMobile();
  };
  check();
  if (!tsecTimer) tsecTimer = setInterval(check, 60000);
}

// overlay: o TerminalSec aberto por dentro do ISO Builder (clique normal). Ctrl/⌘+clique ou botão do meio
// continuam abrindo em nova aba. O iframe é criado na 1ª abertura e fica vivo: reabrir é instantâneo.
function tsecOpen(e) {
  if (e && (e.ctrlKey || e.metaKey || e.shiftKey || e.button === 1)) return;  // deixa o navegador abrir a aba
  if (!tsecState || !tsecState.url) return;
  if (e) e.preventDefault();
  setMenu(false);
  const ov = $("#tsecOv"), body = $("#tsecOvBody");
  $("#tsecOvNew").href = tsecState.url;
  $("#tsecOvDot").className = "tsec-ov-dot " + (tsecState.connected ? "on" : "off");
  $("#tsecOvDot").title = tsecState.connected ? "conectado" : "sem conexão";
  let fr = $("#tsecOvFrame");
  if (!fr || fr.dataset.src !== tsecState.url) {
    if (fr) fr.remove();
    $("#tsecOvMsg").classList.remove("hidden");
    fr = document.createElement("iframe");
    fr.id = "tsecOvFrame";
    fr.title = "TerminalSec";
    fr.dataset.src = tsecState.url;
    fr.allow = "clipboard-write; fullscreen";
    fr.referrerPolicy = "strict-origin-when-cross-origin";
    fr.onload = () => $("#tsecOvMsg").classList.add("hidden");
    fr.src = tsecState.url;
    body.appendChild(fr);
  }
  ov.classList.remove("hidden");
  document.body.classList.add("tsec-ov-open");
  $("#tsecOvClose").focus();
}
function tsecClose() {
  $("#tsecOv").classList.add("hidden");
  document.body.classList.remove("tsec-ov-open");
  const b = $("#tsecBtn"); if (b && !b.classList.contains("hidden")) b.focus();
}
$("#tsecBtn").addEventListener("click", tsecOpen);
$("#tsecOvClose").addEventListener("click", tsecClose);
$("#tsecOv").addEventListener("click", (e) => { if (e.target.id === "tsecOv") tsecClose(); });  // clicar no vidro fecha
addEventListener("keydown", (e) => { if (e.key === "Escape" && !$("#tsecOv").classList.contains("hidden")) tsecClose(); });
// Esc apertado com o foco dentro do TerminalSec: ele avisa por mensagem (só aceita da origem dele)
addEventListener("message", (e) => {
  if (!tsecState || !tsecState.url || $("#tsecOv").classList.contains("hidden")) return;
  let origin = "";
  try { origin = new URL(tsecState.url).origin; } catch (err) { return; }
  if (e.origin === origin && e.data && e.data.type === "terminalsec:escape") tsecClose();
});

// mesmo atalho no menu do celular (o header vira ☰ em telas estreitas)
function tsecMobile() {
  const m = $("#mTsec");
  if (!m) return;
  const st = tsecState;
  m.classList.toggle("hidden", !(st && st.configured && ME && ME.role === "admin"));
  if (!st || !st.configured) return;
  m.href = st.url;
  m.classList.toggle("on", !!st.connected);
  m.classList.toggle("off", !st.connected);
  m.textContent = (st.connected ? "🟢" : "🔴") + " TerminalSec" + (st.connected ? "" : " (sem conexão)") + " ↗";
}

/* -------------------------------------------------- layout */
function shell() {
  $("#top").classList.toggle("hidden", !ME);
  if (!ME) return;
  $("#whoEmail").textContent = ME.email + (ME.role === "admin" ? " · admin" : "");
  const rb = $("#remoteBtn"), ru = (ME.role === "admin" || ME.mode === "partner") ? FEATURES.remote_url : "";
  rb.classList.toggle("hidden", !ru);
  if (ru) {  // dentro do painel (se liberado) ou em nova aba
    rb.href = FEATURES.remote_embed ? "#/remoto" : ru;
    if (FEATURES.remote_embed) rb.removeAttribute("target"); else rb.target = "_blank";
  }
  tsecHeader();
  const dl = ME.mode === "download";
  const links = dl ? [[ME.build_id ? "#/iso/" + ME.build_id : "#/", "Minha ISO"]]
    : ME.mode === "partner" ? [["#/maquinas", "Máquinas"]] : [["#/", "Nova ISO"], ["#/isos", "Minhas ISOs"]];
  if (ME.role === "admin") links.push(["#/maquinas", "Máquinas"], ["#/acessos", "Acessos"], ["#/sistema", "Sistema"]);
  const cur = curRoute();
  const isOn = h => cur === h || (!dl && h === "#/isos" && cur.startsWith("#/iso/"));
  $("#nav").innerHTML = links.map(([h, t]) => `<a href="${h}" class="${isOn(h) ? "on" : ""}">${t}</a>`).join("");
  $("#whoEmail").title = ME.email;
  $("#logout").title = "Sair de " + ME.email;
  // menu das telas estreitas (celular, janela pequena): tudo em uma lista, sem rolagem lateral
  $("#mnav").innerHTML = `<nav class="mnav-links">${links.map(([h, t]) => `<a href="${h}" class="${isOn(h) ? "on" : ""}">${t}</a>`).join("")}
    ${installEvt ? `<a href="#" class="mnav-remote" id="mInstall">Instalar app</a>` : ""}
    ${ru ? `<a href="${esc(rb.getAttribute("href"))}" ${FEATURES.remote_embed ? "" : 'target="_blank" rel="noopener noreferrer"'} class="mnav-remote">Acesso remoto${FEATURES.remote_embed ? "" : " ↗"}</a>` : ""}
    ${ME.role === "admin" && FEATURES.terminalsec ? `<a id="mTsec" class="mnav-remote hidden" target="_blank" rel="noopener noreferrer">TerminalSec ↗</a>` : ""}</nav>
    <a href="/sobre" class="mnav-about">Página inicial</a>
    <div class="mnav-foot"><span class="muted small">${esc(ME.email)}${ME.role === "admin" ? " · admin" : ""}</span><button class="btn sm" id="mLogout">Sair</button></div>`;
  $("#mLogout").onclick = () => $("#logout").click();
  const mi = $("#mInstall"); if (mi) mi.onclick = e => { e.preventDefault(); setMenu(false); installApp(); };
  $$("#mnav a").forEach(a => a.onclick = () => setMenu(false));
  const mt = $("#mTsec"); if (mt) mt.onclick = tsecOpen;
  tsecMobile();
  setMenu(false);
  fitHeader();
}
// Menu do topo: se os itens não couberem, vai enxugando em degraus
// (esconde o e-mail → o texto "Remoto" → usa o botão ☰) em vez de cortar o último item.
const FIT_STEPS = ["fit-noemail", "fit-nolabel", "compact"];
function fitHeader() {
  const top = $("#top"), nav = $("#nav");
  if (!top || top.classList.contains("hidden")) return;
  const was = top.classList.contains("compact");
  top.classList.remove(...FIT_STEPS);
  if (innerWidth > 900) {
    for (const step of FIT_STEPS) {
      if (nav.scrollWidth <= nav.clientWidth + 1) break;
      top.classList.add(step);
    }
  }
  if (was && !top.classList.contains("compact")) setMenu(false);
}
addEventListener("resize", () => requestAnimationFrame(fitHeader));
if (document.fonts) document.fonts.ready.then(fitHeader);
function setMenu(open) {
  $("#mnav").classList.toggle("hidden", !open);
  $("#mnavBg").classList.toggle("hidden", !open);
  $("#menuBtn").setAttribute("aria-expanded", open ? "true" : "false");
  document.body.classList.toggle("menu-open", open);
}
$("#menuBtn").onclick = e => { e.stopPropagation(); setMenu($("#mnav").classList.contains("hidden")); };
// tocar fora fecha o menu SEM acionar o que está embaixo (o fundo recebe o toque)
$("#mnavBg").onclick = () => setMenu(false);
document.addEventListener("keydown", e => { if (e.key === "Escape") setMenu(false); });
// Sair volta para a página inicial pública (sem sessão, "/" é a landing)
$("#logout").onclick = async () => { try { await api("/api/logout", { method: "POST" }); } catch (e) { } ME = null; route.featLoaded = false; location.href = "/"; };

async function route() {
  clearInterval(timer); timer = null;
  if (typeof closeOverlay === "function") closeOverlay();
  if (!ME) {
    try { const r = await api("/api/me"); ME = r.user; FEATURES = r.features || FEATURES; route.featLoaded = true; } catch (e) { ME = null; }
  }
  if (ME && !route.featLoaded) {  // login recém-feito: carrega os recursos do painel
    try { FEATURES = (await api("/api/me")).features || FEATURES; route.featLoaded = true; } catch (e) { }
  }
  if (FEATURES.wireguard && !route.wgDefault) { route.wgDefault = true; W.wireguard = true; }
  const h = curRoute();
  if (!ME && h !== "#/login") return go("#/login");
  shell();
  refreshJobs();
  window.scrollTo(0, 0);
  if (h === "#/login") return ME ? go("#/") : renderLogin();
  if (ME.role === "admin" && !route.domainLoaded) { route.domainLoaded = true; await loadGuestDomain(); }
  if (ME.mode === "download") {  // acesso "somente download": só a ISO vinculada
    try { ME = (await api("/api/me")).user; } catch (e) { return; }  // o admin pode ter trocado a ISO
    if (!ME.build_id) { view.innerHTML = `<div class="card narrow"><h1>Nenhuma ISO vinculada</h1><p class="muted">Peça ao administrador para vincular a ISO a este acesso.</p></div>`; return; }
    if (h !== "#/iso/" + ME.build_id) return go("#/iso/" + ME.build_id);
    return renderBuild(ME.build_id);
  }
  if (ME.mode === "partner") {  // sócio: só as máquinas liberadas (e o Remoto delas)
    if (h === "#/remoto" && FEATURES.remote_url) return renderRemote();
    if (h !== "#/maquinas") return go("#/maquinas");
    return renderMachines();
  }
  if (h === "#/isos") return renderBuilds();
  if (h.startsWith("#/iso/")) return renderBuild(h.slice(6));
  if (h === "#/acessos" && ME.role === "admin") return renderUsers();
  if (h === "#/sistema" && ME.role === "admin") return renderSystem();
  if (h === "#/remoto" && ME.role === "admin" && FEATURES.remote_url) return renderRemote();
  if (h === "#/maquinas" && ME.role === "admin") return renderMachines();
  return renderWizard();
}
window.addEventListener("popstate", route);
window.addEventListener("hashchange", () => { legacyHash(); route(); });

/* -------------------------------------------------- login */
function renderLogin() {
  view.innerHTML = `
  <div class="login-wrap"><div class="card narrow u-w100">
    <div class="center"><div class="logo u-logo-lg">◎</div>
    <h1>ISO Builder</h1><p class="muted">Instalação automática do Ubuntu Server e do Windows 11</p></div>
    <form id="lf">
      <label class="f"><span>Usuário</span>
        <div class="suffix" id="lsuf"><input name="email" type="text" inputmode="email" autocapitalize="off" autocorrect="off" spellcheck="false" autocomplete="username" required placeholder="seu-prefixo"><span id="ldom">@${esc(GUEST_DOMAIN)}</span></div>
        <small id="lhelp">Digite só o prefixo. O administrador pode digitar o e-mail completo.</small></label>
      <label class="f"><span>Senha</span><input name="password" type="password" autocomplete="current-password" required></label>
      <div id="lerr"></div>
      <button class="btn primary big" type="submit">Entrar</button>
    </form>
    <p class="muted small center u-mt14">Acesso somente por convite do administrador.</p>
    <p class="small center u-mt8"><a href="/">← Página inicial</a></p>
  </div></div>`;
  // Só o prefixo; com "@" digitado (ex.: conta do administrador), vale o e-mail completo.
  const em = $('#lf input[name="email"]');
  const sync = () => $("#lsuf").classList.toggle("full", em.value.includes("@"));
  em.oninput = sync;
  api("/api/login-info").then(d => { if (d.guest_domain) { GUEST_DOMAIN = d.guest_domain; $("#ldom").textContent = "@" + GUEST_DOMAIN; } }).catch(() => { });
  $("#lf").onsubmit = async ev => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    const raw = String(f.get("email") || "").trim().toLowerCase();
    const email = raw.includes("@") ? raw : `${raw}@${GUEST_DOMAIN}`;
    try {
      ME = (await api("/api/login", { method: "POST", body: { email, password: f.get("password") } })).user;
      go("#/");
    } catch (e) { $("#lerr").innerHTML = `<div class="note err">${esc(e.message)}</div>`; }
  };
}


/* -------------------------------------------------- assistente */
const W = {
  sys: "ubuntu:24.04", os: "ubuntu",  // 1º passo: qual sistema (define as etapas seguintes)
  series: "24.04", hostname: "", username: "", realname: "", password: "", password2: "",
  timezone: "America/Sao_Paulo", locale: "pt_BR.UTF-8", keyboard: "br", keyboard_variant: "",
  net_mode: "dhcp", net_match_type: "mac", net_match_value: "", net_address: "", net_gateway: "", net_dns: "1.1.1.1,8.8.8.8", net_search: "",
  storage_type: "single", storage_value: "", storage_min_gb: 20, storage_layout: "lvm", storage_allow_usb: false,
  ssh_auth: "key", ssh_port: "22", ssh_keys: "", tailscale: true, wireguard: false, tailscale_hostname: "", tailscale_extra: "", tailscale_key: "",
  docker: true, apps: "coolify_cockpit", coolify_email: "", coolify_password: "", coolify_password2: "",
  ddns: false, ddns_hostname: "", ddns_login: "", ddns_password: "", cloudflare: false, cloudflare_token: "",
  gpu: true, virt: false, virt_bridge: true, tweaks: true, remote_agent: true, terminalsec: true,
  extra_packages: "", confirm_erase: "", panel_access: true,
  deliver_on: false, deliver_prefix: "", deliver_duration: "7d", deliver_label: "", deliver_agent: true,
  // Windows 11 Pro (só a configuração; a ISO vem da Microsoft pelo gravador)
  win_key: "", win_timezone: "E. South America Standard Time", win_keyboard: "0416:00010416", win_apps: "",
  win_tailscale: false  // no Windows o Tailscale começa desligado (precisa de auth key)
};
let step = 0;
let GUEST_DOMAIN = "gvtserver.online";
async function loadGuestDomain() { try { GUEST_DOMAIN = (await api("/api/admin/guest-domain")).domain; } catch (e) { } }
let autoSwitched = false;  // chave deixada em branco: mudou para usuário e senha
// Etapas de cada sistema: [chave, título]. O 1º passo (qual sistema) é comum.
const FLOWS = {
  ubuntu: [["os", "Sistema"], ["srv", "Servidor"], ["acc", "Acesso"], ["net", "Rede"], ["disk", "Disco"], ["remote", "Acesso remoto"], ["apps", "Programas"], ["review", "Revisar"]],
  windows: [["os", "Sistema"], ["wid", "Identificação"], ["wreg", "Região"], ["wdisk", "Disco"], ["wacc", "Acesso"], ["wapps", "Programas"], ["wrev", "Revisar"]],
};
const flow = () => FLOWS[W.os] || FLOWS.ubuntu;
const stepKey = () => flow()[step][0];
const isWin = () => W.os === "windows";
let FEATURES = { wireguard: false, remote_url: "", remote_embed: false };
// 1º passo em dois níveis: família (Linux / Windows) e, dentro dela, a versão.
// famílias: [chave, logo, nome, descrição]; sistemas: [valor, logo, nome, descrição]
const FAMILIES = [["linux", "Linux", "Linux", "Servidores Ubuntu: Docker, Coolify, Cockpit, VPN e acesso pelo painel"],
  ["windows", "Windows", "Windows", "PCs de trabalho: Windows oficial da Microsoft, com programas e acesso remoto"]];
const famOf = v => String(v).startsWith("windows") ? "windows" : "linux";
const SYSTEMS = [["ubuntu:24.04", "Ubuntu", "Ubuntu Server 24.04 LTS", "Mais madura — recomendada para servidores"],
  ["ubuntu:26.04", "Ubuntu", "Ubuntu Server 26.04 LTS", "Mais nova"],
  ["windows:11", "Windows", "Windows 11 Pro", "Para PCs: o gravador baixa o Windows oficial da Microsoft e aplica a configuração"]];
function applySys() {
  const [os, ver] = String(W.sys).split(":");
  if (os === "windows" && !FEATURES.windows) { W.sys = "ubuntu:" + W.series; return applySys(); }
  W.os = os === "windows" ? "windows" : "ubuntu";
  if (W.os === "ubuntu") W.series = ver === "26.04" ? "26.04" : "24.04";
}
const sysName = b => b.os === "windows" ? `Windows ${b.series} Pro` : `Ubuntu ${b.series}`;
const sysLogo = (b, cls = "xs") => osLogo(b.os === "windows" ? "Windows" : "Ubuntu", cls);
// Catálogo de apps do Windows e opções de região (vêm do painel: windows/apps.json)
let WINCAT = null;
async function loadWinCat() {
  if (WINCAT || loadWinCat.busy) return;
  loadWinCat.busy = true;
  try { WINCAT = await api("/api/windows/apps"); } catch (e) { WINCAT = { available: false, apps: [], timezones: {}, keyboards: {}, error: e.message }; }
  loadWinCat.busy = false;
  if ($("#wf") && ["wreg", "wapps", "wrev"].includes(stepKey())) { collect(); renderWizard(); }
}

const field = (name, label, attrs = "", help = "") =>
  `<label class="f"><span>${label}</span><input name="${name}" value="${esc(W[name])}" ${attrs}>${help ? `<small>${help}</small>` : ""}</label>`;
const radio = (name, opts) => `<div class="choice">${opts.map(([v, t, d]) =>
  `<label><input type="radio" name="${name}" value="${v}" ${String(W[name]) === v ? "checked" : ""}><span><b>${t}</b>${d ? `<span class="muted small">${d}</span>` : ""}</span></label>`).join("")}</div>`;
const toggle = (name, text, desc) =>
  `<label class="switch"><input type="checkbox" name="${name}" ${W[name] ? "checked" : ""}><span><b>${text}</b><br><span class="muted small">${desc}</span></span></label>`;

function stepHTML() {
  switch (stepKey()) {
    case "os": return `<h2>Qual sistema?</h2>
      <p class="muted">Define as próximas perguntas e o que o gravador coloca no pendrive.</p>
      <div class="syspick sysfam">${FAMILIES.map(([f, logo, name, desc]) => {
        const off = f === "windows" && !FEATURES.windows;
        return `<label class="syscard ${off ? "off" : ""}"><input type="radio" name="fam" value="${f}" ${famOf(W.sys) === f ? "checked" : ""} ${off ? "disabled" : ""}>
          ${osLogo(logo, "lg")}<span class="systxt"><b>${esc(name)}</b><small>${off ? "Gerador do Windows indisponível neste painel" : esc(desc)}</small></span></label>`;
      }).join("")}</div>
      <h3>Versão</h3>
      <div class="syspick">${SYSTEMS.filter(([v]) => famOf(v) === famOf(W.sys)).map(([v, logo, name, desc]) => {
        const off = v.startsWith("windows") && !FEATURES.windows;
        return `<label class="syscard ${off ? "off" : ""}"><input type="radio" name="sys" value="${v}" ${W.sys === v ? "checked" : ""} ${off ? "disabled" : ""}>
          ${osLogo(logo, "lg")}<span class="systxt"><b>${esc(name)}</b><small>${off ? "Gerador do Windows indisponível neste painel" : esc(desc)}</small></span></label>`;
      }).join("")}</div>`;
    case "srv": return `<h2>Servidor</h2>
      <p class="muted">Ubuntu Server ${esc(W.series)} LTS.</p>
      <div class="grid">${field("hostname", "Nome do servidor (hostname)", 'placeholder="ex.: srv-arquivos-01" autocapitalize="off" required', "Minúsculas, números e hífen.")}
      ${field("username", "Usuário administrador", 'placeholder="ex.: operador" autocapitalize="off" required')}</div>
      ${field("realname", "Nome completo (opcional)", 'placeholder="ex.: Equipe de TI"')}
      <div class="grid">${field("timezone", "Fuso horário")}${field("locale", "Idioma (locale)")}${field("keyboard", "Teclado")}</div>`;
    case "acc": {
      const pwMode = W.ssh_auth === "password";
      return `<h2>Acesso</h2>
      ${radio("ssh_auth", [
        ["key", "Chave SSH", "Mais seguro (recomendado). Você entra sem digitar senha, usando uma chave do seu computador."],
        ["password", "Usuário e senha", "Mais simples: ssh usuario@servidor e digita a senha. Para comandos de administrador, use sudo com a mesma senha."]])}
      <div class="note warn ${pwMode ? "" : "hidden"}" id="pwnote"><b>Proteções do modo senha:</b> a senha só é aceita pela <b>rede local</b> e pelo <b>Tailscale</b> (de IPs públicos da internet, só chave), login direto como <code>root</code> fica bloqueado e o <b>fail2ban</b> bloqueia quem errar a senha várias vezes. Use uma senha forte (mínimo 12 caracteres).</div>
      <div class="grid">
        <label class="f"><span>Senha do servidor</span><input type="password" name="password" value="${esc(W.password)}" autocomplete="new-password"><small id="pwhelp">${pwMode ? "Mínimo 12 caracteres. Usada no SSH, no sudo e no console." : "Mínimo 10 caracteres. Usada no sudo e no console físico."} Não fica salva: só o hash vai na ISO.</small></label>
        <label class="f"><span>Repita a senha</span><input type="password" name="password2" value="${esc(W.password2)}" autocomplete="new-password"></label>
      </div>
      <label class="f"><span>Porta do SSH</span><input name="ssh_port" type="number" min="1" max="65535" inputmode="numeric" value="${esc(W.ssh_port)}" class="u-w-port">
      <small>Deixe <b>22</b> (padrão) ou escolha outra entre 1024 e 65535, ex.: 2222. Com outra porta, conecte com <code>ssh -p PORTA usuario@servidor</code>.</small></label>
      <label class="f"><span>Chave(s) SSH pública(s) <span class="muted small" id="keyopt">${pwMode ? "(opcional)" : "(opcional: em branco = usuário e senha)"}</span></span><textarea name="ssh_keys" placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG7kQv2m... notebook-suporte">${esc(W.ssh_keys)}</textarea>
      <small>Uma por linha. Arquivo <code>id_ed25519.pub</code> (nunca a chave sem .pub).</small></label>
      <div class="row"><label class="btn sm">Carregar arquivo .pub<input type="file" id="pubfile" accept=".pub,text/plain" class="hidden"></label></div>
      <details><summary>Não tenho chave SSH — como criar?</summary>
        <p class="small">No <b>Windows</b> (PowerShell) ou <b>Mac</b> (Terminal):</p>
        <pre class="log">ssh-keygen -t ed25519 -C "notebook-suporte"</pre>
        <p class="small">Aperte Enter nas perguntas. Depois carregue o arquivo <code>id_ed25519.pub</code> da pasta <code>.ssh</code> da sua pasta de usuário (no Mac: <code>cat ~/.ssh/id_ed25519.pub</code>; no Windows: <code>type $env:USERPROFILE\\.ssh\\id_ed25519.pub</code>) e cole acima.</p>
      </details>`;
    }
    case "net": return `<h2>Rede</h2>
      <p class="muted">Use cabo de rede. Wi-Fi não é configurado.</p>
      ${radio("net_mode", [["dhcp", "DHCP (automático)", "Recomendado: o roteador entrega o IP"], ["static", "IP fixo", "Você informa IP, gateway e DNS"]])}
      <div id="static" class="${W.net_mode === "static" ? "" : "hidden"}">
        ${radio("net_match_type", [["mac", "Pelo endereço MAC da placa", "Mais seguro"], ["name", "Pelo nome da interface", "ex.: enp3s0"], ["glob", "Primeira placa de rede", "Só se houver apenas uma"]])}
        <div class="grid">${field("net_match_value", "MAC ou nome da interface", 'placeholder="ex.: 3c:52:82:a1:4f:09 ou enp2s0"')}
        ${field("net_address", "IP/prefixo", 'placeholder="ex.: 10.20.30.40/24"')}${field("net_gateway", "Gateway", 'placeholder="ex.: 10.20.30.1"')}
        ${field("net_dns", "DNS (vírgula)")}${field("net_search", "Domínio de busca (opcional)", 'placeholder="ex.: escritorio.local"')}</div>
      </div>`;
    case "disk": return `<h2>Disco de instalação</h2>
      <div class="note warn"><b>O disco escolhido será totalmente apagado</b> no computador onde o pendrive for usado.</div>
      ${radio("storage_type", [
        ["single", "Único disco interno", "Não precisa saber nada do disco. Se a máquina tiver mais de um disco interno, a instalação para sem apagar nada e mostra a lista."],
        ["serial", "Número de série", "O mais preciso, se você souber (lsblk mostra na coluna SERIAL)"],
        ["path", "Caminho do dispositivo", "ex.: /dev/nvme0n1"],
        ["model", "Modelo do disco", "ex.: Samsung_SSD_980*"]])}
      <div id="sval" class="${W.storage_type === "single" ? "hidden" : ""}">${field("storage_value", "Valor", 'autocapitalize="off" placeholder="ex.: S5GXNF0R912345K, /dev/nvme0n1 ou Samsung_SSD_980*"')}
        <label class="switch"><input type="checkbox" name="storage_allow_usb" ${W.storage_allow_usb ? "checked" : ""}><span>O disco de destino é externo/USB (normalmente não)</span></label></div>
      <div class="grid">${field("storage_min_gb", "Tamanho mínimo esperado (GB)", 'type="number" min="8"', "Proteção: aborta se o disco for menor.")}
      <label class="f"><span>Particionamento</span><select name="storage_layout"><option value="lvm" ${W.storage_layout === "lvm" ? "selected" : ""}>LVM usando o disco inteiro (recomendado)</option><option value="direct" ${W.storage_layout === "direct" ? "selected" : ""}>ext4 simples</option></select></label></div>`;
    case "remote": return `<h2>Acesso remoto</h2>
      <p class="muted">Como você vai alcançar este servidor de longe. Os dois juntos = um é reserva do outro (recomendado para máquinas distantes).</p>
      ${FEATURES.wireguard ? toggle("wireguard", "WireGuard pelo seu servidor", "O servidor conecta para fora até o seu servidor (funciona atrás de qualquer roteador/CGNAT do cliente). Em Máquinas você gera o QR code para o seu Mac/celular e acessa a máquina, e opcionalmente a rede local do cliente.")
        : `<div class="note">WireGuard pelo seu servidor: desligado neste painel (defina <code>HUB_ENDPOINT</code> no Coolify).</div>`}
      ${toggle("tailscale", "Tailscale", "Acesso remoto pela sua Tailnet, sem depender do seu servidor estar no ar.")}
      <div id="ts" class="${W.tailscale ? "" : "hidden"}">
        <label class="f"><span>Auth key</span><input type="password" name="tailscale_key" value="${esc(W.tailscale_key)}" placeholder="tskey-auth-…" autocomplete="off">
        <small>Gere em <a href="https://login.tailscale.com/admin/settings/keys" target="_blank" rel="noopener">login.tailscale.com → Settings → Keys</a>: <b>Reusable desligado</b>, <b>Ephemeral desligado</b>, <b>Pre-approved ligado</b>, tag <code>tag:server</code> (recomendado). A chave vai dentro da ISO e é apagada do servidor após o uso.</small></label>
        <div class="grid">${field("tailscale_hostname", "Nome na Tailnet", `placeholder="${esc(W.hostname || "igual ao hostname")}"`)}${field("tailscale_extra", "Argumentos extras (opcional)", 'placeholder="--advertise-tags=tag:server"')}</div>
      </div>`;
    case "apps": return `<h2>Programas</h2>
      ${radio("apps", [
        ["none", "Nenhum programa extra", "Só o sistema, SSH, Tailscale e os pacotes básicos."],
        ["docker", "Docker + Docker Compose", "Repositório oficial da Docker, habilitado no boot, usuário no grupo docker."],
        ["coolify", "Coolify", "Painel para publicar apps e bancos (inclui Docker). Acesso em http://IP:8000."],
        ["coolify_cockpit", "Coolify + Cockpit completo", "Coolify e o Cockpit com todos os módulos (VMs, arquivos, rede, armazenamento, Podman, atualizações). Cockpit em https://IP:9090."]])}
      <div id="coolifyBox" class="${W.apps.startsWith("coolify") ? "" : "hidden"}">
        <div class="note">O admin do Coolify é criado já na instalação e o cadastro público é fechado (sem isso, o primeiro a abrir a página vira admin). Use um e-mail de <b>domínio real</b> (o Coolify confere o DNS) e uma senha que não esteja em vazamentos conhecidos. A senha vai na ISO e é apagada do servidor depois de usada.</div>
        <div class="grid">${field("coolify_email", "E-mail do admin do Coolify", 'type="email" placeholder="ex.: ti@empresa-exemplo.com"')}
          <label class="f"><span>Senha do admin do Coolify</span><input type="password" name="coolify_password" value="${esc(W.coolify_password)}" autocomplete="new-password"><small>12+ caracteres, com maiúscula, minúscula, número e símbolo, e que não apareça em vazamentos de dados (o Coolify recusa essas).</small></label>
          <label class="f"><span>Repita a senha</span><input type="password" name="coolify_password2" value="${esc(W.coolify_password2)}" autocomplete="new-password"></label></div>
      </div>
      ${toggle("panel_access", "Acesso pelo painel (terminal no navegador)", "O servidor aparece em “Máquinas” e o painel pode abrir um terminal SSH nele, com uma chave própria aceita só pela Tailnet. Sem isso, ele aparece na lista mas o acesso é só do seu computador.")}
      <p class="muted small">Sempre instalados: curl, wget, git, ca-certificates, openssh-server, htop, unzip, rsync.</p>
      ${field("extra_packages", "Pacotes extras (separados por espaço)", 'placeholder="ex.: vim tmux ncdu" autocapitalize="off" spellcheck="false"')}
      ${pkgCatalog()}
      <h3>Extras</h3>
      ${toggle("ddns", "DDNS No-IP", "Mantém um nome (ex.: meuservidor.ddns.net) apontando para o IP público do servidor, atualizado a cada 5 min.")}
      <div id="ddnsBox" class="grid ${W.ddns ? "" : "hidden"}">${field("ddns_hostname", "Hostname no No-IP", 'placeholder="ex.: meuservidor.ddns.net" autocapitalize="off"')}
        ${field("ddns_login", "Usuário (ou e-mail) do No-IP", 'autocapitalize="off" autocomplete="off"')}
        <label class="f"><span>Senha do No-IP</span><input type="password" name="ddns_password" value="${esc(W.ddns_password)}" autocomplete="off"><small>Vai na ISO e fica só no arquivo protegido do ddclient no servidor.</small></label></div>
      ${toggle("cloudflare", "Cloudflare Tunnel", "Publica serviços pela Cloudflare sem abrir portas no roteador. Os domínios você define no painel da Cloudflare.")}
      <div id="cfBox" class="${W.cloudflare ? "" : "hidden"}"><label class="f"><span>Token do túnel</span><input type="password" name="cloudflare_token" value="${esc(W.cloudflare_token)}" autocomplete="off">
        <small>Cloudflare Zero Trust → Networks → Tunnels → crie o túnel (tipo Cloudflared) e cole o token (o texto longo que começa com <code>eyJ</code>; pode colar o comando inteiro). Fica protegido no servidor.</small></label></div>
      ${toggle("gpu", "Placa de vídeo (qualquer marca)", "Descobre a placa na instalação e instala o que ela precisa: NVIDIA (driver + GPU no Docker/Coolify, reinicia uma vez sozinho), AMD e Intel (vídeo por hardware, Vulkan/OpenCL, /dev/dri liberado para os containers). Sem placa, não instala nada.")}
      ${toggle("virt", "Virtualização (KVM)", "libvirt/QEMU para rodar VMs" + (W.apps === "coolify_cockpit" ? ", gerenciadas pelo Cockpit (Máquinas virtuais)." : "."))}
      <div id="virtBox" class="${W.virt ? "" : "hidden"}">${toggle("virt_bridge", "Ponte de rede br0", "As VMs recebem IP do seu roteador, como os outros aparelhos da rede. Aplicada com proteção: se a rede não voltar, é desfeita sozinha.")}</div>
      ${FEATURES.remote_agent ? toggle("remote_agent", "Aparecer no Remoto", "Instala o agente do Remoto (MeshCentral) no primeiro boot: o servidor aparece sozinho em Acesso remoto, com tela, terminal e arquivos pelo navegador. É a 2ª rota, além de Máquinas.") : ""}
      ${FEATURES.terminalsec ? toggle("terminalsec", "Monitorar com TerminalSec", "Instala o agente de segurança no primeiro boot. O servidor aparece no TerminalSec aguardando a sua aprovação (nada entra sem você aceitar). Se o painel estiver fora do ar, tenta de novo sozinho.") : ""}
      ${toggle("tweaks", "Ajustes de servidor", "Verifica e repara o disco sozinho no boot (após queda de energia), console sem apagar a tela e, com virtualização, IOMMU ligado.")}`;
    case "review": {
      const disk = { single: "Único disco interno", serial: "Serial " + W.storage_value, path: W.storage_value, model: "Modelo " + W.storage_value }[W.storage_type];
      return `<h2>Revisar e gerar</h2>
      <div class="table-wrap"><table>
        <tr><th>Ubuntu</th><td>${esc(W.series)} LTS</td></tr>
        <tr><th>Servidor</th><td>${esc(W.hostname)} · usuário ${esc(W.username)}</td></tr>
        <tr><th>Região</th><td>${esc(W.timezone)} · ${esc(W.locale)} · teclado ${esc(W.keyboard)}</td></tr>
        <tr><th>Rede</th><td>${W.net_mode === "dhcp" ? "DHCP" : esc(W.net_address + " via " + W.net_gateway)}</td></tr>
        <tr><th>Disco</th><td>${esc(disk)} · mín. ${esc(W.storage_min_gb)} GB · ${esc(W.storage_layout)}</td></tr>
        <tr><th>SSH</th><td>Porta ${esc(String(W.ssh_port || 22))} · ${W.ssh_auth === "password"
          ? "Usuário e senha (só rede local e Tailscale; fail2ban)" + (W.ssh_keys.trim() ? ` + ${W.ssh_keys.split("\n").filter(l => l.trim()).length} chave(s)` : "")
          : W.ssh_keys.split("\n").filter(l => l.trim()).length + " chave(s); senha desativada no SSH"}</td></tr>
        <tr><th>Acesso remoto</th><td>${[W.wireguard ? "WireGuard pelo seu servidor" : "", W.tailscale ? "Tailscale (" + esc(W.tailscale_hostname || W.hostname) + ")" : ""].filter(Boolean).join(" + ") || "só rede local"}</td></tr>
        <tr><th>Extras</th><td>${[W.ddns ? "DDNS " + esc(W.ddns_hostname) : "", W.cloudflare ? "Cloudflare Tunnel" : "", W.gpu ? "Placa de vídeo" : "",
          W.virt ? "Virtualização" + (W.virt_bridge ? " + br0" : "") : "", W.tweaks ? "ajustes de servidor" : "", W.remote_agent && FEATURES.remote_agent ? "Remoto" : "", W.terminalsec && FEATURES.terminalsec ? "TerminalSec" : ""].filter(Boolean).join(" · ") || "nenhum"}</td></tr>
        <tr><th>Programas</th><td>${{ none: "Nenhum", docker: "Docker + Compose", coolify: "Coolify (com Docker)", coolify_cockpit: "Coolify + Cockpit completo" }[W.apps]}${W.extra_packages ? " · extras: " + esc(W.extra_packages) : ""}</td></tr>
      </table></div>
      ${deliverHTML("ISO")}
      <div class="note warn u-mt16"><b>Atenção:</b> ao dar boot com o pendrive, o disco de destino da máquina será <b>completamente apagado</b>.</div>
      <label class="f"><span>Digite APAGAR para confirmar</span><input name="confirm_erase" value="${esc(W.confirm_erase)}" autocomplete="off" placeholder="APAGAR"></label>
      <div id="werr"></div>`;
    }
    /* ---------- Windows 11 Pro */
    case "wid": return `<h2>Identificação</h2>
      <p class="muted">Windows 11 Pro, em português do Brasil. Sem conta Microsoft: o usuário abaixo é local e administrador.</p>
      <div class="grid">${field("hostname", "Nome do computador", 'placeholder="ex.: recepcao-01" maxlength="15" autocapitalize="off" spellcheck="false" required', "1 a 15 caracteres: minúsculas, números e hífen (limite do Windows).")}
      ${field("username", "Usuário local (administrador)", 'placeholder="ex.: suporte" maxlength="20" autocapitalize="off" spellcheck="false" required', "Minúsculas, números, _ ou -. Diferente do nome do computador.")}</div>
      ${field("realname", "Nome completo (opcional)", 'placeholder="ex.: Suporte de TI" maxlength="60"')}
      <div class="grid">
        <label class="f"><span>Senha do usuário</span><input type="password" name="password" value="${esc(W.password)}" autocomplete="new-password"><small>Mínimo 10 caracteres. Vai dentro da configuração do pendrive: guarde o pendrive com cuidado.</small></label>
        <label class="f"><span>Repita a senha</span><input type="password" name="password2" value="${esc(W.password2)}" autocomplete="new-password"></label>
      </div>
      <label class="f"><span>Chave do Windows <span class="muted small">(opcional)</span></span><input name="win_key" value="${esc(W.win_key)}" placeholder="XXXXX-XXXXX-XXXXX-XXXXX-XXXXX" maxlength="29" autocapitalize="characters" autocomplete="off" spellcheck="false" class="mono u-keyinput">
        <small>Em branco = instala o Windows 11 Pro <b>sem ativar</b> (dá para ativar depois em Configurações → Sistema → Ativação). Com chave, o Windows ativa sozinho quando tiver internet.</small></label>`;
    case "wreg": {
      if (!WINCAT) { loadWinCat(); return `<h2>Região</h2><p class="muted">Carregando…</p>`; }
      const opts = (obj, cur) => Object.entries(obj || {}).map(([v, t]) => `<option value="${esc(v)}" ${cur === v ? "selected" : ""}>${esc(t)}</option>`).join("");
      return `<h2>Região</h2>
      <p class="muted">Idioma, teclado e fuso já ficam definidos: nenhuma tela de configuração aparece na instalação.</p>
      <div class="grid">
        <label class="f"><span>Idioma do Windows</span><select disabled><option>Português (Brasil) — pt-BR</option></select><small>O gravador baixa o Windows 11 em português do Brasil.</small></label>
        <label class="f"><span>Teclado</span><select name="win_keyboard">${opts(WINCAT.keyboards, W.win_keyboard)}</select></label>
        <label class="f"><span>Fuso horário</span><select name="win_timezone">${opts(WINCAT.timezones, W.win_timezone)}</select></label>
      </div>`;
    }
    case "wdisk": return `<h2>Disco de instalação</h2>
      <div class="note warn"><b>O maior disco interno do computador será totalmente apagado</b> e o Windows instalado nele.</div>
      <ul class="plain">
        <li><b>Nunca o pendrive:</b> discos USB ficam de fora da escolha.</li>
        <li><b>Mais de um disco interno?</b> Vai no maior. Se não houver nenhum disco elegível, a instalação para com uma mensagem, sem apagar nada.</li>
        <li><b>PCs antigos:</b> as exigências de TPM 2.0, Secure Boot e memória do Windows 11 são contornadas. O computador precisa inicializar em modo UEFI.</li>
      </ul>`;
    case "wacc": return `<h2>Acesso</h2>
      <p class="muted">O computador aparece em “Máquinas” (com o logo do Windows) alguns minutos depois de instalado, e se reporta a cada 10 minutos. Para alcançá-lo de longe, deixe ao menos uma forma de acesso ligada (Remoto, WireGuard ou Tailscale); duas = uma é reserva da outra.</p>
      ${toggle("panel_access", "Terminal pelo painel (OpenSSH)", "Instala o OpenSSH Server do Windows (shell PowerShell) com a chave do painel: o terminal abre no navegador, sem senha. O painel entra pelo túnel (WireGuard ou Tailscale, abaixo) ou por um endereço definido em Máquinas → Configurações.")}
      ${FEATURES.remote_agent ? toggle("remote_agent", "Aparecer no Remoto (MeshCentral)", "Instala o agente do Remoto: tela, terminal e arquivos pelo navegador, de qualquer lugar, sem abrir portas no roteador. Recomendado.")
        : `<div class="note">Remoto (MeshCentral): indisponível neste painel agora; o computador aparece só em Máquinas.</div>`}
      ${FEATURES.wireguard ? toggle("wireguard", "WireGuard pelo seu servidor", "O PC conecta para fora até o seu servidor (funciona atrás de qualquer roteador/CGNAT do cliente), com um serviço do Windows que sobe no boot, antes de qualquer login. O terminal do painel passa pelo túnel e, em Máquinas, você gera o QR code para o seu Mac/celular alcançar o PC. (O acesso à rede local do cliente ainda não existe no Windows.)")
        : `<div class="note">WireGuard pelo seu servidor: desligado neste painel (defina <code>HUB_ENDPOINT</code> no Coolify).</div>`}
      ${toggle("win_tailscale", "Tailscale", "Acesso remoto pela sua Tailnet, sem depender do seu servidor estar no ar.")}
      <div id="ts" class="${W.win_tailscale ? "" : "hidden"}">
        <label class="f"><span>Auth key</span><input type="password" name="tailscale_key" value="${esc(W.tailscale_key)}" placeholder="tskey-auth-…" autocomplete="off">
        <small>Gere em <a href="https://login.tailscale.com/admin/settings/keys" target="_blank" rel="noopener">login.tailscale.com → Settings → Keys</a>: <b>Reusable desligado</b>, <b>Ephemeral desligado</b>, <b>Pre-approved ligado</b>, tag <code>tag:server</code> (recomendado). A chave vai dentro da configuração do pendrive e é apagada do PC após o uso.</small></label>
        ${field("tailscale_hostname", "Nome na Tailnet (opcional)", `placeholder="${esc(W.hostname || "igual ao nome do computador")}" autocapitalize="off" spellcheck="false"`)}
      </div>`;
    case "wapps": {
      if (!WINCAT) { loadWinCat(); return `<h2>Programas</h2><p class="muted">Carregando o catálogo…</p>`; }
      return `<h2>Programas</h2>
      <p class="muted">Marque o que instalar. Tudo vem do <b>winget</b> (fontes oficiais), sozinho, depois da instalação do Windows e com internet; um programa que falhar não trava os outros.</p>
      ${winCatalog()}
      <details class="u-mt12" ${W.win_apps.split(/\s+/).some(i => i && !WINCAT.apps.some(a => a.id === i)) ? "open" : ""}><summary>Avançado: outros programas (ID do winget)</summary>
        ${field("win_apps", "IDs do winget (separados por espaço)", 'placeholder="ex.: Mozilla.Firefox Notepad++.Notepad++" autocapitalize="off" spellcheck="false"', "Marcar acima escreve aqui. Procure IDs com <code>winget search nome</code> ou em winget.run.")}</details>`;
    }
    case "wrev": {
      const ids = W.win_apps.split(/\s+/).filter(Boolean), names = ids.map(i => ((WINCAT && WINCAT.apps.find(a => a.id === i)) || { name: i }).name);
      const key = W.win_key.replace(/[^A-Z0-9]/gi, "");
      return `<h2>Revisar e gerar</h2>
      <div class="table-wrap"><table>
        <tr><th>Sistema</th><td>${osLogo("Windows", "xs")} Windows 11 Pro · pt-BR · 64 bits</td></tr>
        <tr><th>Computador</th><td>${esc(W.hostname)} · usuário local ${esc(W.username)}</td></tr>
        <tr><th>Ativação</th><td>${key ? `chave informada (termina em <span class="mono">${esc(key.slice(-5).toUpperCase())}</span>): ativa sozinho depois da instalação, com internet — o resultado aparece em Máquinas` : "sem chave: instala sem ativar"}</td></tr>
        <tr><th>Região</th><td>${esc((WINCAT && WINCAT.timezones[W.win_timezone]) || W.win_timezone)} · teclado ${esc((WINCAT && WINCAT.keyboards[W.win_keyboard]) || W.win_keyboard)}</td></tr>
        <tr><th>Disco</th><td>maior disco interno (nunca USB)</td></tr>
        <tr><th>Acesso</th><td>${[W.panel_access ? "terminal pelo painel (OpenSSH)" : "", W.remote_agent && FEATURES.remote_agent ? "Remoto" : "",
          W.wireguard && FEATURES.wireguard ? "WireGuard pelo seu servidor" : "", W.win_tailscale ? "Tailscale (" + esc(W.tailscale_hostname || W.hostname) + ")" : ""].filter(Boolean).join(" · ") || "só aparece em Máquinas"}</td></tr>
        <tr><th>Programas</th><td>${names.length ? esc(names.join(", ")) : "nenhum"}</td></tr>
      </table></div>
      <div class="note u-mt16">O painel gera só a <b>configuração</b> (alguns KB). O gravador isoBuilder baixa o Windows 11 oficial direto da Microsoft no computador de quem grava o pendrive e aplica esta configuração.</div>
      ${deliverHTML("instalação")}
      <div class="note warn u-mt16"><b>Atenção:</b> ao dar boot com o pendrive, o <b>maior disco interno</b> da máquina será <b>completamente apagado</b>.</div>
      <label class="f"><span>Digite APAGAR para confirmar</span><input name="confirm_erase" value="${esc(W.confirm_erase)}" autocomplete="off" placeholder="APAGAR"></label>`;
    }
  }
}

// "Entregar para uma pessoa" (só admin), no último passo dos dois sistemas
function deliverHTML(what) {
  if (ME.role !== "admin") return "";
  return `<div class="card u-mt16">
    <label class="switch"><input type="checkbox" name="deliver_on" ${W.deliver_on ? "checked" : ""}><span><b>Entregar esta ${what} para uma pessoa</b><br><span class="muted small">Cria agora um acesso temporário “somente download” já vinculado a ela: a pessoa entra, baixa o gravador e grava o pendrive. A máquina aparece em “Máquinas” com esse e-mail.</span></span></label>
    <div id="deliverBox" class="${W.deliver_on ? "" : "hidden"}">
      <div class="grid">
        <label class="f"><span>E-mail de acesso</span><div class="suffix"><input name="deliver_prefix" value="${esc(W.deliver_prefix)}" placeholder="ex.: filial-centro" autocapitalize="off" autocomplete="off"><span>@${esc(GUEST_DOMAIN)}</span></div><small>Vazio = prefixo aleatório. A senha é gerada automaticamente.</small></label>
        <label class="f"><span>Validade do acesso</span><select name="deliver_duration">${[["24h", "24 horas"], ["3d", "3 dias"], ["7d", "7 dias"], ["30d", "30 dias"]].map(([v, t]) => `<option value="${v}" ${W.deliver_duration === v ? "selected" : ""}>${t}</option>`).join("")}</select></label>
        ${field("deliver_label", "Para quem (opcional)", 'placeholder="ex.: Suporte da filial Centro"')}
      </div>
      ${agentOK() ? toggle("deliver_agent", "Instalar acesso remoto junto", "O gravador do cliente oferece instalar o agente do Remoto (a pessoa precisa permitir na tela). Assim você acessa o PC dela para gravar o pendrive e formatar.") : ""}</div></div>`;
}

/* -------------------------------------------------- catálogo de pacotes (estilo Ninite)
   Nomes conferidos nos repositórios do Ubuntu 24.04 e 26.04. Marcar escreve no campo
   "Pacotes extras"; digitar no campo marca/desmarca. [pacote, descrição, aviso?] */
const PKG_CATALOG = [
  ["Monitoramento", [["btop", "monitor de CPU, memória, disco e rede"], ["glances", "visão geral do sistema"], ["iotop", "uso de disco por processo"],
    ["iftop", "uso de rede por conexão"], ["nload", "tráfego de rede em tempo real"], ["ncdu", "o que está ocupando o disco"],
    ["sysstat", "histórico de CPU e disco (sar)"], ["lm-sensors", "temperaturas"], ["smartmontools", "saúde dos discos"], ["nvtop", "uso da GPU"]]],
  ["Rede", [["net-tools", "ifconfig e netstat"], ["nmap", "varredura de rede"], ["tcpdump", "captura de pacotes"], ["mtr-tiny", "traceroute contínuo"],
    ["iperf3", "velocidade na rede local"], ["traceroute", "rota até um destino"], ["dnsutils", "consultar e testar DNS"], ["whois", "dono de domínios e IPs"],
    ["ethtool", "detalhes da placa de rede"], ["speedtest-cli", "velocidade da internet"]]],
  ["Terminal e edição", [["vim", "editor de texto avançado"], ["micro", "editor simples, com mouse"], ["tmux", "sessões que continuam após desconectar"],
    ["screen", "sessões no terminal"], ["bash-completion", "completar comandos com Tab"], ["tree", "pastas em árvore"], ["jq", "ler JSON no terminal"]]],
  ["Arquivos e compactação", [["zip", "criar .zip"], ["p7zip-full", "compactar e extrair .7z"], ["rclone", "sincronizar com nuvens (Drive, S3, OneDrive)"], ["pv", "progresso em cópias grandes"]]],
  ["Discos e compartilhamento", [["nfs-common", "montar pastas de outro servidor Linux"], ["cifs-utils", "montar pastas compartilhadas (SMB)"], ["samba", "compartilhar pastas com Windows"],
    ["ntfs-3g", "ler e gravar discos do Windows"], ["exfatprogs", "pendrives e HDs exFAT"], ["nvme-cli", "saúde e ajustes de SSDs NVMe"], ["zfsutils-linux", "sistema de arquivos com snapshots"],
    ["mdadm", "RAID por software"], ["hdparm", "ajustes e testes de disco"]]],
  ["Segurança", [["ufw", "firewall simples (instala desligado)"], ["clamav", "antivírus"], ["lynis", "auditoria de segurança"],
    ["auditd", "registro de auditoria"], ["unattended-upgrades", "instala correções de segurança sozinho"]]],
  ["Servidores e linguagens", [["nginx", "servidor web", "coolify"], ["apache2", "servidor web", "coolify"], ["mariadb-server", "banco de dados compatível com MySQL"],
    ["postgresql", "banco de dados"], ["redis-server", "cache e filas em memória"], ["python3-pip", "instalar bibliotecas Python"], ["nodejs", "rodar apps JavaScript"], ["openjdk-21-jre-headless", "rodar programas Java"]]],
  ["Backup", [["restic", "backup com criptografia"], ["borgbackup", "backup com deduplicação"]]],
  ["Hardware", [["pciutils", "placas instaladas no PC"], ["usbutils", "aparelhos USB conectados"], ["dmidecode", "placa-mãe e memórias"], ["lshw", "inventário de hardware"]]],
];
// nome amigável quando o do pacote não é o que as pessoas conhecem
const PKG_NAMES = { "p7zip-full": "7-Zip", "mariadb-server": "MariaDB", postgresql: "PostgreSQL", "redis-server": "Redis",
  "python3-pip": "Python (pip)", nodejs: "Node.js", "openjdk-21-jre-headless": "Java 21", apache2: "Apache", nginx: "NGINX",
  "zfsutils-linux": "ZFS", clamav: "ClamAV", "speedtest-cli": "Speedtest", "bash-completion": "Bash completion", "lm-sensors": "Sensores",
  smartmontools: "SMART", "unattended-upgrades": "Atualizações automáticas", "nfs-common": "NFS", "cifs-utils": "Pastas do Windows",
  samba: "Samba", "ntfs-3g": "NTFS", exfatprogs: "exFAT", "nvme-cli": "NVMe", dnsutils: "dig e nslookup", pciutils: "lspci", usbutils: "lsusb",
  borgbackup: "Borg", "mtr-tiny": "mtr", rclone: "Rclone", vim: "Vim", tmux: "tmux", jq: "jq", ufw: "UFW", lynis: "Lynis", nvtop: "nvtop" };
function pkgIcon(p, icons = window.PKG_ICONS, label = p) {
  const ic = (icons || {})[p];
  const svg = !ic ? "" : ic.t === "b" ? `<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">${ic.d}</svg>`
    : `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${ic.d}</svg>`;
  return `<span class="pkgico" style="--c:${ic ? ic.c : "#64748b"}">${svg || esc(String(label).slice(0, 2))}<i class="pkgchk" aria-hidden="true"><svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"><path d="M20 6 9 17l-5-5"/></svg></i></span>`;
}
function pkgCatalog() {
  return `<details class="pkgcat" id="pkgcat"><summary>Escolher da lista <span class="muted small" id="pkgcount"></span></summary>
    <p class="muted small">Marque o que quiser: o nome vai para o campo acima (dá para digitar outros). Instalados no primeiro boot; um nome que não existir nesta versão do Ubuntu é ignorado, sem travar a instalação.</p>
    ${PKG_CATALOG.map(([g, items]) => `<fieldset class="pkggrp"><legend>${esc(g)}</legend><div class="pkgitems">${items.map(([p, d, warn]) =>
      `<label class="pkgitem" title="${esc(p)}: ${esc(d)}"><input type="checkbox" data-pkg="${esc(p)}" ${warn ? `data-warn="${warn}"` : ""}>${pkgIcon(p)}
        <span class="pkgtxt"><b>${esc(PKG_NAMES[p] || p)}</b>${PKG_NAMES[p] && PKG_NAMES[p].toLowerCase() !== p ? `<code>${esc(p)}</code>` : ""}<small>${esc(d)}</small>${warn ? `<em class="pkgwarn">usa as portas 80/443, que são do Coolify</em>` : ""}</span></label>`).join("")}</div></fieldset>`).join("")}
  </details>`;
}
function syncPkgs() {
  const f = $('input[name="extra_packages"]'); if (!f) return;
  const set = new Set(f.value.split(/\s+/).filter(Boolean)), coolify = ($('input[name="apps"]:checked') || {}).value?.startsWith("coolify");
  let n = 0;
  $$("#pkgcat [data-pkg]").forEach(c => {
    c.checked = set.has(c.dataset.pkg); if (c.checked) n++;
    const it = c.closest(".pkgitem"); it.classList.toggle("on", c.checked); it.classList.toggle("warn", !!(c.dataset.warn && coolify));
  });
  const cnt = $("#pkgcount"); if (cnt) cnt.textContent = n ? `(${n} marcado${n > 1 ? "s" : ""})` : "";
}
function wirePkgCatalog() {
  const f = $('input[name="extra_packages"]'); if (!f || !$("#pkgcat")) return;
  $$("#pkgcat [data-pkg]").forEach(c => c.onchange = () => {
    const list = f.value.split(/\s+/).filter(Boolean).filter(p => p !== c.dataset.pkg);
    if (c.checked) list.push(c.dataset.pkg);
    f.value = list.join(" ");
    syncPkgs();
  });
  f.addEventListener("input", syncPkgs);
  syncPkgs();
  if (f.value.trim()) $("#pkgcat").open = true;
}

/* -------------------------------------------------- catálogo de apps do Windows (winget, estilo Ninite)
   Vem de windows/apps.json; ícones em window.WIN_ICONS (gerados por make-pkg-icons.py).
   Marcar escreve o ID no campo "IDs do winget" (avançado); digitar lá marca/desmarca. */
function winCatalog() {
  const apps = (WINCAT && WINCAT.apps) || [];
  if (!apps.length) return `<div class="note warn">${esc(WINCAT && WINCAT.error ? WINCAT.error : "Catálogo de programas do Windows indisponível neste painel.")} Dá para informar IDs do winget em “Avançado”.</div>`;
  const cats = [];
  apps.forEach(a => { const c = a.cat || "Outros"; let g = cats.find(x => x[0] === c); if (!g) cats.push(g = [c, []]); g[1].push(a); });
  return `<div id="wincat"><p class="muted small u-mt0" id="wincount"></p>${cats.map(([g, items]) => `<fieldset class="pkggrp"><legend>${esc(g)}</legend><div class="pkgitems">${items.map(a =>
    `<label class="pkgitem" title="${esc(a.id)}: ${esc(a.desc)}"><input type="checkbox" data-wapp="${esc(a.id)}">${pkgIcon(a.id, window.WIN_ICONS, a.name)}
      <span class="pkgtxt"><b>${esc(a.name)}</b><small>${esc(a.desc)}</small></span></label>`).join("")}</div></fieldset>`).join("")}</div>`;
}
function syncWinApps() {
  const f = $('input[name="win_apps"]'); if (!f) return;
  const set = new Set(f.value.split(/\s+/).filter(Boolean));
  let n = 0;
  $$("#wincat [data-wapp]").forEach(c => { c.checked = set.has(c.dataset.wapp); if (c.checked) n++; c.closest(".pkgitem").classList.toggle("on", c.checked); });
  const cnt = $("#wincount"); if (cnt) cnt.textContent = set.size ? `${set.size} programa${set.size > 1 ? "s" : ""} marcado${set.size > 1 ? "s" : ""}${set.size > n ? ` (${set.size - n} pelo ID)` : ""}.` : "Nenhum programa marcado.";
}
function wireWinCatalog() {
  const f = $('input[name="win_apps"]'); if (!f) return;
  $$("#wincat [data-wapp]").forEach(c => c.onchange = () => {
    const list = f.value.split(/\s+/).filter(Boolean).filter(i => i !== c.dataset.wapp);
    if (c.checked) list.push(c.dataset.wapp);
    f.value = list.join(" ");
    syncWinApps();
  });
  f.addEventListener("input", syncWinApps);
  syncWinApps();
}
// chave do Windows: maiúsculas e hífen a cada 5 caracteres enquanto digita
function wireWinKey() {
  const k = $('input[name="win_key"]'); if (!k) return;
  k.addEventListener("input", () => {
    const raw = k.value.toUpperCase().replace(/[^A-Z0-9]/g, "").slice(0, 25);
    k.value = raw.replace(/(.{5})(?=.)/g, "$1-");
  });
}

function collect() {
  $$("input,select,textarea", view).forEach(el => {
    if (!el.name || !(el.name in W)) return;
    if (el.type === "radio") { if (el.checked) W[el.name] = el.value; }
    else if (el.type === "checkbox") W[el.name] = el.checked;
    else W[el.name] = el.value.trim ? (el.type === "password" ? el.value : el.value.trim()) : el.value;
  });
}

function checkStep() {
  const e = [], k = stepKey();
  if (k === "wid") {
    if (!/^[a-z0-9]([a-z0-9-]{0,13}[a-z0-9])?$/.test(W.hostname) || /^\d+$/.test(W.hostname)) e.push("Nome do computador: 1 a 15 caracteres, minúsculas, números e hífen (não só números).");
    if (!/^[a-z_][a-z0-9_-]{0,19}$/.test(W.username)) e.push("Usuário: minúsculas, números, _ ou - (até 20), começando com letra.");
    else if (W.username === W.hostname) e.push("O usuário não pode ter o mesmo nome do computador.");
    if (W.password.length < 10) e.push("A senha precisa de ao menos 10 caracteres.");
    else if (W.password !== W.password2) e.push("As senhas não conferem.");
    const key = W.win_key.replace(/[^A-Z0-9]/gi, "");
    if (key && !/^[BCDFGHJKMNPQRTVWXY2346789]{25}$/i.test(key)) e.push("Chave do Windows: 25 caracteres (XXXXX-XXXXX-XXXXX-XXXXX-XXXXX) ou deixe em branco.");
  }
  if (k === "wacc" && W.win_tailscale && !/^tskey-/.test(W.tailscale_key.trim())) e.push("Cole a auth key do Tailscale (tskey-auth-…) ou desligue o Tailscale.");
  if (k === "wacc" && W.win_tailscale && W.tailscale_hostname && !/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(W.tailscale_hostname.toLowerCase())) e.push("Nome na Tailnet: minúsculas, números e hífen.");
  if (k === "wacc" && !e.length && !(W.remote_agent && FEATURES.remote_agent) && !(W.wireguard && FEATURES.wireguard) && !W.win_tailscale
    && !confirm("Sem Remoto, WireGuard e Tailscale, o PC só será alcançável pela rede local do cliente. Continuar?")) e.push("Escolha pelo menos uma forma de acesso (Remoto, WireGuard ou Tailscale).");
  if (k === "wapps" && W.win_apps.split(/\s+/).some(i => i && !/^[A-Za-z0-9][A-Za-z0-9.+_-]{0,99}$/.test(i))) e.push("ID do winget inválido em “Avançado”.");
  if (k === "wrev" && W.deliver_on && W.deliver_prefix && !/^[a-z0-9]([a-z0-9._-]{0,38}[a-z0-9])?$/.test(W.deliver_prefix)) e.push("Prefixo do e-mail: letras minúsculas, números, ponto, hífen ou _.");
  if (k === "srv") {
    if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(W.hostname)) e.push("Nome do servidor: use minúsculas, números e hífen.");
    if (!/^[a-z_][a-z0-9_-]{0,31}$/.test(W.username)) e.push("Usuário: minúsculas, números, _ ou -.");
  }
  if (k === "acc") {
    const hasKey = W.ssh_keys.split("\n").some(l => /^(ssh-|ecdsa-|sk-)/.test(l.trim()));
    if (W.ssh_auth === "key" && !hasKey && !/PRIVATE KEY|-----BEGIN/.test(W.ssh_keys)) {
      // Chave é opcional: sem chave, o acesso passa a ser por usuário e senha
      // (sem chave e sem senha no SSH não haveria como entrar no servidor).
      W.ssh_auth = "password";
      autoSwitched = true;
    }
    const minPw = W.ssh_auth === "password" ? 12 : 10;
    if (W.password.length < minPw) e.push(`A senha precisa de ao menos ${minPw} caracteres.`);
    if (W.password !== W.password2) e.push("As senhas não conferem.");
    if (/PRIVATE KEY|-----BEGIN/.test(W.ssh_keys)) e.push("Isso é uma chave PRIVADA. Use o arquivo .pub.");
    const sp = String(W.ssh_port).trim();
    if (!/^\d+$/.test(sp) || !(+sp === 22 || (+sp >= 1024 && +sp <= 65535 && ![6001, 6002, 8000, 8080, 9090].includes(+sp))))
      e.push("Porta SSH: use 22 (padrão) ou de 1024 a 65535 (exceto 6001, 6002, 8000, 8080 e 9090, usadas pelos programas).");

  }
  if (k === "net" && W.net_mode === "static" && (!W.net_address.includes("/") || !W.net_gateway)) e.push("Informe IP/prefixo e gateway.");
  if (k === "disk" && W.storage_type !== "single" && W.storage_value.length < 3) e.push("Informe o valor do critério do disco.");
  if (k === "remote" && W.tailscale && !/^tskey-/.test(W.tailscale_key.trim())) e.push("Cole a auth key do Tailscale (tskey-auth-…) ou desligue o Tailscale.");
  if (k === "remote" && !W.tailscale && !W.wireguard && !confirm("Sem WireGuard e sem Tailscale, o acesso será só pela rede local do cliente. Continuar?")) e.push("Escolha pelo menos uma forma de acesso remoto.");
  if (k === "apps" && W.ddns) {
    if (!/^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/.test(W.ddns_hostname.toLowerCase())) e.push("DDNS: informe o hostname do No-IP (ex.: meuservidor.ddns.net).");
    if (!W.ddns_login) e.push("DDNS: informe o usuário do No-IP.");
    if (!W.ddns_password || /['\\]/.test(W.ddns_password)) e.push("DDNS: informe a senha do No-IP (sem aspas simples nem barra invertida).");
  }
  if (k === "apps" && W.cloudflare && !/[A-Za-z0-9_=+\/-]{80,}/.test(W.cloudflare_token)) e.push("Cloudflare: cole o token do túnel.");
  if (k === "apps" && W.apps.startsWith("coolify")) {
    const p = W.coolify_password;
    if (!/^[^@\s]+@[^@\s]+\.[a-z]{2,}$/i.test(W.coolify_email)) e.push("Informe o e-mail do admin do Coolify.");
    if (p.length < 12 || !/[a-z]/.test(p) || !/[A-Z]/.test(p) || !/\d/.test(p) || !/[^A-Za-z0-9]/.test(p)) e.push("Senha do Coolify: 12+ caracteres com maiúscula, minúscula, número e símbolo.");
    else if (p !== W.coolify_password2) e.push("As senhas do Coolify não conferem.");
    if (p.includes("'")) e.push("A senha do Coolify não pode ter aspas simples.");
  }
  if (k === "review" && W.deliver_on && W.deliver_prefix && !/^[a-z0-9]([a-z0-9._-]{0,38}[a-z0-9])?$/.test(W.deliver_prefix)) e.push("Prefixo do e-mail: letras minúsculas, números, ponto, hífen ou _.");
  return e;
}

function renderWizard() {
  applySys();
  const STEPS = flow().map(x => x[1]);
  step = Math.min(step, STEPS.length - 1);
  const last = step === STEPS.length - 1, genTxt = isWin() ? "Gerar configuração" : "Gerar ISO";
  if (FEATURES.windows && !WINCAT) loadWinCat();  // catálogo do Windows pronto antes de chegar lá
  view.innerHTML = `
  <h1>Nova ISO</h1><p class="muted">${isWin()
    ? "Responda às perguntas; no final o painel gera a configuração e o gravador de pendrive baixa o Windows oficial da Microsoft e aplica."
    : "Responda às perguntas; no final o servidor gera a ISO e você baixa com o gravador de pendrive."}</p>
  ${step === 0 || INV ? `<div id="invBox">${inviteCard()}</div>` : ""}
  <div class="wizard">
    <div class="steps-m"><div class="spread"><b>${STEPS[step]}</b><span class="muted small">Passo ${step + 1} de ${STEPS.length}</span></div>
      <div class="bar"><i id="stepbar"></i></div></div>
    <ol class="steps">${STEPS.map((s, i) => `<li data-i="${i}" class="${i === step ? "on" : i < step ? "ok" : ""}"><span class="n">${i < step ? "✓" : i + 1}</span>${s}</li>`).join("")}</ol>
    <div class="card"><form id="wf" autocomplete="off">${stepHTML()}
      <div id="serr"></div>
      <div class="nav-btns"><button type="button" class="btn" id="back" ${step === 0 ? "disabled" : ""}>Voltar</button>
      <button type="submit" class="btn primary">${last ? genTxt : "Continuar"}</button></div>
    </form></div>
  </div>`;
  $("#stepbar").style.width = ((step + 1) / STEPS.length) * 100 + "%";  // via CSSOM (a CSP barra style inline)
  wireInvite();
  // trocar o sistema troca as etapas: redesenha na hora (a lista da esquerda muda)
  $$('input[name="sys"]').forEach(r => r.onchange = () => { collect(); renderWizard(); });
  // trocar a família escolhe a 1ª versão dela (ou a que já estava escolhida)
  $$('input[name="fam"]').forEach(r => r.onchange = () => {
    collect();
    if (famOf(W.sys) !== r.value) W.sys = SYSTEMS.find(([v]) => famOf(v) === r.value)[0];
    applySys(); renderWizard();
  });
  wireWinCatalog();
  wireWinKey();
  $$(".steps li").forEach(li => li.onclick = () => { collect(); const i = +li.dataset.i; if (i < step) { step = i; renderWizard(); } });
  $("#back").onclick = () => { collect(); step = Math.max(0, step - 1); renderWizard(); };
  $$('input[name="net_mode"]').forEach(r => r.onchange = () => $("#static").classList.toggle("hidden", r.value !== "static"));
  $$('input[name="ssh_auth"]').forEach(r => r.onchange = () => {
    const pw = r.value === "password";
    $("#pwnote").classList.toggle("hidden", !pw);
    $("#keyopt").textContent = pw ? "(opcional)" : "(opcional: em branco = usuário e senha)";
    $("#pwhelp").textContent = (pw ? "Mínimo 12 caracteres. Usada no SSH, no sudo e no console." : "Mínimo 10 caracteres. Usada no sudo e no console físico.") + " Não fica salva: só o hash vai na ISO.";
  });
  $$('input[name="storage_type"]').forEach(r => r.onchange = () => $("#sval").classList.toggle("hidden", r.value === "single"));
  $$('input[name="apps"]').forEach(r => r.onchange = () => { $("#coolifyBox").classList.toggle("hidden", !r.value.startsWith("coolify")); syncPkgs(); });
  wirePkgCatalog();
  const dl = $('input[name="deliver_on"]'); if (dl) dl.onchange = () => $("#deliverBox").classList.toggle("hidden", !dl.checked);
  const ts = $('input[name="tailscale"]') || $('input[name="win_tailscale"]'); if (ts) ts.onchange = () => $("#ts").classList.toggle("hidden", !ts.checked);
  [["ddns", "#ddnsBox"], ["cloudflare", "#cfBox"], ["virt", "#virtBox"]].forEach(([n, box]) => {
    const c = $(`input[name="${n}"]`); if (c) c.onchange = () => $(box).classList.toggle("hidden", !c.checked);
  });
  const pf = $("#pubfile");
  if (pf) pf.onchange = () => {
    const f = pf.files[0]; if (!f) return;
    if (f.size > 20000) return toast("Arquivo grande demais para ser uma chave pública.");
    const rd = new FileReader();
    rd.onload = () => {
      const t = String(rd.result);
      if (/PRIVATE KEY|-----BEGIN/.test(t)) return toast("Esse é o arquivo da chave PRIVADA. Escolha o .pub.");
      const ta = $('textarea[name="ssh_keys"]'); ta.value = (ta.value.trim() + "\n" + t.trim()).trim(); toast("Chave carregada.");
    };
    rd.readAsText(f);
  };
  $("#wf").onsubmit = async ev => {
    ev.preventDefault(); collect();
    const errs = checkStep();
    if (autoSwitched) {
      autoSwitched = false;
      const msg = `<div class="note warn">Nenhuma chave SSH informada: o acesso ao servidor será por <b>usuário e senha</b> (aceita só pela rede local e pelo Tailscale). Se preferir chave, cole-a abaixo e escolha “Chave SSH”.</div>`;
      if (errs.length) { renderWizard(); $("#serr").innerHTML = msg + `<div class="note err">${errs.map(esc).join("<br>")}</div>`; return; }
      toast("Sem chave SSH: acesso por usuário e senha.");
    }
    if (errs.length) { $("#serr").innerHTML = `<div class="note err">${errs.map(esc).join("<br>")}</div>`; return; }
    if (!last) { step++; return renderWizard(); }
    if (W.confirm_erase !== "APAGAR") { $("#serr").innerHTML = `<div class="note err">Digite APAGAR para confirmar.</div>`; return; }
    const btn = $('#wf button[type="submit"]'); btn.disabled = true; btn.textContent = "Enviando…";
    try {
      const remote_agent = !!(W.remote_agent && FEATURES.remote_agent);
      // Windows: só os campos dele (nada do Ubuntu vai junto); Ubuntu: como sempre
      const body = isWin()
        ? { os: "windows", hostname: W.hostname, username: W.username, realname: W.realname, password: W.password, win_key: W.win_key,
            win_timezone: W.win_timezone, win_keyboard: W.win_keyboard, panel_access: W.panel_access, remote_agent, win_apps: W.win_apps,
            wireguard: !!(W.wireguard && FEATURES.wireguard), tailscale: W.win_tailscale,
            ...(W.win_tailscale ? { tailscale_key: W.tailscale_key, tailscale_hostname: W.tailscale_hostname } : {}),
            confirm_erase: W.confirm_erase }
        : { ...W, os: "ubuntu", storage_min_gb: +W.storage_min_gb, remote_agent, terminalsec: !!(W.terminalsec && FEATURES.terminalsec) };
      if (!isWin()) delete body.win_key;
      if (W.deliver_on) body.deliver = { prefix: W.deliver_prefix, duration: W.deliver_duration, label: W.deliver_label, remote_agent: !!(W.deliver_agent && agentOK()) };
      const r = await api("/api/builds", { method: "POST", body });
      if (r.delivery) { try { sessionStorage.setItem("uab-delivery-" + r.id, JSON.stringify(r.delivery)); } catch (e) { } }
      W.password = W.password2 = W.tailscale_key = W.confirm_erase = W.coolify_password = W.coolify_password2 = W.ddns_password = W.cloudflare_token = W.win_key = ""; step = 0;
      W.deliver_on = false; W.deliver_prefix = W.deliver_label = "";
      go("#/iso/" + r.id);
    } catch (e) { $("#serr").innerHTML = `<div class="note err">${esc(e.message)}</div>`; btn.disabled = false; btn.textContent = genTxt; }
  };
}

/* -------------------------------------------------- lista de ISOs */
const STATUS = { queued: "Na fila", running: "Gerando", done: "Pronta", error: "Erro", expired: "Expirada" };
async function renderBuilds() {
  view.innerHTML = `<h1>Minhas ISOs</h1><div id="bl" class="card">Carregando…</div>`;
  const d = await api("/api/builds");
  const q = d.quota ? `<p class="muted small">Você usou ${d.quota.used} de ${d.quota.max} ISOs deste acesso.</p>` : "";
  $("#bl").innerHTML = q + (d.builds.length ? `<div class="table-wrap"><table class="rtable"><tr><th>Nome</th><th>Sistema</th><th>Status</th><th>Criada</th>${ME.role === "admin" ? "<th>Conta</th>" : ""}<th></th></tr>
    ${d.builds.map(b => `<tr><td class="rt-title"><div class="mname">${sysLogo(b, "")}<div><b>${esc(b.hostname)}</b>${b.os === "windows" ? `<br><span class="muted small">configuração do Windows</span>` : ""}</div></div></td><td data-label="Sistema" class="nowrap">${esc(sysName(b))}</td><td data-label="Status"><span class="pill ${b.status}">${STATUS[b.status] || esc(b.status)}</span></td>
      <td data-label="Criada" class="nowrap">${fmtDate(b.created_at)}</td>${ME.role === "admin" ? `<td class="small" data-label="Conta">${esc(b.owner)}</td>` : ""}<td class="rt-actions"><div class="row"><a class="btn sm" href="#/iso/${b.id}">Abrir</a>${ME.mode === "download" || ["queued", "running"].includes(b.status) ? "" : `<button class="btn sm danger" data-del="${b.id}">Excluir</button>`}</div></td></tr>`).join("")}
    </table></div>` : `<p class="muted">Nenhuma ISO ainda.</p><a class="btn primary" href="#/">Criar a primeira</a>`);
  $$("#bl [data-del]").forEach(b => b.onclick = () => delBuild(b.dataset.del, renderBuilds));
}

/* -------------------------------------------------- uma ISO: progresso + download + tutorial */
function deliveryCard(id) {
  let c = null;
  try { c = JSON.parse(sessionStorage.getItem("uab-delivery-" + id) || "null"); } catch (e) { }
  if (!c) return "";
  return `<div class="card" id="dcard"><h2>Acesso criado para esta ISO</h2>
    <p class="muted small">Envie para a pessoa. A senha <b>não será mostrada novamente</b> (em Acessos dá para gerar outra). Ela verá o progresso e, quando pronta, o botão de download.</p>
    <div class="cred" id="dtxt">${esc(credText(c))}</div>
    <div class="row u-mt12"><button class="btn primary" id="dcopy">Copiar tudo</button><button class="btn" id="dclose">Já copiei, fechar</button></div></div>`;
}
function wireDelivery(id) {
  const c = JSON.parse(sessionStorage.getItem("uab-delivery-" + id) || "null");
  if (!c || !$("#dcopy")) return;
  $("#dcopy").onclick = async () => { try { await navigator.clipboard.writeText(credText(c)); toast("Copiado!"); } catch (e) { toast("Selecione o texto e copie."); } };
  $("#dclose").onclick = () => { sessionStorage.removeItem("uab-delivery-" + id); $("#dcard").remove(); };
}

async function renderBuild(id) {
  let b;
  try { b = (await api("/api/builds/" + id)).build; } catch (e) { view.innerHTML = `<div class="note err">${esc(e.message)}</div>`; return; }
  const win = b.os === "windows";
  if (b.status === "queued" || b.status === "running") {
    view.innerHTML = deliveryCard(id) + (win ? `<div class="card narrow"><h1>Gerando a configuração de ${esc(b.hostname)}</h1>
      <p class="muted">Leva poucos segundos.</p><div class="bar"><i id="pb"></i></div><p id="pm"><b>${Math.round(b.progress)}%</b> · ${esc(b.message)}</p></div>`
      : `<div class="card narrow"><h1>Gerando a ISO de ${esc(b.hostname)}</h1>
      <p class="muted">Pode levar alguns minutos (na primeira vez, o servidor também baixa o Ubuntu oficial, ~3–4 GB). Você pode sair desta página: a geração continua.</p>
      <div class="bar"><i id="pb"></i></div><p id="pm"><b>${Math.round(b.progress)}%</b> · ${esc(b.message)}</p></div>`);
    $("#pb").style.width = b.progress + "%";
    wireDelivery(id);
    timer = setInterval(async () => {
      try {
        const n = (await api("/api/builds/" + id)).build;
        if (n.status !== "queued" && n.status !== "running") return route();
        $("#pb").style.width = n.progress + "%";
        $("#pm").innerHTML = `<b>${Math.round(n.progress)}%</b> · ${esc(n.message)}`;
      } catch (e) { }
    }, 2000);
    return;
  }
  if (b.status === "error") {
    view.innerHTML = `<div class="card"><h1>Não foi possível gerar ${win ? "a configuração" : "a ISO"}</h1><div class="note err">${esc(b.message)}</div>
      <div class="row"><a class="btn primary" href="#/">Tentar de novo</a><button class="btn" id="showlog">Ver detalhes técnicos</button><button class="btn danger" id="del">Excluir</button></div><pre class="log hidden" id="log"></pre></div>`;
    $("#showlog").onclick = async () => { $("#log").textContent = await api(`/api/builds/${id}/log`); $("#log").classList.remove("hidden"); };
    $("#del").onclick = () => delBuild(id);
    return;
  }
  if (b.status === "expired") {
    view.innerHTML = `<div class="card"><h1>${win ? "Configuração expirada" : "ISO expirada"}</h1><p class="muted">${esc(b.message)}. Gere novamente se precisar.</p><a class="btn primary" href="#/">Nova ISO</a></div>`;
    return;
  }
  win ? renderWinDownload(b) : renderDownload(b);
}

async function delBuild(id, after) {
  if (!confirm("Excluir esta ISO do servidor? O download e o gravador personalizado dela param de funcionar; servidores já instalados com ela continuam aparecendo em Máquinas.")) return;
  try { await api("/api/builds/" + id, { method: "DELETE" }); toast("ISO excluída."); after ? after() : go("#/isos"); } catch (e) { toast(e.message); }
}

function tutorialFor(os, b) {
  const iso = esc(b.iso_name);
  if (os === "windows" || os === "windows-arm64") return `
    <ol>
      <li>Conecte o pendrive (8 GB ou mais; <b>tudo nele será apagado</b>).</li>
      <li>Abra o arquivo <b>isoBuilder-Windows.exe</b> que você baixou.</li>
      <li>Se aparecer <i>“O Windows protegeu o computador”</i>: clique em <b>Mais informações</b> → <b>Executar assim mesmo</b>. (Aparece porque o programa não tem certificado pago de assinatura.)</li>
      <li>Clique <b>Sim</b> na pergunta de permissão de administrador (necessária para gravar no pendrive).</li>
      <li>O gravador abre no navegador e <b>baixa sozinho a ISO ${iso}</b> (~${fmtBytes(b.iso_size)}) para a pasta Downloads, conferindo se veio íntegra. Se a internet cair, ele continua de onde parou.</li>
      <li>Escolha o pendrive, marque a confirmação e clique <b>Gravar pendrive</b>.</li>
      <li>Espere a gravação e a verificação (5–15 min). Ao ver <b>“Pendrive pronto!”</b>, pode removê-lo.</li>
    </ol>`;
  if (os === "macos") return `
    <ol>
      <li>Conecte o pendrive (8 GB ou mais; <b>tudo nele será apagado</b>).</li>
      <li>Abra o <b>isoBuilder-macOS.zip</b> baixado (ele vira o app <b>isoBuilder</b>) e mova para a pasta Aplicativos, se quiser.</li>
      <li>Na primeira vez: clique com o <b>botão direito → Abrir → Abrir</b>. No macOS 15 (Sequoia) ou mais novo: tente abrir, depois vá em <b>Ajustes do Sistema → Privacidade e Segurança</b> e clique em <b>Abrir Mesmo Assim</b>. (Aparece porque o app não é notarizado pela Apple.)</li>
      <li>O gravador abre no navegador e <b>baixa sozinho a ISO ${iso}</b> (~${fmtBytes(b.iso_size)}) para Downloads, conferindo se veio íntegra. Depois escolha o pendrive, confirme e clique <b>Gravar pendrive</b>.</li>
      <li>O Mac pede a <b>sua senha</b> (necessária para gravar no pendrive).</li>
      <li>Ao ver <b>“Pendrive pronto!”</b>, o pendrive é ejetado e pode ser removido. Se o Mac disser “disco ilegível”, clique <b>Ignorar</b> — é normal.</li>
    </ol>`;
  if (os === "linux") return `
    <ol><li>Dê permissão ao gravador baixado e rode com sudo (ele baixa a ISO sozinho):</li></ol>
    <pre class="log">chmod +x isobuilder-linux-amd64 && sudo ./isobuilder-linux-amd64</pre>
    <p class="small">Ou manualmente: <code>sudo dd if=${iso} of=/dev/sdX bs=4M conv=fsync status=progress</code> (confira o /dev/sdX com <code>lsblk</code>!).</p>`;
  return `<p>Grave o pendrive a partir de um computador Windows ou Mac: abra este site nele e siga as instruções.</p>`;
}

function renderDownload(b) {
  const os = detectOS();
  const helperName = { windows: "isoBuilder para Windows", "windows-arm64": "isoBuilder para Windows (ARM)", macos: "isoBuilder para Mac", linux: "isoBuilder para Linux" }[os];
  view.innerHTML = deliveryCard(b.id) + `
  <div class="spread"><div><h1>ISO pronta: ${esc(b.hostname)}</h1>
    <p class="muted">Ubuntu ${esc(b.series)} · ${fmtBytes(b.iso_size)} · disponível até ${fmtDate(b.expires_at)}</p></div>
    <span class="pill done">Pronta</span></div>
  ${b.summary.TAILSCALE_ENABLED === "yes" ? `<div class="note warn">A ISO contém a auth key do Tailscale e o hash da senha: guarde o arquivo e o pendrive com cuidado.</div>` : ""}
  <div class="dl">
    ${os !== "mobile" ? `<div class="stepbox"><span class="n">1</span><div><h2>Baixe o isoBuilder (gravador que já vem com a ISO) ou só a ISO</h2>
      <div class="row"><a class="btn primary big-dl" href="/download/bundle/${b.id}/${os}" download>⬇ Baixar para ${OS_NAME[os]}</a>
        <a class="btn" href="/download/iso/${b.id}" download>⬇ Baixar só a ISO (${fmtBytes(b.iso_size)})</a>
        <a class="btn sm" href="/download/sha256/${b.id}" download>.sha256</a></div>
      <p class="muted small u-mt8"><b>Recomendado:</b> o isoBuilder é um único arquivo pequeno; ao abrir, ele baixa a ISO <b>${esc(b.iso_name)}</b> sozinho, confere a integridade e grava o pendrive. Link válido até ${fmtDate(Math.min(b.expires_at, Date.now() / 1000 + 7 * 86400))}.<br>
      <b>Só a ISO:</b> para gravar do seu jeito (Rufus em modo DD, balenaEtcher, <code>dd</code>). Veja “Outras formas de baixar” abaixo.</p></div></div>`
    : `<div class="stepbox"><span class="n">1</span><div><h2>Abra este site no computador</h2>
      <p>Para gravar o pendrive, acesse este endereço num computador Windows ou Mac com o pendrive conectado.</p>
      <div class="row"><a class="btn" href="/download/iso/${b.id}" download>⬇ Baixar só a ISO (${fmtBytes(b.iso_size)})</a></div></div></div>`}
    ${os !== "mobile" ? `<div class="stepbox"><span class="n">2</span><div><h2>Grave o pendrive</h2>${tutorialFor(os, b)}</div></div>` : ""}
    <div class="stepbox"><span class="n">${os !== "mobile" ? 3 : 2}</span><div><h2>No computador que vai virar servidor</h2>
      <ol><li>Conecte o <b>cabo de rede</b> e o pendrive.</li>
      <li>Ligue e abra o menu de boot (<b>F12</b>, F11, F8 ou Esc, conforme a marca) e escolha o pendrive.</li>
      <li>Não precisa fazer mais nada: a instalação é automática (~10–20 min) e o computador reinicia sozinho.</li>
      ${b.summary.TAILSCALE_ENABLED === "yes"
        ? `<li>Alguns minutos depois ele aparece no Tailscale como <b>${esc(b.summary.TAILSCALE_HOSTNAME || b.hostname)}</b>. Acesse com:<br>
        <code>ssh ${esc(b.summary.ADMIN_USER)}@${esc(b.summary.TAILSCALE_HOSTNAME || b.hostname)}</code> (com MagicDNS) ou <code>ssh ${esc(b.summary.ADMIN_USER)}@&lt;IP do Tailscale&gt;</code>${b.summary.SSH_AUTH === "password" ? " e digite a senha. Para administrar: <code>sudo</code>." : ""}</li>`
        : `<li>Esta ISO não usa Tailscale: acesse pela rede local com <code>ssh ${esc(b.summary.ADMIN_USER)}@&lt;IP do servidor&gt;</code>${b.summary.SSH_AUTH === "password" ? " (e digite a senha)" : ""} (o IP aparece na tela de login do servidor${b.summary.NET_MODE === "static" ? ": " + esc((b.summary.NET_ADDRESS || "").split("/")[0]) : " e no seu roteador"}).</li>`}
      ${b.summary.COOLIFY_ENABLED === "yes" ? `<li><b>Coolify</b> (instalado no primeiro boot, alguns minutos depois): <code>http://&lt;IP&gt;:8000</code>, entre com o admin ${esc(b.summary.COOLIFY_ADMIN_EMAIL || "definido na ISO")}.</li>` : ""}
      ${b.summary.COCKPIT_ENABLED === "yes" ? `<li><b>Cockpit</b>: <code>https://&lt;IP&gt;:9090</code> (aceite o certificado), com o usuário <b>${esc(b.summary.ADMIN_USER)}</b> e a senha do servidor.</li>` : ""}
      <li>Se o pendrive ficar conectado, não tem problema: ele não reinstala por cima.</li></ol></div></div>
  </div>
  <details><summary>Outras formas de baixar / outros sistemas</summary>
    <h3>Gravador com a ISO, para outro sistema</h3>
    <div class="row">
      <a class="btn sm" href="/download/bundle/${b.id}/windows" download>Windows (x64)</a>
      <a class="btn sm" href="/download/bundle/${b.id}/windows-arm64" download>Windows (ARM)</a>
      <a class="btn sm" href="/download/bundle/${b.id}/macos" download>Mac (Intel e Apple Silicon)</a>
      <a class="btn sm" href="/download/bundle/${b.id}/linux" download>Linux</a></div>
    <h3>Arquivos separados</h3>
    <div class="row">
      <a class="btn sm" href="/download/iso/${b.id}" download>Só a ISO (${fmtBytes(b.iso_size)})</a>
      <a class="btn sm" href="/download/sha256/${b.id}" download>Arquivo .sha256</a></div>
    <p class="small muted u-mt8">Com a ISO e o .sha256 na mesma pasta, o gravador também funciona sem internet.</p>
    <h3>Sem o gravador</h3>
    <p class="small"><b>Rufus</b> (Windows, <a href="https://rufus.ie" target="_blank" rel="noopener">rufus.ie</a>): escolha o pendrive e a ISO → Iniciar → quando perguntar, escolha <b>“Gravar em modo Imagem DD”</b>.<br>
    <b>balenaEtcher</b> (Windows/Mac/Linux, <a href="https://etcher.balena.io" target="_blank" rel="noopener">etcher.balena.io</a>): Flash from file → Select target → Flash.</p>
    <p class="small muted">SHA256 da ISO: <span class="mono">${esc(b.sha256)}</span></p>
  </details>
  ${ME.mode === "download" ? "" : `<div class="row u-mt16"><a class="btn" href="#/isos">Voltar</a><button class="btn danger" id="del">Excluir do servidor</button></div>`}`;
  const del = $("#del"); if (del) del.onclick = () => delBuild(b.id);
  wireDelivery(b.id);
}

/* ---------- build do Windows: o gravador baixa o Windows da Microsoft e aplica a configuração */
function winTutorial(os) {
  const exe = { windows: "isoBuilder-Windows.exe", "windows-arm64": "isoBuilder-Windows-ARM64.exe", macos: "isoBuilder-macOS.zip", linux: "isobuilder-linux-amd64" }[os];
  const open = os === "macos" ? `<li>Abra o <b>${exe}</b> (vira o app <b>isoBuilder</b>). Na primeira vez: <b>botão direito → Abrir → Abrir</b> (no macOS 15 ou mais novo, libere em <b>Ajustes do Sistema → Privacidade e Segurança → Abrir Mesmo Assim</b>). O Mac pede a sua senha para gravar no pendrive.</li>`
    : os === "linux" ? `<li>Dê permissão e rode com sudo: <code>chmod +x ${exe} && sudo ./${exe}</code></li>`
    : `<li>Abra o <b>${exe}</b>. Se aparecer <i>“O Windows protegeu o computador”</i>: <b>Mais informações</b> → <b>Executar assim mesmo</b>, e clique <b>Sim</b> na permissão de administrador.</li>`;
  return `<ol>
      <li>Conecte o pendrive (<b>16 GB ou mais</b>; <b>tudo nele será apagado</b>).</li>
      ${open}
      <li>O gravador abre no navegador, <b>baixa o Windows 11 oficial direto da Microsoft</b> (~8 GB, português do Brasil) e confere a configuração deste painel. Se o download da Microsoft for bloqueado, ele deixa escolher uma ISO do Windows 11 já baixada.</li>
      <li>Escolha o pendrive, confirme e clique <b>Gravar pendrive</b>. Ao ver <b>“Pendrive pronto!”</b>, pode removê-lo.</li>
    </ol>`;
}

function renderWinDownload(b) {
  const os = detectOS(), s = b.summary, apps = (s.WIN_APPS || "").split(" ").filter(Boolean);
  const until = fmtDate(Math.min(b.expires_at, Date.now() / 1000 + 7 * 86400));
  view.innerHTML = deliveryCard(b.id) + `
  <div class="spread"><div class="mname">${osLogo("Windows", "lg")}<div><h1>Configuração pronta: ${esc(b.hostname)}</h1>
    <p class="muted">Windows ${esc(s.WIN_VERSION || b.series)} ${esc(s.WIN_EDITION || "Pro")} · ${esc(s.WIN_LANG || "pt-BR")} · disponível até ${fmtDate(b.expires_at)}</p></div></div>
    <span class="pill done">Pronta</span></div>
  <div class="note"><b>O gravador baixa o Windows oficial da Microsoft e aplica esta configuração.</b> O painel gera só a configuração (${fmtBytes(b.iso_size)}): usuário, região, disco, acesso e programas. Nenhuma ISO do Windows passa por aqui.</div>
  <div class="note warn">A configuração contém a senha do usuário${[s.WIN_ACTIVATION === "key" ? "a chave do Windows" : "", s.TAILSCALE_ENABLED === "yes" ? "a auth key do Tailscale" : ""].filter(Boolean).map(x => ", " + x).join("").replace(/, ([^,]*)$/, " e $1")}: guarde o pendrive com cuidado.</div>
  <div class="dl">
    ${os !== "mobile" ? `<div class="stepbox"><span class="n">1</span><div><h2>Baixe o isoBuilder (gravador de pendrive)</h2>
      <div class="row"><a class="btn primary big-dl" href="/download/bundle/${b.id}/${os}" download>⬇ Baixar para ${OS_NAME[os]}</a></div>
      <p class="muted small u-mt8">Um arquivo pequeno que já vem com esta configuração. Link válido até ${until}.</p></div></div>
      <div class="stepbox"><span class="n">2</span><div><h2>Grave o pendrive</h2>${winTutorial(os)}</div></div>`
    : `<div class="stepbox"><span class="n">1</span><div><h2>Abra este site no computador</h2>
      <p>Para gravar o pendrive, acesse este endereço num computador Windows, Mac ou Linux com o pendrive conectado.</p></div></div>`}
    <div class="stepbox"><span class="n">${os !== "mobile" ? 3 : 2}</span><div><h2>No computador que vai receber o Windows</h2>
      <ol><li>Conecte o pendrive (e, se possível, o <b>cabo de rede</b>: os programas e o OpenSSH precisam de internet).</li>
      <li>Ligue e abra o menu de boot (<b>F12</b>, F11, F8 ou Esc, conforme a marca) e escolha o pendrive (modo <b>UEFI</b>).</li>
      <li>Não precisa fazer mais nada: o <b>maior disco interno é apagado</b>, o Windows se instala sozinho (~20–40 min) e reinicia algumas vezes. Entra direto com o usuário <b class="mono">${esc(s.ADMIN_USER)}</b>.</li>
      <li>Com internet, ele aparece em <b>Máquinas</b> com o nome <b>${esc(b.hostname)}</b>${s.REMOTE_AGENT === "yes" ? " e no <b>Remoto</b>" : ""}${s.WG_ENABLED === "yes" ? ", conecta o túnel WireGuard até o seu servidor" : ""}${s.TAILSCALE_ENABLED === "yes" ? `, entra na sua Tailnet como <b>${esc(s.TAILSCALE_HOSTNAME || b.hostname)}</b>` : ""}${apps.length ? `, e instala ${apps.length === 1 ? "o programa escolhido" : `os ${apps.length} programas escolhidos`} (acompanhe em Máquinas → Resumo)` : ""}.</li>
      <li>Retire o pendrive quando a instalação começar a reiniciar${s.WIN_ACTIVATION === "key" ? "" : ". Sem chave, o Windows fica sem ativar (ative depois em Configurações → Sistema → Ativação)"}.</li></ol></div></div>
  </div>
  <details><summary>Avançado: gravar com Ventoy ou Rufus / outros sistemas</summary>
    <h3>Gravador para outro sistema</h3>
    <div class="row">
      <a class="btn sm" href="/download/bundle/${b.id}/windows" download>Windows (x64)</a>
      <a class="btn sm" href="/download/bundle/${b.id}/windows-arm64" download>Windows (ARM)</a>
      <a class="btn sm" href="/download/bundle/${b.id}/macos" download>Mac (Intel e Apple Silicon)</a>
      <a class="btn sm" href="/download/bundle/${b.id}/linux" download>Linux</a></div>
    <h3>Só a configuração</h3>
    <div class="row"><a class="btn sm" href="/download/config/${b.id}" download>⬇ Baixar autounattend.xml</a>
      <a class="btn sm" href="/download/sha256/${b.id}" download>.sha256</a></div>
    <p class="small u-mt8"><b>Ventoy:</b> copie a ISO do Windows 11 para o pendrive e, no plugin <i>auto_install</i> (<code>ventoy/ventoy.json</code>), associe a ISO a este arquivo.<br>
    <b>Rufus</b> (<a href="https://rufus.ie" target="_blank" rel="noopener">rufus.ie</a>): grave a ISO do Windows 11 normalmente, <b>desmarcando</b> as opções de personalização do Rufus, e depois copie o <code>autounattend.xml</code> para a raiz do pendrive.</p>
    <p class="small muted">SHA256 da configuração: <span class="mono">${esc(b.sha256)}</span></p>
  </details>
  ${ME.mode === "download" ? "" : `<div class="row u-mt16"><a class="btn" href="#/isos">Voltar</a><button class="btn danger" id="del">Excluir do servidor</button></div>`}`;
  const del = $("#del"); if (del) del.onclick = () => delBuild(b.id);
  wireDelivery(b.id);
}

/* -------------------------------------------------- admin: acessos */
function credText(c) {
  const extra = c.mode === "download" && c.iso ? `\nISO:      ${c.iso} (entre e siga os passos para gravar o pendrive)` : "";
  const until = c.mode === "partner" ? "Conta de sócio (sem prazo)" : `Válido até: ${fmtDate(c.expires_at)}`;
  return `Acesso ao ISO Builder\n\nEndereço: ${c.url}\nE-mail:   ${c.email}\nSenha:    ${c.password}\n${until}${extra}`;
}
function showCred(c, title) {
  $("#cred").innerHTML = `<div class="card"><h2>${esc(title)}</h2>
    <p class="muted small">Copie agora: por segurança a senha <b>não será mostrada novamente</b> (dá para gerar outra).</p>
    <div class="cred" id="credtxt">${esc(credText(c))}</div>
    <div class="row u-mt12"><button class="btn primary" id="copy">Copiar tudo</button><button class="btn" id="closecred">Fechar</button></div></div>`;
  $("#copy").onclick = async () => { try { await navigator.clipboard.writeText(credText(c)); toast("Copiado!"); } catch (e) { toast("Selecione o texto e copie manualmente."); } };
  $("#closecred").onclick = () => { $("#cred").innerHTML = ""; };
  $("#cred").scrollIntoView({ behavior: "smooth" });
}

async function renderUsers() {
  view.innerHTML = `<h1>Acessos</h1><p class="muted">Contas temporárias para outras pessoas gerarem ISOs ou só baixarem uma ISO que você preparou. Ninguém consegue se cadastrar sozinho.</p>
  <div class="card"><h2>Novo acesso temporário</h2><form id="uf">
    <div class="choice">
      <label><input type="radio" name="mode" value="build" checked><span><b>Pode gerar ISOs</b><span class="muted small">A pessoa preenche o assistente e gera as próprias ISOs.</span></span></label>
      <label><input type="radio" name="mode" value="download"><span><b>Só baixar uma ISO pronta</b><span class="muted small">Você gera a ISO; a pessoa só baixa e grava o pendrive. Ela não vê senha nem auth key.</span></span></label>
      <label><input type="radio" name="mode" value="partner"><span><b>Sócio (permanente)</b><span class="muted small">Sem prazo e sem ISOs. Acessa só as máquinas que você liberar, pelo painel e pelo Remoto, com as permissões que escolher.</span></span></label>
    </div>
    <label class="f hidden" id="isoPick"><span>ISO para este acesso</span><select name="build_id"></select>
      <small>A ISO fica guardada até o fim da validade do acesso.</small></label>
    ${agentOK() ? `<label class="switch hidden" id="agentPick"><input type="checkbox" name="remote_agent" checked><span><b>Instalar acesso remoto junto</b><br><span class="muted small">O gravador do cliente oferece instalar o agente do Remoto (a pessoa precisa permitir na tela). Assim você acessa o PC dela para gravar o pendrive e formatar.</span></span></label>` : ""}
    <div class="grid">
      <label class="f"><span>Para quem (opcional)</span><input name="label" placeholder="ex.: Suporte da filial Centro"></label>
      <label class="f"><span>E-mail de login</span><div class="suffix"><input name="prefix" placeholder="ex.: filial-centro" autocapitalize="off" autocomplete="off"><span>@${esc(GUEST_DOMAIN)}</span></div><small id="pfxHelp">Só o prefixo; vazio = aleatório.</small></label>
      <label class="f" id="durF"><span>Validade</span><select name="duration"><option value="1h">1 hora</option><option value="24h" selected>24 horas</option><option value="3d">3 dias</option><option value="7d">7 dias</option><option value="30d">30 dias</option></select></label>
      <label class="f" id="maxB"><span>Máximo de ISOs</span><input name="max_builds" type="number" min="1" max="20" value="3"></label>
    </div>
    <div id="uerr"></div><button class="btn primary" type="submit">Criar acesso</button></form></div>
  <div id="cred"></div>
  <div class="card"><h2>Acessos criados</h2><div id="ul">Carregando…</div></div>`;
  const mine = (await api("/api/builds")).builds.filter(b => b.status === "done");
  $('select[name="build_id"]').innerHTML = mine.length
    ? mine.map(b => `<option value="${b.id}">${esc(b.hostname)} · ${esc(sysName(b))} · ${fmtDate(b.created_at)}</option>`).join("")
    : `<option value="">Nenhuma ISO pronta: gere uma em “Nova ISO”</option>`;
  $$('#uf input[name="mode"]').forEach(r => r.onchange = () => {
    const m = $('#uf input[name="mode"]:checked').value, dl = m === "download", pt = m === "partner";
    $("#isoPick").classList.toggle("hidden", !dl); $("#maxB").classList.toggle("hidden", dl || pt); $("#durF").classList.toggle("hidden", pt);
    const ap = $("#agentPick"); if (ap) ap.classList.toggle("hidden", !dl);
    $("#pfxHelp").textContent = pt ? "Prefixo, ou o e-mail completo do sócio (qualquer domínio)." : "Só o prefixo; vazio = aleatório.";
  });
  $("#uf").onsubmit = async ev => {
    ev.preventDefault();
    const f = Object.fromEntries(new FormData(ev.target));
    try { const c = await api("/api/admin/users", { method: "POST", body: f }); ev.target.reset(); $("#isoPick").classList.add("hidden"); $("#maxB").classList.remove("hidden"); const ap = $("#agentPick"); if (ap) ap.classList.add("hidden"); showCred(c, "Acesso criado"); loadUsers(); $("#uerr").innerHTML = ""; }
    catch (e) { $("#uerr").innerHTML = `<div class="note err">${esc(e.message)}</div>`; }
  };
  loadUsers();
}

async function loadUsers() {
  const [d, bl] = await Promise.all([api("/api/admin/users"), api("/api/builds")]);
  const ready = bl.builds.filter(b => b.status === "done");
  const isoOptions = cur => ready.length
    ? ready.map(b => `<option value="${b.id}" ${b.id === cur ? "selected" : ""}>${esc(b.hostname)} · ${esc(sysName(b))} · ${fmtDate(b.created_at)}</option>`).join("")
    : `<option value="">Nenhuma ISO pronta: gere uma em “Nova ISO”</option>`;
  if (!d.users.length) { $("#ul").innerHTML = `<p class="muted">Nenhum acesso criado.</p>`; return; }
  $("#ul").innerHTML = `<div class="table-wrap"><table class="rtable"><tr><th>Login</th><th>Situação</th><th>Validade</th><th>Último acesso</th><th>Tipo / ISOs</th><th></th></tr>
    ${d.users.map(u => `<tr><td class="rt-title"><b>${esc(u.email)}</b>${u.label ? `<br><span class="muted small">${esc(u.label)}</span>` : ""}</td>
      <td data-label="Situação"><span class="pill ${u.state === "ativa" ? "done" : "error"}">${esc(u.state)}</span></td><td data-label="Validade" class="nowrap">${u.mode === "partner" ? "sem prazo" : fmtDate(u.expires_at)}</td>
      <td data-label="Último acesso" class="nowrap">${fmtDate(u.last_login)}</td><td data-label="Tipo / ISOs"><div>${u.mode === "partner"
        ? `<b>Sócio</b><br><span class="small muted">${u.perm_count ? u.perm_count + (u.perm_count === 1 ? " máquina liberada" : " máquinas liberadas") : "nenhuma máquina liberada"}</span>`
        : u.mode === "download"
        ? `Só download<br><span class="small ${u.build_ok ? "muted" : "err"}">${u.build_hostname ? esc(u.build_hostname) : "ISO removida"}${!u.build_ok ? " (indisponível)" : u.build_status !== "done" ? " (sendo gerada)" : ""}</span>${agentLine(u)}
          <div class="hidden u-mt8" id="rl-${u.id}"><select>${isoOptions(u.build_ok ? u.build_id : "")}</select>
            <div class="row u-mt8"><button class="btn sm primary" data-a="relink-save" data-id="${u.id}" ${ready.length ? "" : "disabled"}>Vincular</button>
            <button class="btn sm" data-a="relink-cancel" data-id="${u.id}">Cancelar</button></div></div>`
        : `${u.builds}/${u.max_builds} ISOs`}</div></td>
      <td class="rt-actions"><div class="row">${u.mode === "partner"
        ? `<button class="btn sm primary" data-a="perms" data-id="${u.id}" data-email="${esc(u.email)}">Permissões</button>`
        : u.mode === "download" ? `<button class="btn sm ${u.build_ok ? "" : "primary"}" data-a="relink" data-id="${u.id}">${u.build_ok ? "Trocar ISO" : "Vincular ISO"}</button>` : ""}
      ${kebab(`${u.mode === "partner" ? "" : `<button data-a="topartner" data-id="${u.id}" data-email="${esc(u.email)}">Tornar sócio</button>`}
        ${u.mode === "download" && u.remote_agent && u.agent && u.agent.state === "ok" ? `<button data-a="agentopen" data-host="${esc(u.agent.host)}">Abrir tela do PC do cliente</button>` : ""}
        ${u.mode === "download" && (agentOK() || u.remote_agent) ? `<button data-a="agent" data-id="${u.id}" data-on="${u.remote_agent ? 0 : 1}">${u.remote_agent ? "Desligar" : "Ligar"} acesso remoto no gravador</button>` : ""}
        <button data-a="reset" data-id="${u.id}">Nova senha</button>
        ${u.mode === "partner" ? "" : `<button data-a="extend" data-id="${u.id}">Mais 24 horas de validade</button>`}
        ${u.state === "ativa" ? `<button class="danger" data-a="revoke" data-id="${u.id}">Revogar acesso</button>` : ""}
        <button class="danger" data-a="delete" data-id="${u.id}">Excluir conta</button>`)}</div></td></tr>`).join("")}</table></div>`;
  $$("#ul [data-a]").forEach(btn => btn.onclick = async () => {
    const id = btn.dataset.id, a = btn.dataset.a;
    if (a === "perms") return openPerms(id, btn.dataset.email);
    if (a === "agentopen") {
      try {
        const h = (btn.dataset.host || "").toLowerCase();
        const d = (await remoteDevices(true)).find(x => x.name.toLowerCase() === h);
        return d ? openRemoteView(d, "desktop") : toast("Esse PC ainda não apareceu no Remoto (ou está desligado).");
      } catch (e) { return toast(e.message); }
    }
    if (a === "agent") {
      try {
        await api(`/api/admin/users/${id}/agent`, { method: "POST", body: { enabled: btn.dataset.on === "1" } });
        toast(btn.dataset.on === "1" ? "Ligado: vale para o próximo download do gravador por esta conta." : "Desligado: o próximo gravador baixado não oferece o acesso remoto.");
        return loadUsers();
      } catch (e) { return toast(e.message); }
    }
    if (a === "topartner") {
      if (!confirm(`Tornar ${btn.dataset.email} sócio?\n\nA conta passa a ser permanente (sem validade), com o mesmo login e senha, e não baixa nem gera mais ISOs. Em seguida você escolhe as máquinas que ela pode acessar.`)) return;
      try {
        const r = await api(`/api/admin/users/${id}/partner`, { method: "POST" });
        toast("Agora é sócio. Escolha as permissões.");
        await loadUsers();
        return openPerms(id, r.email, r.suggest);
      } catch (e) { return toast(e.message); }
    }
    const box = $("#rl-" + id);
    if (a === "relink" || a === "relink-cancel") { box.classList.toggle("hidden", a === "relink-cancel"); $(`#ul [data-a="relink"][data-id="${id}"]`).classList.toggle("hidden", a === "relink"); return; }
    try {
      if (a === "relink-save") {
        const bid = box.querySelector("select").value;
        if (!bid) return;
        await api(`/api/admin/users/${id}/link`, { method: "POST", body: { build_id: bid } });
        toast("ISO vinculada. A pessoa já vê a nova ISO no próximo acesso.");
      }
      if (a === "reset") { if (!confirm("Gerar nova senha? A atual deixa de funcionar.")) return; showCred(await api(`/api/admin/users/${id}/reset`, { method: "POST" }), "Nova senha gerada"); }
      if (a === "extend") { await api(`/api/admin/users/${id}/extend`, { method: "POST", body: { duration: "24h" } }); toast("Validade: mais 24 horas a partir de agora."); }
      if (a === "revoke") { if (!confirm("Revogar este acesso agora?")) return; await api(`/api/admin/users/${id}/revoke`, { method: "POST" }); }
      if (a === "delete") { if (!confirm("Excluir a conta? Os arquivos das ISOs dela são apagados, mas os PCs instalados com pendrives já gravados continuam aparecendo em Máquinas. Sócios perdem também o acesso ao Remoto.")) return; await api(`/api/admin/users/${id}`, { method: "DELETE" }); }
      loadUsers();
    } catch (e) { toast(e.message); }
  });
}


/* -------------------------------------------------- menu "⋯" das tabelas */
const kebab = items => `<details class="kebab"><summary class="btn sm" aria-label="Mais ações" title="Mais ações">⋯</summary><div class="kebab-pop">${items}</div></details>`;
const closeKebabs = except => $$("details.kebab[open]").forEach(d => { if (d !== except) d.open = false; });
document.addEventListener("toggle", e => {
  const d = e.target;
  if (!(d instanceof HTMLDetailsElement) || !d.classList.contains("kebab") || !d.open) return;
  closeKebabs(d);
  const r = $("summary", d).getBoundingClientRect(), pop = $(".kebab-pop", d);
  const up = r.bottom + pop.offsetHeight + 8 > innerHeight && r.top > pop.offsetHeight + 8;
  pop.style.top = (up ? r.top - pop.offsetHeight - 6 : r.bottom + 6) + "px";
  pop.style.left = Math.max(8, Math.min(r.right - pop.offsetWidth, innerWidth - pop.offsetWidth - 8)) + "px";
}, true);
document.addEventListener("click", e => { const d = e.target.closest("details.kebab"); if (!d || e.target.closest(".kebab-pop button")) closeKebabs(null); });
addEventListener("scroll", () => closeKebabs(null), true);
addEventListener("resize", () => closeKebabs(null));

/* -------------------------------------------------- sócios: permissões por máquina */
const PCOLS = [["view", "Ver"], ["terminal", "Terminal"], ["desktop", "Tela"], ["files", "Arquivos"], ["vpn", "VPN"], ["config", "Configurações"]];
async function openPerms(uid, email, suggest = []) {
  closeOverlay();
  OVL = { perms: uid };
  const el = document.createElement("div");
  el.id = "ovl";
  el.innerHTML = `<div class="ovl-bg"></div><aside class="ovl" role="dialog" aria-modal="true">
    <header class="ovl-h"><div><b>Permissões de ${esc(email)}</b><br><span class="muted small">Marque o que o sócio pode fazer em cada máquina. <b>Admin</b> marca tudo; dá para desmarcar depois.</span></div>
      <button class="btn sm" id="ovlClose" aria-label="Fechar">✕</button></header>
    <div class="ovl-b" id="pBody">Carregando…</div></aside>`;
  document.body.appendChild(el);
  document.body.classList.add("noscroll");
  $(".ovl-bg", el).onclick = closeOverlay; $("#ovlClose").onclick = closeOverlay;
  let d;
  try { d = await api(`/api/admin/users/${uid}/perms`); } catch (e) { $("#pBody").innerHTML = `<div class="note err">${esc(e.message)}</div>`; return; }
  const B = d.bits, ALL = Object.values(B).reduce((a, b) => a | b, 0), NODE_ALL = B.view | B.terminal | B.desktop | B.files;
  const cur = {}; d.items.forEach(i => { cur[i.kind + ":" + i.ref] = i.perms; });
  // conta convertida em sócio: as máquinas da ISO que ela recebeu já vêm com "Ver" (nada vale até salvar)
  const sug = suggest.filter(id => d.machines.some(m => m.id === id) && !cur["machine:" + id]);
  sug.forEach(id => { cur["machine:" + id] = B.view; });
  const row = (kind, ref, name, sub, online) => {
    const key = kind + ":" + ref, p = cur[key] || 0, all = kind === "machine" ? ALL : NODE_ALL;
    const cols = PCOLS.map(([k, t]) => (kind === "node" && (k === "vpn" || k === "config"))
      ? `<td data-label="${t}" class="muted small">—</td>`
      : `<td data-label="${t}"><input type="checkbox" data-k="${key}" data-b="${B[k]}" ${p & B[k] ? "checked" : ""} aria-label="${t}"></td>`).join("");
    return `<tr data-row="${key}" data-all="${all}"><td class="rt-title">${dot(online)} <b>${esc(name)}</b><br><span class="muted small">${esc(sub)}</span></td>
      <td data-label="Admin"><label class="switch-mini"><input type="checkbox" data-admin="${key}" ${(p & all) === all ? "checked" : ""}> Admin</label></td>${cols}</tr>`;
  };
  const head = `<tr><th>Máquina</th><th>Tudo</th>${PCOLS.map(([, t]) => `<th>${t}</th>`).join("")}</tr>`;
  $("#pBody").innerHTML = `${d.note ? `<div class="note warn">${esc(d.note)}</div>` : ""}
    ${sug.length ? `<div class="note">${sug.length === 1 ? "A máquina instalada com a ISO que esta pessoa recebeu já veio" : `As ${sug.length} máquinas instaladas com a ISO que esta pessoa recebeu já vieram`} com <b>Ver</b> marcado. Ajuste e clique em <b>Salvar permissões</b>: nada é liberado antes disso.</div>` : ""}
    <h3>Máquinas do painel</h3>
    ${d.machines.length ? `<div class="table-wrap"><table class="rtable ptable">${head}${d.machines.map(m => row("machine", m.id, m.label || m.hostname, m.hostname, m.online)).join("")}</table></div>` : `<p class="muted">Nenhuma máquina instalada ainda.</p>`}
    ${d.nodes.length ? `<h3>Só no Remoto</h3><p class="muted small">Computadores adicionados por convite (não vieram de uma ISO).</p>
      <div class="table-wrap"><table class="rtable ptable">${head}${d.nodes.map(n => row("node", n.id, n.name, n.os, n.online)).join("")}</table></div>` : ""}
    <div class="row u-mt16"><button class="btn primary" id="pSave">Salvar permissões</button><button class="btn" id="pCancel">Cancelar</button></div>
    <p class="muted small u-mt8">Terminal vale para o terminal do painel e o do Remoto. Tela e Arquivos são pelo Remoto (a máquina precisa do agente). Marcar qualquer item já inclui Ver.</p>`;
  const sync = key => {
    const tr = $(`#pBody tr[data-row="${CSS.escape(key)}"]`), all = +tr.dataset.all;
    let p = 0; $$(`input[data-k="${CSS.escape(key)}"]`, tr).forEach(c => { if (c.checked) p |= +c.dataset.b; });
    if (p && !(p & B.view)) { $(`input[data-k="${CSS.escape(key)}"][data-b="${B.view}"]`, tr).checked = true; p |= B.view; }
    $(`input[data-admin]`, tr).checked = (p & all) === all;
    cur[key] = p;
  };
  $$("#pBody input[data-k]").forEach(c => c.onchange = () => {
    if (!c.checked && +c.dataset.b === B.view) $$(`input[data-k="${CSS.escape(c.dataset.k)}"]`).forEach(x => { x.checked = false; });  // sem ver = sem nada
    sync(c.dataset.k);
  });
  $$("#pBody input[data-admin]").forEach(c => c.onchange = () => {
    $$(`input[data-k="${CSS.escape(c.dataset.admin)}"]`).forEach(x => { x.checked = c.checked; });
    sync(c.dataset.admin);
  });
  $("#pCancel").onclick = closeOverlay;
  $("#pSave").onclick = async () => {
    const items = Object.entries(cur).filter(([, p]) => p).map(([k, p]) => { const [kind, ...ref] = k.split(":"); return { kind, ref: ref.join(":"), perms: p }; });
    $("#pSave").disabled = true;
    try {
      const r = await api(`/api/admin/users/${uid}/perms`, { method: "POST", body: { items } });
      toast(r.warning || "Permissões salvas.");
      closeOverlay(); loadUsers();
    } catch (e) { toast(e.message); $("#pSave").disabled = false; }
  };
}

/* -------------------------------------------------- admin: sistema */
async function renderRemoteFrame() {
  let u = FEATURES.remote_url;
  if (FEATURES.remote_sso) {  // remoto do próprio ISO Builder: abre já logado
    try { u = (await api("/api/remoto/login")).url; } catch (e) { }
  }
  const label = FEATURES.remote_sso ? location.host + "/remoto" : u.replace(/^https:\/\//, "").replace(/\/$/, "");
  view.innerHTML = `<div class="spread"><div><h1>Acesso remoto</h1><p class="muted">${esc(label)}</p></div>
    <a class="btn" href="${esc(u)}" target="_blank" rel="noopener noreferrer">Abrir em nova aba</a></div>
    <div class="rframe u-mt12"><div class="muted rload">Carregando…</div>
    <iframe src="${esc(u)}" title="Acesso remoto" allow="clipboard-read; clipboard-write; fullscreen"></iframe></div>`;
  $(".rframe iframe").onload = () => { const l = $(".rload"); if (l) l.remove(); };
}

async function renderSystem() {
  view.innerHTML = `<h1>Sistema</h1><div class="card"><h2>WireGuard (seu servidor como ponto de encontro)</h2><div id="hubCard">Carregando…</div></div>
    <div class="card"><h2>Painel na Tailnet</h2><div id="pnl">Carregando…</div></div>
    <div class="card"><h2>ISO oficial do Ubuntu</h2><div id="base">Carregando…</div></div>`;
  api("/api/panel").then(d => {
    const h = d.wg, hs = h.hub || {};
    $("#hubCard").innerHTML = !h.enabled
      ? `<div class="note warn">Desligado. Para usar o seu servidor como ponto de encontro: defina <code>HUB_ENDPOINT</code> (ex.: <code>seu-ddns.exemplo.com:51822</code>) no Coolify, abra essa porta <b>UDP</b> no roteador apontando para este servidor e reinicie.</div>`
      : `<div class="note ${hs.online ? "ok" : "err"}">${hs.online ? "Hub no ar" : esc(hs.message || "Hub fora do ar")} · endereço público <b class="mono">${esc(h.endpoint)}</b></div>
         <div class="grid2">${kv("Rede da VPN", `<span class="mono">${esc(hs.net || "10.77.0.0/16")}</span>`)}${kv("Mapas de rede dos clientes", `<span class="mono">${esc(hs.lanmap || "10.200.0.0/16")}</span>`)}${kv("Peers", Object.keys(hs.peers || {}).length)}${kv("Chave pública do hub", `<span class="mono small">${esc(hs.pubkey || "—")}</span>`)}</div>`;
    const t = d.tailscale;
    $("#pnl").innerHTML = `${panelBanner(t)}
      <div class="grid2">${kv("Estado", esc(t.state))}${kv("IP do painel", esc((t.ips || [])[0] || "—"))}${kv("Nome", esc(t.dns || t.hostname || "—"))}${kv("Chave aceita a partir de", `<span class="mono small">${esc(d.key_from)}</span>`)}</div>
      <details><summary>Chave pública do painel</summary><p class="small muted">Vai nas ISOs com “Acesso pelo painel” ligado, com a restrição <code>from=</code> acima. Para dar acesso a um servidor já instalado: <code>uab-add-ssh-key</code> não aceita opções; use <code>echo 'from="${esc(d.key_from)}" ${esc(d.public_key)}' >> ~/.ssh/authorized_keys</code>.</p>
      <pre class="log">${esc(d.public_key)}</pre></details>`;
  }).catch(e => { $("#pnl").innerHTML = `<div class="note err">${esc(e.message)}</div>`; });
  const load = async () => {
    const d = await api("/api/admin/base");
    $("#base").innerHTML = `<p class="muted small">Espaço livre no servidor: <b>${fmtBytes(d.disk.free)}</b> de ${fmtBytes(d.disk.total)}. Cada ISO gerada ocupa ~3–4 GB e é apagada ao expirar.</p>
    <div class="table-wrap"><table><tr><th>Versão</th><th>No servidor</th><th>Situação</th><th></th></tr>
    ${Object.entries(d.series).map(([s, v]) => {
      const t = v.task;
      const busy = t && ["queued", "downloading", "verifying"].includes(t.state);
      return `<tr><td><b>Ubuntu ${s} LTS</b></td><td>${v.cached ? esc(v.cached.name) + "<br><span class='muted small'>" + fmtBytes(v.cached.size) + "</span>" : "<span class='muted'>não baixada</span>"}</td>
        <td>${t ? `${esc(t.message)}${busy ? ` <b>${Math.round(t.percent)}%</b>` : ""}` : (v.cached ? "Pronta" : "—")}</td>
        <td><div class="row"><button class="btn sm" data-s="${s}" ${busy ? "disabled" : ""}>${v.cached ? "Verificar/atualizar" : "Baixar agora"}</button>${v.cached ? `<button class="btn sm danger" data-rm="${s}" ${busy ? "disabled" : ""}>Apagar</button>` : ""}</div></td></tr>`;
    }).join("")}</table></div>
    <p class="muted small">Se a ISO oficial não estiver no servidor, ela é baixada automaticamente na próxima geração. A assinatura GPG da Canonical e o SHA256 são sempre conferidos.
    ${d.keep_base ? "Para não guardá-la, apague aqui ou defina <code>KEEP_BASE_ISO=false</code> no Coolify (apaga sozinha após cada geração)." : "<b>KEEP_BASE_ISO=false</b>: ela é apagada sozinha após cada geração."}</p>`;
    $$("#base [data-rm]").forEach(b => b.onclick = async () => {
      if (!confirm(`Apagar a ISO oficial do Ubuntu ${b.dataset.rm} do servidor? As ISOs já geradas não são afetadas.`)) return;
      try { const r = await api("/api/admin/base/" + b.dataset.rm, { method: "DELETE" }); toast(`Apagada (${fmtBytes(r.freed)} liberados).`); load(); } catch (e) { toast(e.message); }
    });
    $$("#base [data-s]").forEach(b => b.onclick = async () => { try { await api("/api/admin/base/" + b.dataset.s, { method: "POST" }); load(); } catch (e) { toast(e.message); } });
  };
  await load();
  timer = setInterval(load, 3000);
}


/* -------------------------------------------------- máquinas instaladas (admin) */
const fmtAgo = t => {
  if (!t) return "—";
  const s = Math.max(0, Date.now() / 1000 - t);
  if (s < 60) return "agora";
  if (s < 3600) return Math.round(s / 60) + " min atrás";
  if (s < 86400) return Math.round(s / 3600) + " h atrás";
  return Math.round(s / 86400) + " dias atrás";
};
const fmtUptime = s => {
  if (s == null) return "—";
  const d = Math.floor(s / 86400), h = Math.floor((s % 86400) / 3600), m = Math.floor((s % 3600) / 60);
  return (d ? d + "d " : "") + (h ? h + "h " : "") + m + "min";
};
const sshTarget = m => m.ssh_host || (m.wg && m.wg.connected ? m.wg.server_ip : "") || m.tailscale_ip || (m.wg ? m.wg.server_ip : "") || (m.local_ips[0] || "").split(" ").pop().split("/")[0] || "";
const mstate = m => m.online ? "online" : m.off_reason === "reboot" ? "reiniciando" : m.off_reason ? "desligado" : "offline";
const dot = (on, t) => `<span class="dot ${on ? "on" : "off"}" title="${t || (on ? "online" : "offline")}"></span>`;

async function renderMachines() {
  view.innerHTML = `<div class="spread"><div><h1>Máquinas</h1>
    <p class="muted">Servidores instalados com ISOs deste painel. Eles se reportam no primeiro boot e a cada 10 minutos.</p></div>
    <button class="btn sm" id="mref">Atualizar</button></div>
    <div id="pbanner"></div><div id="ml" class="card">Carregando…</div>`;
  $("#mref").onclick = loadMachines;
  await loadMachines();
  timer = setInterval(() => { if (!document.hidden && !$("#ovl")) loadMachines(); }, 20000);
}

function panelBanner(p) {
  if (p.state === "Running") return `<div class="note ok">Painel conectado à Tailnet como <b>${esc(p.dns || p.hostname)}</b> (${esc((p.ips || [])[0] || "")}): o terminal no navegador funciona para máquinas com Tailscale.</div>`;
  return `<div class="note warn"><b>Terminal no navegador indisponível:</b> ${esc(p.message || "o painel não está na Tailnet.")}
    <br><span class="small">No Coolify, defina <code>TS_AUTHKEY</code> (auth key reutilizável ou de uso único, com a tag <code>tag:iso-painel</code>) e reinicie o serviço. A lista de máquinas funciona mesmo assim.</span></div>`;
}

async function loadMachines() {
  let d;
  try { d = await api("/api/machines"); } catch (e) { $("#ml").innerHTML = `<div class="note err">${esc(e.message)}</div>`; return; }
  if (ME.role !== "admin") $("#pbanner").innerHTML = "";  // aviso de configuração do painel: só o admin
  else $("#pbanner").innerHTML = d.hub && d.hub.online
    ? `<div class="note ok">Terminal no navegador pelo <b>WireGuard do seu servidor</b>${d.panel.state === "Running" ? " e pelo Tailscale" : ""}. Máquinas com o túnel conectado abrem direto.</div>`
    : panelBanner(d.panel);
  const active = d.machines.filter(m => !m.archived_at), arch = d.machines.filter(m => m.archived_at);
  if (!d.machines.length) {
    $("#ml").innerHTML = `<p class="muted">Nenhuma máquina se reportou ainda. As ISOs geradas a partir de agora fazem o servidor aparecer aqui alguns minutos depois do primeiro boot.</p>`;
    return;
  }
  $("#ml").innerHTML = (active.length ? `<div class="table-wrap"><table class="rtable"><tr><th></th><th>Servidor</th><th>Conta</th><th>VPN</th><th>Rede local</th><th>Provisionamento</th><th>Visto</th><th></th></tr>
    ${active.map(m => `<tr class="clickable" data-m="${m.id}">
      <td class="rt-dot">${dot(m.online, mstate(m))}</td>
      <td class="rt-title"><div class="mname">${osLogo(m.os)}<div><b>${esc(m.label || m.hostname)}</b>${m.label ? `<br><span class="muted small">${esc(m.hostname)}</span>` : ""}<br><span class="muted small">${esc(m.os)}</span>${tsecBadge(m)}${m.online ? "" : `<br><span class="err small">${mstate(m)} · ${fmtAgo(m.off_at || m.last_seen)}</span>`}</div></div></td>
      <td class="small" data-label="Conta">${m.account ? esc(m.account) : `<span class="muted">—</span>`}</td>
      <td data-label="VPN">${m.wg_ip ? `<span class="mono">${esc(m.wg_ip)}</span> <span class="muted small">WG</span>${m.wg && !m.wg.connected && m.online ? (String(m.wg_reason).startsWith("Conectando") ? ` <span class="muted small">conectando…</span>` : ` <span class="err small" title="${esc(m.wg_reason)}">sem conexão</span>`) : ""}<br>` : ""}${m.tailscale_ip ? `<span class="mono">${esc(m.tailscale_ip)}</span> <span class="muted small">TS</span>` : (m.wg_ip ? "" : `<span class="muted">—</span>`)}</td>
      <td class="small" data-label="Rede local">${esc((m.local_ips[0] || "—").split(" ").pop())}</td>
      <td data-label="Provisionamento">${m.provisioned.startsWith("ok") ? `<span class="pill done">concluído</span>` : `<span class="pill running">${esc(m.provisioned || "em andamento")}</span>`}</td>
      <td class="small" data-label="Visto">${fmtAgo(m.last_seen)}</td>
      <td class="rt-actions"><button class="btn sm primary" data-open="${m.id}">Abrir</button></td></tr>`).join("")}</table></div>`
    : `<p class="muted">Nenhuma máquina ativa. As arquivadas estão logo abaixo.</p>`)
    + (arch.length ? `<details class="archived" id="archBox" ${ARCH_OPEN ? "open" : ""}><summary>Arquivadas (${arch.length})</summary>
      <p class="muted small">Fora da lista principal. Se ligarem, os relatórios continuam chegando, mas terminal, VPN (túnel pausado) e acesso de sócios ficam bloqueados até restaurar.</p>
      <div class="table-wrap"><table class="rtable"><tr><th></th><th>Servidor</th><th>Arquivada</th><th>Visto</th><th></th></tr>
      ${arch.map(m => `<tr class="clickable" data-m="${m.id}"><td class="rt-dot">${dot(m.online, mstate(m))}</td>
        <td class="rt-title"><div class="mname">${osLogo(m.os)}<div><b>${esc(m.label || m.hostname)}</b>${m.label ? `<br><span class="muted small">${esc(m.hostname)}</span>` : ""}<br><span class="muted small">${esc(m.os)}</span></div></div></td>
        <td class="small nowrap" data-label="Arquivada">${fmtDate(m.archived_at)}</td>
        <td class="small" data-label="Visto">${m.online ? `<span class="ok">ligada agora</span>` : fmtAgo(m.last_seen)}</td>
        <td class="rt-actions"><div class="row"><button class="btn sm" data-restore="${m.id}">Restaurar</button>
          ${kebab(`<button class="danger" data-forget="${m.id}">Esquecer de vez</button>`)}</div></td></tr>`).join("")}</table></div></details>` : "");
  $$("#ml [data-m]").forEach(tr => tr.onclick = e => { if (!e.target.closest(".rt-actions")) openMachine(tr.dataset.m); });
  $$("#ml [data-open]").forEach(b => b.onclick = () => openMachine(b.dataset.open));
  const ab = $("#archBox"); if (ab) ab.ontoggle = () => { ARCH_OPEN = ab.open; };
  $$("#ml [data-restore]").forEach(b => b.onclick = () => machineAction(b.dataset.restore, "restore"));
  $$("#ml [data-forget]").forEach(b => b.onclick = () => machineAction(b.dataset.forget, "forget"));
}
let ARCH_OPEN = false;

// Arquivar / restaurar / esquecer de vez (lista e janela da máquina)
async function machineAction(id, action) {
  const msg = {
    archive: "Arquivar esta máquina?\n\nEla sai da lista principal. Se continuar ligada, os relatórios chegam normalmente, mas terminal, VPN (túnel pausado) e acesso de sócios ficam bloqueados até você restaurar. Nada é apagado.",
    forget: "ESQUECER DE VEZ esta máquina?\n\nApaga os dados, o túnel VPN e os acessos de dispositivos, e o painel passa a RECUSAR os relatórios desta instalação: ela não aparece mais, nem se ligar.\n\nSe o mesmo PC for reinstalado com o pendrive, ele aparece de novo como máquina nova.",
  }[action];
  if (msg && !confirm(msg)) return;
  try {
    if (action === "forget") await api("/api/machines/" + id, { method: "DELETE" });
    else await api(`/api/machines/${id}/${action}`, { method: "POST" });
    toast({ archive: "Máquina arquivada.", restore: "Máquina restaurada: terminal, VPN e sócios voltaram.", forget: "Máquina esquecida." }[action]);
    if ($("#ovl")) closeOverlay();
    loadMachines();
  } catch (e) { toast(e.message); }
}

/* ---------- overlay */
let OVL = null;  // {id, tab, term, ws, fit, ro}

/* -------------------------------------------------- Remoto (MeshCentral) nativo
   Lista e botões no visual do painel; só o visualizador (tela/terminal/arquivos)
   vem do MeshCentral, já logado e sem a interface dele. */
const RMODES = [["desktop", "Tela"], ["terminal", "Terminal"], ["files", "Arquivos"]];
let RDEV = { at: 0, list: [] };
async function remoteDevices(force) {
  if (!force && Date.now() - RDEV.at < 20000) return RDEV.list;
  RDEV = { at: Date.now(), list: (await api("/api/remoto/devices")).devices };
  return RDEV.list;
}
// Logo do sistema pelo nome que a máquina informa ("Ubuntu 24.04.5 LTS", "Microsoft Windows 11 Pro"…).
const OS_MATCH = [[/windows/i, "windows"], [/mint/i, "mint"], [/kali/i, "kali"], [/raspbian|raspberry/i, "raspberry"], [/proxmox/i, "proxmox"],
  [/ubuntu/i, "ubuntu"], [/debian/i, "debian"], [/fedora/i, "fedora"], [/centos/i, "centos"], [/rocky/i, "rocky"], [/alma/i, "alma"],
  [/red ?hat|rhel/i, "redhat"], [/suse/i, "suse"], [/arch/i, "arch"], [/truenas/i, "truenas"], [/freebsd/i, "freebsd"],
  [/mac|darwin/i, "macos"], [/linux/i, "linux"]];
function osLogo(os, cls = "") {
  const key = (OS_MATCH.find(([re]) => re.test(os || "")) || [])[1], ic = key && (window.OS_ICONS || {})[key];
  const svg = ic ? `<svg viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">${ic.d}</svg>`
    : `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><rect x="2" y="3" width="20" height="14" rx="2"/><path d="M8 21h8M12 17v4"/></svg>`;
  return `<span class="oslogo ${cls}" style="--c:${ic ? ic.c : "#64748b"}" title="${esc(os || "sistema desconhecido")}">${svg}</span>`;
}

async function renderRemote() {
  if (!FEATURES.remote_sso) return renderRemoteFrame();  // outro remote (REMOTE_URL): página dele inteira
  view.innerHTML = `<div class="spread"><div><h1>Remoto</h1><p class="muted">Tela, terminal e arquivos dos computadores com o agente, direto no navegador.</p></div>
    <div class="row">${ME.role === "admin" ? `<button class="btn primary" id="rInvite">Adicionar computador</button>` : ""}<button class="btn" id="rFull">Opções avançadas ↗</button></div></div>
    <div id="rInv"></div><div class="card"><div class="spread"><input id="rSearch" placeholder="Buscar pelo nome, sistema ou IP" class="u-w100 rsearch"><button class="btn sm" id="rReload">Atualizar</button></div>
    <div id="rList" class="u-mt12">Carregando…</div></div>`;
  $("#rReload").onclick = () => loadRemote(true);
  $("#rSearch").oninput = () => loadRemote(false);
  $("#rFull").onclick = async () => { try { window.open((await api("/api/remoto/login")).url, "_blank", "noopener"); } catch (e) { toast(e.message); } };
  if ($("#rInvite")) $("#rInvite").onclick = remoteInvite;
  await loadRemote(true);
  timer = setInterval(() => { if (!document.hidden && !$("#ovl")) loadRemote(true); }, 20000);
}

async function loadRemote(force) {
  let list;
  try { list = await remoteDevices(force); } catch (e) { $("#rList").innerHTML = `<div class="note err">${esc(e.message)}</div>`; return; }
  const q = ($("#rSearch")?.value || "").trim().toLowerCase();
  const shown = list.filter(d => !q || [d.name, d.os, d.ip, d.group].join(" ").toLowerCase().includes(q));
  if (!list.length) {
    $("#rList").innerHTML = `<div class="note">Nenhum computador ainda. Gere ISOs com <b>Programas → Extras → Aparecer no Remoto</b> ou use <b>Adicionar computador</b> para instalar o agente num PC.</div>`;
    return;
  }
  $("#rList").innerHTML = `<div class="table-wrap"><table class="rtable"><tr><th></th><th>Computador</th><th>Grupo</th><th>IP</th><th>Visto</th><th></th></tr>
    ${shown.map(d => `<tr><td class="rt-dot">${dot(d.online, d.online ? "online" : "offline")}</td>
      <td class="rt-title"><div class="mname">${osLogo(d.os)}<div><b>${esc(d.name)}</b><br><span class="muted small">${esc(d.os || "—")}</span></div></div></td>
      <td class="small" data-label="Grupo">${esc(d.group)}</td><td class="small mono" data-label="IP">${esc(d.ip || "—")}</td>
      <td class="small" data-label="Visto">${d.online ? "agora" : d.last ? fmtAgo(Math.floor(d.last / 1000)) : "—"}</td>
      <td class="rt-actions"><div class="row">${RMODES.map(([m, t]) => `<button class="btn sm ${m === "desktop" ? "primary" : ""}" data-rv="${esc(d.id)}" data-m="${m}" ${d.online ? "" : "disabled"}>${t}</button>`).join("")}</div></td></tr>`).join("")}
    </table></div>${shown.length ? "" : `<p class="muted">Nada encontrado para “${esc(q)}”.</p>`}
    <p class="muted small u-mt8"><b>Tela</b> funciona em computadores com área de trabalho (Windows, Mac, Linux com interface). Em servidores sem interface gráfica, use <b>Terminal</b> e <b>Arquivos</b>.</p>`;
  $$("#rList [data-rv]").forEach(b => b.onclick = () => openRemoteView(list.find(x => x.id === b.dataset.rv), b.dataset.m));
}

// Remoto pronto para receber agentes (grupo criado) e o admin pode usá-lo
const agentOK = () => !!(ME && ME.role === "admin" && FEATURES.remote_sso && FEATURES.remote_agent);
function agentLine(u) {           // estado do PC do cliente numa conta "só download"
  if (!u.remote_agent) return "";
  const a = u.agent || {};
  if (a.state === "ok") return `<br><span class="small ok">● PC conectado: <b>${esc(a.host)}</b>${a.os ? " · " + esc(a.os) : ""} · ${fmtAgo(a.at)}</span>`;
  if (a.state === "erro") return `<br><span class="small err">Acesso remoto falhou no PC do cliente: ${esc(a.message)}</span>`;
  if (a.state === "recusado") return `<br><span class="small muted">O cliente não permitiu o acesso remoto.</span>`;
  return `<br><span class="small muted">Acesso remoto: aguardando o cliente abrir o gravador.</span>`;
}

/* -------------------------------------------------- convite do agente (Remoto)
   Atalho para formatar um PC à distância: o cliente abre o link e instala o agente
   (a página do convite detecta Windows/Mac/Linux); o PC aparece no Remoto e o cartão
   avisa na hora. O estado fica aqui fora porque as telas são redesenhadas. */
let INV = null;   // { url, until, known: Set(ids já existentes), found: [], poll }
const INV_HOURS = [[1, "1 hora"], [24, "1 dia"], [72, "3 dias"], [168, "7 dias"]];
const inviteReady = () => ME && ME.role === "admin" && FEATURES.remote_sso;
function inviteMsg() {
  return `Olá! Para eu acessar o seu computador e preparar a formatação, abra este link nele e instale o programa indicado para o seu sistema (Windows, Mac ou Linux): ${INV.url}\n\nDepois é só deixar o computador ligado e conectado à internet. O link vale até ${fmtDate(INV.until / 1000)}.`;
}
function inviteCard() {
  if (!inviteReady()) return "";
  if (!INV) return `<div class="card inv"><div class="spread"><div class="inv-head">${osLogo("", "sm")}<div><b>Formatar um PC à distância</b>
      <p class="muted small">Mande um link para o cliente instalar o agente. Quando o PC aparecer, você grava o pendrive e formata pelo acesso remoto; o cliente só instala o agente e espeta o pendrive.</p></div></div>
      <div class="row inv-go"><select id="invHours" aria-label="Validade do link">${INV_HOURS.map(([h, t]) => `<option value="${h}" ${h === 24 ? "selected" : ""}>vale ${t}</option>`).join("")}</select>
      <button class="btn primary" id="invNew" type="button">Gerar link do agente</button></div></div></div>`;
  const found = INV.found.map(d => `<div class="inv-pc">${osLogo(d.os, "sm")}<div class="inv-pct"><b>${esc(d.name)}</b><span class="muted small">${esc(d.os || "")} · conectou agora</span></div>
      <button class="btn sm primary" data-invopen="${esc(d.id)}" type="button">Abrir tela</button></div>`).join("");
  return `<div class="card inv"><div class="spread"><b>Link do agente para o cliente</b><button class="btn sm" id="invClose" type="button">Fechar</button></div>
    <div class="cred mono u-mt8">${esc(INV.url)}</div>
    <div class="row u-mt12"><button class="btn primary" id="invCopy" type="button">Copiar link</button>
      <button class="btn" id="invMsg" type="button">Copiar mensagem pronta</button>
      <a class="btn" href="https://wa.me/?text=${encodeURIComponent(inviteMsg())}" target="_blank" rel="noopener noreferrer">Enviar pelo WhatsApp</a></div>
    <p class="muted small u-mt8">Vale até ${fmtDate(INV.until / 1000)}. O cliente abre no computador dele e instala o programa do sistema dele.</p>
    <div class="inv-wait">${found || `<span class="inv-spin" aria-hidden="true"></span><span class="muted small">Aguardando o computador do cliente aparecer…</span>`}</div></div>`;
}
function wireInvite(root = document) {
  const box = $("#invBox", root); if (!box) return;
  const nb = $("#invNew", box); if (nb) nb.onclick = () => inviteCreate(+$("#invHours", box).value);
  const cl = $("#invClose", box); if (cl) cl.onclick = () => { clearInterval(INV.poll); INV = null; refreshInvite(); };
  const copy = async (txt, ok) => { try { await navigator.clipboard.writeText(txt); toast(ok); } catch (e) { toast("Selecione o texto e copie."); } };
  const cp = $("#invCopy", box); if (cp) cp.onclick = () => copy(INV.url, "Link copiado!");
  const ms = $("#invMsg", box); if (ms) ms.onclick = () => copy(inviteMsg(), "Mensagem copiada: é só colar para o cliente.");
  $$("[data-invopen]", box).forEach(b => b.onclick = () => { const d = INV.found.find(x => x.id === b.dataset.invopen); if (d) openRemoteView(d, "desktop"); });
}
function refreshInvite() { const box = $("#invBox"); if (box) { box.innerHTML = inviteCard(); wireInvite(); } }
async function inviteCreate(hours) {
  const btn = $("#invNew"); if (btn) { btn.disabled = true; btn.textContent = "Gerando…"; }
  try {
    const known = new Set((await remoteDevices(true)).map(d => d.id));   // o que já existia antes do convite
    const { url } = await api("/api/admin/remoto/invite", { method: "POST", body: { hours } });
    if (INV) clearInterval(INV.poll);
    INV = { url, until: Date.now() + hours * 3600e3, known, found: [] };
    INV.poll = setInterval(async () => {
      if (!INV || document.hidden) return;
      if (Date.now() > INV.until) { clearInterval(INV.poll); return; }
      try {
        const fresh = (await remoteDevices(true)).filter(d => !INV.known.has(d.id) && !INV.found.some(f => f.id === d.id));
        if (fresh.length) { INV.found.push(...fresh); refreshInvite(); toast(`${fresh[0].name} conectou: já dá para acessar.`); }
      } catch (e) { }
    }, 10000);
    refreshInvite();
  } catch (e) { toast(e.message); if (btn) { btn.disabled = false; btn.textContent = "Gerar link do agente"; } }
}
// página Remoto: "Adicionar computador" usa o mesmo cartão
async function remoteInvite() {
  const box = $("#rInv"); box.id = "invBox";
  box.innerHTML = inviteCard(); wireInvite();
  if (!INV) inviteCreate(72);
}

// Janela lateral (igual à de Máquinas) com o visualizador do MeshCentral.
function openRemoteView(d, mode) {
  closeOverlay();
  OVL = { remote: d, tab: mode };
  const el = document.createElement("div");
  el.id = "ovl";
  el.innerHTML = `<div class="ovl-bg"></div><aside class="ovl ovl-wide" role="dialog" aria-modal="true">
    <header class="ovl-h"><div>${osLogo(d.os, "sm")} ${dot(d.online)} <b>${esc(d.name)}</b> <span class="muted small">${esc(d.os || "")}</span></div>
      <div class="row"><button class="btn sm" id="rvNew" title="Abrir em nova aba">↗</button><button class="btn sm" id="ovlClose" aria-label="Fechar">✕</button></div></header>
    <nav class="tabs" id="ovlTabs">${RMODES.map(([m, t]) => `<button data-t="${m}">${t}</button>`).join("")}</nav>
    <div class="ovl-b rv-body"><div class="rframe rv-frame"><div class="muted rload">Conectando…</div><iframe title="Remoto" allow="clipboard-read; clipboard-write; fullscreen"></iframe></div></div></aside>`;
  document.body.appendChild(el);
  document.body.classList.add("noscroll");
  const close = () => closeOverlay();
  $(".ovl-bg", el).onclick = close; $("#ovlClose").onclick = close;
  $$("#ovlTabs button", el).forEach(b => b.onclick = () => remoteMode(d, b.dataset.t));
  $("#rvNew").onclick = () => { const f = $(".rv-frame iframe"); if (f && f.src) window.open(f.src, "_blank", "noopener"); };
  remoteMode(d, mode);
}

async function remoteMode(d, mode, root = document) {
  if (OVL) OVL.tab = mode;
  if (OVL && OVL.remote) $$("#ovlTabs button[data-t]").forEach(b => b.classList.toggle("on", b.dataset.t === mode));
  const f = $(".rv-frame iframe", root);
  if (!f) return;
  const ld = $(".rv-frame .rload", root); if (ld) ld.style.display = "";
  try {
    f.onload = () => { const l = $(".rv-frame .rload", root); if (l) l.style.display = "none"; };
    f.src = (await api(`/api/remoto/view?node=${encodeURIComponent(d.id)}&mode=${mode}`)).url;
  } catch (e) { toast(e.message); }
}

// Máquinas: a mesma tela, dentro da janela do servidor (quando ele tem o agente).
function tabTela() {
  const d = OVL.rdev, P = OVL.m.perms ?? 63, bit = { desktop: 4, terminal: 2, files: 8 };
  const modes = RMODES.filter(([m]) => P & bit[m]);
  $("#ovlBody").innerHTML = `<div class="spread"><div class="row">${modes.map(([m, t]) => `<button class="btn sm" data-rm="${m}">${t}</button>`).join("")}</div>
      <span class="muted small">pelo Remoto (MeshCentral)</span></div>
    <div class="rframe rv-frame u-mt12"><div class="muted rload">Conectando…</div><iframe title="Remoto" allow="clipboard-read; clipboard-write; fullscreen"></iframe></div>`;
  const pick = m => { $$("#ovlBody [data-rm]").forEach(b => b.classList.toggle("primary", b.dataset.rm === m)); remoteMode(d, m, $("#ovlBody")); };
  $$("#ovlBody [data-rm]").forEach(b => b.onclick = () => pick(b.dataset.rm));
  pick(modes[0][0]);
}

function closeOverlay() {
  if (!OVL) return;
  stopTerminal();
  clearInterval(OVL.timer);
  $("#ovl")?.remove();
  document.body.classList.remove("noscroll");
  OVL = null;
}
document.addEventListener("keydown", e => { if (e.key === "Escape" && OVL && !OVL.ws) closeOverlay(); });

async function openMachine(id, tab = "resumo") {
  closeOverlay();
  OVL = { id, tab };
  const el = document.createElement("div");
  el.id = "ovl";
  el.innerHTML = `<div class="ovl-bg"></div><aside class="ovl" role="dialog" aria-modal="true">
    <header class="ovl-h"><div id="ovlTitle">Carregando…</div><button class="btn sm" id="ovlClose" aria-label="Fechar">✕</button></header>
    <nav class="tabs" id="ovlTabs">
      <button data-t="resumo">Resumo</button><button data-t="terminal">Terminal</button>
      <button data-t="vpn">VPN (WireGuard)</button><button data-t="conectar">Conectar do meu computador</button><button data-t="config">Configurações</button></nav>
    <div class="ovl-b" id="ovlBody"></div></aside>`;
  document.body.appendChild(el);
  document.body.classList.add("noscroll");
  $(".ovl-bg", el).onclick = () => { if (!OVL.ws || confirm("Fechar e encerrar o terminal?")) closeOverlay(); };
  $("#ovlClose").onclick = () => { if (!OVL.ws || confirm("Fechar e encerrar o terminal?")) closeOverlay(); };
  $$("#ovlTabs button").forEach(b => b.onclick = () => { if (OVL.tab === "terminal" && b.dataset.t !== "terminal" && OVL.ws && !confirm("Sair do terminal encerra a sessão. Continuar?")) return; showTab(b.dataset.t); });
  await refreshMachine();
  // só as abas que a conta pode usar (o admin pode tudo; o sócio, o que foi liberado)
  const P = OVL.m.perms ?? 63, need = { terminal: 2, vpn: 16, config: 32 };
  $$("#ovlTabs button").forEach(b => { const n = need[b.dataset.t]; if (n && !(P & n)) b.remove(); });
  if (need[tab] && !(P & need[tab])) tab = "resumo";
  // com o agente do Remoto no servidor: aba "Tela" (mesmo nome de computador)
  if (FEATURES.remote_sso && (P & 14)) {
    try {
      const h = (OVL.m.hostname || "").toLowerCase();
      OVL.rdev = (await remoteDevices(false)).find(x => x.name.toLowerCase() === h && x.online);
    } catch (e) { }
    if (OVL && OVL.rdev) {
      $("#ovlTabs").insertAdjacentHTML("afterbegin", `<button data-t="tela">Tela</button>`);  // o Remoto aplica as permissões do sócio
      $("#ovlTabs button[data-t=tela]").onclick = () => {
        if (OVL.tab === "terminal" && OVL.ws && !confirm("Sair do terminal encerra a sessão. Continuar?")) return;
        showTab("tela");
      };
    }
  }
  showTab(tab);
  OVL.timer = setInterval(() => { if (OVL && OVL.tab === "resumo" && !document.hidden) refreshMachine().then(() => OVL && OVL.tab === "resumo" && showTab("resumo")); }, 15000);
}

async function refreshMachine() {
  const d = await api("/api/machines/" + OVL.id);
  OVL.m = d.machine; OVL.panel = d.panel;
  const m = OVL.m;
  $("#ovlTitle").innerHTML = `${osLogo(m.os, "sm")} ${dot(m.online, mstate(m))} <b>${esc(m.label || m.hostname)}</b> <span class="muted small">${mstate(m)} · ${m.off_at ? "avisou " + fmtAgo(m.off_at) : "visto " + fmtAgo(m.last_seen)}</span>${m.archived_at ? ` <span class="pill">arquivada</span>` : ""}`;
}

function showTab(t) {
  if (OVL.tab === "terminal" && t !== "terminal") stopTerminal();
  OVL.tab = t;
  $$("#ovlTabs button").forEach(b => b.classList.toggle("on", b.dataset.t === t));
  ({ tela: tabTela, resumo: tabResumo, terminal: tabTerminal, vpn: tabVpn, conectar: tabConectar, config: tabConfig })[t]();
}

const kv = (k, v) => `<div class="kv"><span>${k}</span><b>${v}</b></div>`;

// Aviso enquanto o provisionamento não terminou: "não reinicie" (rodando) ou o que falta.
function provBanner(m) {
  const pv = m.provisioned || "", st = m.steps || {};
  if (pv.startsWith("ok") || !m.online) return "";
  const plan = m.provision_plan || [], done = plan.filter(k => String(st[k] || "").startsWith("ok")).length;
  const count = plan.length ? ` (${done} de ${plan.length} etapas)` : "";
  const failed = Object.entries(st).filter(([, v]) => String(v).startsWith("falhou"));
  if (m.provision_current || (m.services || {})["uab-provision"] && !failed.length)
    return `<div class="note warn"><b>Instalando os programas${count}: evite reiniciar.</b>${m.provision_current ? ` Agora: <span class="mono">${esc(m.provision_current)}</span>.` : ""}
      Dá para usar a máquina normalmente; se reiniciar, a instalação continua sozinha no próximo boot.</div>`;
  return `<div class="note warn"><b>Instalação pendente${count}.</b> ${failed.length ? `Falhou: ${failed.map(([k]) => `<span class="mono">${esc(k)}</span>`).join(", ")}. ` : ""}A máquina tenta de novo sozinha a cada minuto (e a cada boot); o motivo aparece em Provisionamento, no Resumo.</div>`;
}

function tabResumo() {
  const m = OVL.m, r = m.resources || {}, sv = m.services || {};
  const gb = n => n ? (n / 1e9).toFixed(1) + " GB" : "—";
  const svc = (n, on) => `<span class="pill ${on ? "done" : "error"}">${n} ${on ? "ativo" : "inativo"}</span>`;
  if (m.platform === "windows") return tabResumoWin(m, r, sv, gb, svc);
  $("#ovlBody").innerHTML = `${provBanner(m)}
    ${appLinks(m)}
    <div class="grid2">
      ${kv("Conta", esc(m.account || "—"))}${kv("ISO gerada por", esc(m.owner_email || "—"))}
      ${kv("Hostname", esc(m.hostname))}${kv("Usuário admin", `<span class="mono">${esc(m.admin_user)}</span>`)}${m.terminalsec ? kv("TerminalSec", tsecBadge(m, true)) : ""}
      ${kv("IP Tailscale", m.tailscale_ip ? `<span class="mono">${esc(m.tailscale_ip)}</span>` : "—")}${kv("Nome Tailscale", esc(m.tailscale_dns || "—"))}
      ${kv("Rede local", esc(m.local_ips.map(x => x.split(" ").pop()).join(", ") || "—"))}${kv("Gateway", esc(m.report.gateway || "—"))}
      ${kv("Sistema", `${osLogo(m.os, "xs")} ${esc(m.os)}`)}${kv("Kernel", esc(m.kernel))}
      ${kv("Ligado há", fmtUptime(m.uptime))}${kv("Acesso SSH", (m.ssh_auth === "password" ? "usuário e senha (rede local/Tailscale)" : "chave SSH") + " · porta " + (m.ssh_port_real || 22))}
      ${kv("CPU", `${r.cpus || "—"} núcleos · carga ${r.load1 ?? "—"}`)}${kv("Memória", `${gb(r.mem_available)} livres de ${gb(r.mem_total)}`)}
      ${kv("Disco /", `${gb(r.disk_free)} livres de ${gb(r.disk_total)}`)}${kv("Primeiro contato", fmtDate(m.first_seen))}
      ${(m.steps || {}).gpu_info ? kv("Placa de vídeo", esc(m.steps.gpu_info)) : ""}
    </div>
    <h3>Serviços</h3><div class="row">${svc("SSH", sv.ssh)}${svc("Tailscale", sv.tailscaled)}${m.apps.docker ? svc("Docker", sv.docker) : ""}${m.apps.coolify ? svc("Coolify", sv.coolify) : ""}${m.apps.cockpit ? svc("Cockpit", sv.cockpit) : ""}${m.ssh_auth === "password" ? svc("fail2ban", sv.fail2ban) : ""}${"ddclient" in sv ? svc("DDNS", sv.ddclient) : ""}${"cloudflared" in sv ? svc("Cloudflare Tunnel", sv.cloudflared) : ""}${"libvirtd" in sv ? svc("Virtualização", sv.libvirtd) : ""}${"nvidia" in sv ? svc("GPU NVIDIA", sv.nvidia) : ""}${"meshagent" in sv ? svc("Remoto", sv.meshagent) : ""}
      ${m.report.docker_containers != null ? `<span class="pill">${m.report.docker_containers} container(s)</span>` : ""}</div>
    <h3>Provisionamento</h3>
    <div class="table-wrap"><table>${Object.entries(m.steps || {}).map(([k, v]) => `<tr><td>${esc(k)}</td><td>${String(v).startsWith("ok") || /^100\./.test(v) ? `<span class="ok">${esc(v)}</span>` : String(v).startsWith("falhou") ? `<span class="err">${esc(v)}</span>` : esc(v)}</td></tr>`).join("") || "<tr><td class='muted'>sem dados</td></tr>"}
      <tr><td><b>geral</b></td><td>${esc(m.provisioned || "—")}</td></tr></table></div>
    ${m.downloads.length ? `<h3>Quem baixou</h3><div class="table-wrap"><table>${m.downloads.slice().reverse().map(x => `<tr><td>${esc(x.email)}</td><td class="small">${esc(x.kind === "iso" ? "ISO" : "gravador " + x.kind.replace("gravador-", ""))}</td><td class="small">${fmtDate(x.at)}</td></tr>`).join("")}</table></div>` : ""}
    <div class="row u-mt16"><button class="btn primary" id="goTerm">Abrir terminal</button><button class="btn" id="goConn">Conectar do meu computador</button></div>`;
  $("#goTerm").onclick = () => showTab("terminal");
  $("#goConn").onclick = () => showTab("conectar");
}

// Resumo de uma máquina Windows: sem kernel, Tailscale, Docker e afins (só o que faz sentido lá)
function tabResumoWin(m, r, sv, gb, svc) {
  $("#ovlBody").innerHTML = `${provBanner(m)}
    <div class="grid2">
      ${kv("Conta", esc(m.account || "—"))}${kv("Configuração gerada por", esc(m.owner_email || "—"))}
      ${kv("Nome do computador", esc(m.hostname))}${kv("Usuário admin", `<span class="mono">${esc(m.admin_user)}</span>`)}
      ${kv("Sistema", `${osLogo(m.os, "xs")} ${esc(m.os || "Windows")}`)}${kv("Versão", esc(m.kernel || "—"))}
      ${kv("Rede local", esc(m.local_ips.map(x => x.split(" ").pop()).join(", ") || "—"))}${kv("IP de origem", `<span class="mono">${esc(m.source_ip || "—")}</span>`)}
      ${kv("Ligado há", fmtUptime(m.uptime))}${kv("OpenSSH", m.panel_access ? `chave do painel · porta ${m.ssh_port_real || 22} · PowerShell` : "sem acesso pelo painel")}
      ${m.wg || m.build_wg ? kv("VPN (WireGuard)", m.wg ? `<span class="mono">${esc(m.wg.server_ip)}</span> ${m.wg.connected ? `<span class="ok small">conectado</span>` : `<span class="err small">sem conexão</span>`}` : `<span class="muted">aguardando o registro no hub</span>`) : ""}
      ${m.tailscale_ip || m.build_ts ? kv("Tailscale", m.tailscale_ip ? `<span class="mono">${esc(m.tailscale_ip)}</span>${m.tailscale_dns ? ` <span class="muted small">${esc(m.tailscale_dns)}</span>` : ""}` : `<span class="muted">${esc(m.tailscale_state || "não conectado")}</span>`) : ""}
      ${kv("CPU", `${r.cpus || "—"} núcleos`)}${kv("Memória", `${gb(r.mem_available)} livres de ${gb(r.mem_total)}`)}
      ${kv("Disco C:", `${gb(r.disk_free)} livres de ${gb(r.disk_total)}`)}${kv("Primeiro contato", fmtDate(m.first_seen))}
    </div>
    <h3>Serviços</h3><div class="row">${"ssh" in sv || m.panel_access ? svc("OpenSSH", sv.ssh) : ""}${"meshagent" in sv ? svc("Remoto", sv.meshagent) : ""}${"wireguard" in sv ? svc("WireGuard", sv.wireguard) : ""}${"tailscaled" in sv ? svc("Tailscale", sv.tailscaled) : ""}</div>
    <h3>Provisionamento</h3>
    <div class="table-wrap"><table>${Object.entries(m.steps || {}).map(([k, v]) => `<tr><td>${esc(WIN_STEP[k] || k)}</td><td>${String(v).startsWith("ok") ? `<span class="ok">${esc(v)}</span>` : /^(falhou|desistiu)/.test(String(v)) ? `<span class="err">${esc(v)}</span>` : esc(v)}</td></tr>`).join("") || "<tr><td class='muted'>sem dados</td></tr>"}
      <tr><td><b>geral</b></td><td>${esc(m.provisioned || "—")}</td></tr></table></div>
    ${m.downloads && m.downloads.length ? `<h3>Quem baixou</h3><div class="table-wrap"><table>${m.downloads.slice().reverse().map(x => `<tr><td>${esc(x.email)}</td><td class="small">${esc(x.kind === "config" ? "autounattend.xml" : "gravador " + x.kind.replace("gravador-", ""))}</td><td class="small">${fmtDate(x.at)}</td></tr>`).join("")}</table></div>` : ""}
    <div class="row u-mt16">${OVL.rdev ? `<button class="btn primary" id="goTela">Abrir tela (Remoto)</button>` : ""}<button class="btn ${OVL.rdev ? "" : "primary"}" id="goTerm">Abrir terminal</button><button class="btn" id="goConn">Conectar do meu computador</button></div>`;
  const gt = $("#goTela"); if (gt) gt.onclick = () => showTab("tela");
  $("#goTerm").onclick = () => showTab("terminal");
  $("#goConn").onclick = () => showTab("conectar");
}
// indicador do TerminalSec (monitoramento de segurança) na lista de máquinas
const TSEC_ST = { online: ["🟢", "protegido"], pending: ["🟡", "aguardando aprovação"], offline: ["🔴", "sem contato"], archived: ["⚪", "arquivado"] };
function tsecBadge(m, inline) {
  const t = m.terminalsec;
  if (!t) return "";
  const [ico, txt] = TSEC_ST[t.status] || ["⚪", t.status];
  const url = /^https?:\/\//.test(t.url || "") ? t.url : "#";
  return `${inline ? "" : "<br>"}<a class="small" href="${esc(url)}" target="_blank" rel="noopener" title="Abrir no TerminalSec">${ico} TerminalSec: ${esc(txt)}</a>`;
}

// nomes das etapas do provisionamento do Windows (docs/windows/CONTRACT.md §4)
const WIN_STEP = { openssh: "OpenSSH (terminal pelo painel)", wireguard: "WireGuard (túnel até o seu servidor)", tailscale_install: "Tailscale (instalação)",
  tailscale_login: "Tailscale (login na Tailnet)", remote_agent: "Agente do Remoto", apps: "Programas (winget)", activation: "Ativação do Windows", report: "Relatório ao painel" };

function appLinks(m) {
  const ip = m.tailscale_ip || sshTarget(m);
  if (!ip || !(m.apps.coolify || m.apps.cockpit)) return "";
  return `<div class="row">${m.apps.coolify ? `<a class="btn sm" href="http://${esc(ip)}:8000" target="_blank" rel="noopener">Abrir Coolify ↗</a>` : ""}
    ${m.apps.cockpit ? `<a class="btn sm" href="https://${esc(ip)}:9090" target="_blank" rel="noopener">Abrir Cockpit ↗</a>` : ""}
    <span class="muted small">abre direto no endereço da máquina (seu computador precisa estar na Tailnet)</span></div>`;
}

/* ---------- terminal no navegador (xterm.js via WebSocket) */
function loadXterm() {
  if (window.Terminal && window.FitAddon) return Promise.resolve();
  const add = (tag, attrs) => new Promise((ok, fail) => { const e = document.createElement(tag); Object.assign(e, attrs); e.onload = ok; e.onerror = fail; document.head.appendChild(e); });
  return Promise.all([
    add("link", { rel: "stylesheet", href: "/static/vendor/xterm.css" }),
    add("script", { src: "/static/vendor/xterm.js" }).then(() => add("script", { src: "/static/vendor/addon-fit.js" })),
  ]);
}

function stopTerminal() {
  if (!OVL) return;
  try { OVL.ws && OVL.ws.close(); } catch (e) { }
  try { OVL.ro && OVL.ro.disconnect(); } catch (e) { }
  try { OVL.term && OVL.term.dispose(); } catch (e) { }
  OVL.ws = OVL.term = OVL.ro = OVL.fit = null;
}

function tabTerminal() {
  const m = OVL.m, p = OVL.panel, win = m.platform === "windows";
  const viaWg = m.wg && m.wg.connected;
  const viaTs = p.state === "Running" && m.tailscale_ip;
  const reason = !m.panel_access ? (win ? "Esta configuração foi gerada sem “terminal pelo painel”: o OpenSSH não recebeu a chave do painel." + (OVL.rdev ? " Use a aba Tela (Remoto), que também tem terminal." : "")
      : "Esta ISO foi gerada sem “acesso pelo painel”: a chave do painel não está no servidor. Use “Conectar do meu computador”.")
    : m.ssh_host || viaWg || viaTs ? ""
    : m.wg ? (m.wg_reason || "O túnel WireGuard desta máquina está desconectado agora.") + (win && OVL.rdev ? " Enquanto isso, use a aba Tela (Remoto), que também tem terminal." : "")
    : win && m.tailscale_ip ? (p.message || "O painel não está na Tailnet.")
    : win ? (m.build_wg ? "O PC ainda não se conectou ao hub WireGuard (acontece na pós-instalação, com internet)." : "Este PC não tem túnel (WireGuard/Tailscale) conectado.") + " Em Configurações, dá para informar um endereço que o painel alcance (ex.: o IP da rede local, se o painel estiver na mesma rede)." + (OVL.rdev ? " Ou use a aba Tela (Remoto), que também tem terminal." : "")
    : !m.tailscale_ip ? "A máquina ainda não informou IP do Tailscale nem se conectou ao hub WireGuard. Aguarde ou defina um endereço em Configurações."
    : (p.message || "O painel não está na Tailnet.");
  $("#ovlBody").innerHTML = `${provBanner(m)}
    <div class="spread"><p class="muted small">Entra como <b class="mono">${esc(m.admin_user)}</b> em <b class="mono">${esc(sshTarget(m))}</b> usando a chave do painel (sem digitar IP, usuário ou senha). ${win ? "Abre o <b>PowerShell</b> do Windows, já como administrador." : "Para comandos de administrador: <code>sudo</code> (pede a senha do servidor)."}</p>
      <div class="row"><button class="btn primary sm" id="tConn" ${reason && !m.ssh_host ? "disabled" : ""}>Conectar</button><button class="btn sm" id="tDisc" disabled>Desconectar</button></div></div>
    ${reason ? `<div class="note warn">${esc(reason)}</div>` : ""}
    <div id="term" class="term"></div><div id="tStat" class="muted small u-mt8"></div>`;
  $("#tConn").onclick = startTerminal;
  $("#tDisc").onclick = () => { stopTerminal(); $("#tStat").textContent = "Desconectado."; $("#tConn").disabled = false; $("#tDisc").disabled = true; };
  if (!reason) startTerminal();  // já conecta ao abrir a aba
}

async function startTerminal() {
  stopTerminal();
  $("#tConn").disabled = true;
  $("#tStat").textContent = "Carregando terminal…";
  try { await loadXterm(); } catch (e) { $("#tStat").textContent = "Não foi possível carregar o terminal."; $("#tConn").disabled = false; return; }
  const term = new window.Terminal({ cursorBlink: true, fontSize: 14, fontFamily: "ui-monospace, SFMono-Regular, Menlo, Consolas, monospace", theme: { background: "#0d1117" }, scrollback: 5000 });
  const fit = new window.FitAddon.FitAddon();
  term.loadAddon(fit);
  term.open($("#term"));
  fit.fit();
  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(`${proto}//${location.host}/api/machines/${OVL.id}/terminal`);
  ws.binaryType = "arraybuffer";
  OVL.term = term; OVL.ws = ws; OVL.fit = fit;
  const send = o => { if (ws.readyState === 1) ws.send(JSON.stringify(o)); };
  ws.onopen = () => { send({ t: "r", c: term.cols, r: term.rows }); $("#tDisc").disabled = false; term.focus(); };
  ws.onmessage = ev => {
    if (typeof ev.data === "string") {
      const msg = JSON.parse(ev.data);
      if (msg.t === "s") { $("#tStat").textContent = msg.d; term.writeln(`\x1b[90m${msg.d}\x1b[0m`); }
    } else term.write(new Uint8Array(ev.data));
  };
  ws.onclose = () => { if (OVL && OVL.ws === ws) { OVL.ws = null; $("#tConn").disabled = false; $("#tDisc").disabled = true; } };
  term.onData(d => send({ t: "i", d }));
  term.onResize(({ cols, rows }) => send({ t: "r", c: cols, r: rows }));
  OVL.ro = new ResizeObserver(() => { try { fit.fit(); } catch (e) { } });
  OVL.ro.observe($("#term"));
}

/* ---------- VPN WireGuard pelo seu servidor */
async function tabVpn() {
  $("#ovlBody").innerHTML = "Carregando…";
  let d;
  try { d = await api(`/api/machines/${OVL.id}/wg`); } catch (e) { $("#ovlBody").innerHTML = `<div class="note err">${esc(e.message)}</div>`; return; }
  const m = OVL.m, w = d.machine, win = m.platform === "windows";
  if (!d.enabled) { $("#ovlBody").innerHTML = `<div class="note warn">WireGuard pelo seu servidor está desligado neste painel. Defina <code>HUB_ENDPOINT</code> no Coolify (e abra a porta UDP no roteador).</div>`; return; }
  if (!w) { $("#ovlBody").innerHTML = `<div class="note">Esta máquina ainda não se registrou no hub. Isso acontece sozinho ${win ? "na pós-instalação do Windows se a configuração" : "no primeiro boot se a ISO"} foi gerada com “WireGuard pelo seu servidor”${d.hub.online ? "" : " — e o hub precisa estar no ar (" + esc(d.hub.message || "") + ")"}.</div>`; return; }
  const lans = (m.local_ips || []).map(x => { const [ifn, cidr] = x.split(" "); return { ifn, cidr }; }).filter(x => x.cidr && x.cidr.endsWith("/24"));
  $("#ovlBody").innerHTML = `
    <div class="grid2">
      ${kv("Túnel", w.connected ? `<span class="ok">● conectado</span> <span class="muted small">(${fmtAgo(w.last_handshake)})</span>` : `<span class="err">● sem conexão</span> <span class="muted small">${w.last_handshake ? "último contato " + fmtAgo(w.last_handshake) : "nunca conectou"}</span>`)}
      ${kv(win ? "IP do PC na VPN" : "IP do servidor na VPN", `<span class="mono">${esc(w.server_ip)}</span>`)}
      ${kv("Hub", d.hub.online ? `<span class="ok">no ar</span> · <span class="mono small">${esc(d.endpoint)}</span>` : `<span class="err">${esc(d.hub.message || "fora do ar")}</span>`)}
      ${kv("Rede do cliente", win ? "não disponível no Windows" : w.lan ? `<span class="mono">${esc(w.lan_cidr)}</span> → <span class="mono">${esc(w.lan_map)}</span>` : "desligada")}
    </div>
    ${!w.connected && m.wg_reason ? `<div class="note warn u-mt12"><b>Por que não conecta:</b> ${esc(m.wg_reason)}</div>` : ""}
    ${m.wg_note ? `<div class="note u-mt12">${esc(m.wg_note)}</div>` : ""}
    <h3>Conectar um dispositivo seu (Mac, Windows, celular)</h3>
    <p class="small muted">Gera uma configuração só para ${win ? "este PC" : "este servidor"}. Instale o app oficial do WireGuard, importe o arquivo ou escaneie o QR code e ative. A chave privada é mostrada <b>uma única vez</b>.</p>
    <form id="wgNew" class="row"><input name="name" placeholder="ex.: MacBook do suporte" maxlength="40"><button class="btn primary" type="submit">Gerar acesso</button></form>
    <div id="wgOut"></div>
    <h3>Dispositivos com acesso</h3>
    <div id="wgList">${d.clients.length ? `<div class="table-wrap"><table>${d.clients.map(c => `<tr><td><b>${esc(c.name)}</b><br><span class="muted small mono">${esc(c.ip)}</span></td><td class="small">${c.last_handshake ? "último uso " + fmtAgo(c.last_handshake) : "nunca conectou"}</td><td class="small">${fmtDate(c.created_at)}</td><td><button class="btn sm danger" data-rev="${c.id}">Revogar</button></td></tr>`).join("")}</table></div>` : `<p class="muted small">Nenhum ainda.</p>`}</div>
    ${win ? `<h3>Acesso à rede local do cliente</h3>
    <p class="small muted">Ainda não existe no Windows: pela VPN você alcança só o próprio PC (<span class="mono">${esc(w.server_ip)}</span>), não os outros equipamentos da rede do cliente. Se precisar disso, use um servidor Ubuntu instalado por este painel na mesma rede.</p>` : `<h3>Acesso à rede local do cliente (opcional)</h3>
    <p class="small muted">Com isso ligado, pela VPN você alcança também os equipamentos da rede do cliente (impressoras, câmeras, sistemas). Para não conflitar com redes iguais de outros clientes, cada rede ganha um endereço próprio: <span class="mono">192.168.0.50</span> vira <span class="mono">${esc(w.lan_map.replace(/\.0\/24$/, ".50"))}</span>. Aplicado pelo servidor em até 10 min; depois, gere o acesso do dispositivo de novo para incluir essa rota.</p>
    ${lans.length ? `<form id="wgLan" class="row"><select name="lan">${lans.map(l => `<option value="${esc(l.ifn)}|${esc(l.cidr)}" ${w.lan_if === l.ifn ? "selected" : ""}>${esc(l.ifn)} · ${esc(l.cidr)}</option>`).join("")}</select>
      <button class="btn ${w.lan ? "" : "primary"}" type="submit" name="on" value="1">${w.lan ? "Atualizar" : "Ligar acesso à rede"}</button>
      ${w.lan ? `<button class="btn danger" type="button" id="wgLanOff">Desligar</button>` : ""}</form>`
      : `<p class="muted small">A máquina não informou nenhuma rede local /24.</p>`}`}`;
  $("#wgNew").onsubmit = async ev => {
    ev.preventDefault();
    try {
      const c = await api(`/api/machines/${OVL.id}/wg/clients`, { method: "POST", body: { name: ev.target.name.value } });
      const blob = URL.createObjectURL(new Blob([c.config], { type: "text/plain" }));
      const fname = (m.label || m.hostname).replace(/[^a-z0-9-]/gi, "-").slice(0, 30) + ".conf";
      $("#wgOut").innerHTML = `<div class="card"><div class="wgqr">${c.qr ? `<img src="${c.qr}" alt="QR code da configuração WireGuard" width="260" height="260">` : ""}
        <div><b>Acesso “${esc(c.name)}” criado</b>
        <ol class="small"><li>Instale o WireGuard: <a href="https://apps.apple.com/app/wireguard/id1451685025" target="_blank" rel="noopener">Mac</a> · <a href="https://www.wireguard.com/install/" target="_blank" rel="noopener">Windows/Linux</a> · <a href="https://apps.apple.com/app/wireguard/id1441195209" target="_blank" rel="noopener">iPhone</a> · <a href="https://play.google.com/store/apps/details?id=com.wireguard.android" target="_blank" rel="noopener">Android</a></li>
        <li>Celular: escaneie o QR code. Computador: baixe o arquivo e use “Importar túnel de arquivo”.</li>
        <li>${win ? `Ative o túnel: o PC fica em <span class="mono">${esc(c.server_ip)}</span> (ex.: Área de Trabalho Remota, se ligada no PC, ou pastas compartilhadas). O OpenSSH do PC aceita só a chave do painel: para o PowerShell, use o terminal no navegador.`
          : `Ative o túnel e conecte: <code>ssh ${esc(m.admin_user)}@${esc(c.server_ip)}</code>${c.lan_map ? ` · rede do cliente em <span class="mono">${esc(c.lan_map)}</span>` : ""}`}</li></ol>
        <div class="row"><a class="btn primary" href="${blob}" download="${esc(fname)}">⬇ Baixar ${esc(fname)}</a><button class="btn" id="wgCopy">Copiar configuração</button></div>
        <p class="muted small u-mt8">Guarde agora: por segurança a chave privada não fica no painel. Perdeu? Revogue e gere outro.</p></div></div></div>`;
      $("#wgCopy").onclick = async () => { try { await navigator.clipboard.writeText(c.config); toast("Copiado!"); } catch (e) { toast("Não foi possível copiar."); } };
      const l = await api(`/api/machines/${OVL.id}/wg`);
      $("#wgList").innerHTML = `<div class="table-wrap"><table>${l.clients.map(x => `<tr><td><b>${esc(x.name)}</b><br><span class="muted small mono">${esc(x.ip)}</span></td><td class="small">${x.last_handshake ? "último uso " + fmtAgo(x.last_handshake) : "nunca conectou"}</td><td class="small">${fmtDate(x.created_at)}</td><td><button class="btn sm danger" data-rev="${x.id}">Revogar</button></td></tr>`).join("")}</table></div>`;
      wireRevoke();
    } catch (e) { toast(e.message); }
  };
  const wireRevoke = () => $$("#wgList [data-rev]").forEach(b => b.onclick = async () => {
    if (!confirm("Revogar este acesso? O dispositivo perde a conexão na hora.")) return;
    await api(`/api/machines/${OVL.id}/wg/clients/${b.dataset.rev}`, { method: "DELETE" }); tabVpn();
  });
  wireRevoke();
  const lf = $("#wgLan");
  if (lf) {
    lf.onsubmit = async ev => { ev.preventDefault(); const [iface, cidr] = lf.lan.value.split("|"); try { await api(`/api/machines/${OVL.id}/wg/lan`, { method: "POST", body: { enabled: true, iface, cidr } }); toast("Acesso à rede ligado (o servidor aplica em até 10 min)."); tabVpn(); } catch (e) { toast(e.message); } };
    const off = $("#wgLanOff"); if (off) off.onclick = async () => { await api(`/api/machines/${OVL.id}/wg/lan`, { method: "POST", body: { enabled: false } }); tabVpn(); };
  }
}

/* ---------- conectar do próprio computador */
function tabConectar() {
  const m = OVL.m, os = detectOS();
  if (m.platform === "windows") return tabConectarWin(m, os);
  const host = sshTarget(m), user = m.admin_user;
  const port = m.ssh_host ? +m.ssh_port || 22 : m.ssh_port_real || 22;
  const cmd = `ssh ${port !== 22 ? `-p ${port} ` : ""}${user}@${host}`;
  const dnsCmd = m.tailscale_dns ? `ssh ${m.ssh_port_real !== 22 ? `-p ${m.ssh_port_real} ` : ""}${user}@${m.tailscale_dns.split(".")[0]}` : "";
  const tsDl = { windows: "https://tailscale.com/download/windows", "windows-arm64": "https://tailscale.com/download/windows", macos: "https://tailscale.com/download/mac", linux: "https://tailscale.com/download/linux", mobile: "https://tailscale.com/download" }[os];
  const term = { windows: "PowerShell ou Terminal do Windows (o cliente ssh já vem no Windows 10/11)", "windows-arm64": "PowerShell ou Terminal do Windows", macos: "Terminal", linux: "terminal", mobile: "um app de SSH (Termius, Blink, JuiceSSH)" }[os];
  $("#ovlBody").innerHTML = `
    <div class="dl">
      ${m.wg ? `<div class="stepbox"><span class="n">1</span><div><h2>Ative a VPN desta máquina</h2>
        <p class="small">Na aba <b>VPN (WireGuard)</b>, gere um acesso para este computador, importe no app WireGuard e ative o túnel. O servidor fica em <b class="mono">${esc(m.wg.server_ip)}</b>.</p>
        <button class="btn primary" type="button" id="goVpn">Abrir aba VPN</button>
        ${m.tailscale_ip ? `<p class="muted small u-mt8">Ou, pela sua Tailnet: <span class="mono">${esc(m.tailscale_ip)}</span>.</p>` : ""}</div></div>` : ""}
      <div class="stepbox ${m.wg && !m.tailscale_ip ? "hidden" : ""}"><span class="n">${m.wg ? "1b" : "1"}</span><div><h2>Seu computador na mesma Tailnet</h2>
        <p class="small">O servidor está na sua Tailnet${m.tailscale_ip ? ` com o IP <b class="mono">${esc(m.tailscale_ip)}</b>` : ""}. Para alcançá-lo, este computador também precisa estar nela (mesma conta).</p>
        <div class="row"><a class="btn primary" href="${tsDl}" target="_blank" rel="noopener">⬇ Instalar Tailscale (${OS_NAME[os]})</a>
        <a class="btn" href="https://login.tailscale.com/admin/machines" target="_blank" rel="noopener">Ver máquinas da Tailnet</a></div>
        <p class="muted small u-mt8">Depois de instalar, clique em <b>Log in</b> e entre com a mesma conta. No painel da Tailnet você deve ver <b>${esc(m.tailscale_dns.split(".")[0] || m.hostname)}</b>.</p></div></div>
      <div class="stepbox"><span class="n">2</span><div><h2>Abra a conexão SSH</h2>
        <p class="small">Clique para abrir direto no ${esc(term)}:</p>
        <div class="row"><a class="btn primary" href="ssh://${encodeURIComponent(user)}@${esc(host)}${port !== 22 ? ":" + port : ""}">▶ Abrir SSH no Terminal</a>
          <button class="btn" data-copy="${esc(cmd)}">Copiar comando</button></div>
        <pre class="log u-mt8">${esc(cmd)}${dnsCmd ? `\n# com MagicDNS ligado:\n${esc(dnsCmd)}` : ""}</pre>
        <p class="muted small">${m.ssh_auth === "password" ? `Usuário <b class="mono">${esc(user)}</b> já preenchido; quando pedir, digite a <b>senha que você definiu ao gerar a ISO</b> (o painel não guarda senhas).` : `Entra com a sua chave SSH (a que foi cadastrada na ISO). Se aparecer “Permission denied (publickey)”, este computador não tem a chave: use o terminal no navegador ou adicione a chave com <code>uab-add-ssh-key</code>.`}
        ${os.startsWith("windows") ? " No Windows, se o botão não abrir nada, use “Copiar comando” e cole no PowerShell." : ""}</p></div></div>
      <div class="stepbox"><span class="n">3</span><div><h2>Ou sem instalar nada</h2>
        <p class="small">Use o terminal no navegador: o painel já está na Tailnet e entra pela chave dele.</p>
        <button class="btn" id="goT">Abrir terminal no navegador</button></div></div>
    </div>`;
  $$("#ovlBody [data-copy]").forEach(b => b.onclick = async () => { try { await navigator.clipboard.writeText(b.dataset.copy); toast("Copiado!"); } catch (e) { toast(b.dataset.copy); } });
  $("#goT").onclick = () => showTab("terminal");
  const gv = $("#goVpn"); if (gv) gv.onclick = () => showTab("vpn");
}

// Windows: Remoto (tela), VPN (WireGuard/Tailscale) até o PC e o terminal no navegador.
// O OpenSSH do PC aceita só a chave do painel (sem senha), por isso o SSH direto fica
// para quem cadastrar a própria chave.
function tabConectarWin(m, os) {
  const host = sshTarget(m) || "IP-DO-PC", user = m.admin_user, port = m.ssh_host ? +m.ssh_port || 22 : m.ssh_port_real || 22;
  const cmd = `ssh ${port !== 22 ? `-p ${port} ` : ""}${user}@${host}`;
  let n = 0;
  const box = (title, body) => `<div class="stepbox"><span class="n">${++n}</span><div><h2>${title}</h2>${body}</div></div>`;
  const tsDl = { windows: "https://tailscale.com/download/windows", "windows-arm64": "https://tailscale.com/download/windows", macos: "https://tailscale.com/download/mac", linux: "https://tailscale.com/download/linux", mobile: "https://tailscale.com/download" }[os];
  $("#ovlBody").innerHTML = `
    <div class="dl">
      ${OVL.rdev ? box("Pelo Remoto (de qualquer lugar)", `<p class="small">Tela, terminal e arquivos pelo navegador, sem instalar nada e sem abrir portas.</p>
        <button class="btn primary" id="goTela">Abrir tela</button>`) : ""}
      ${m.wg ? box("Pela VPN do seu servidor (WireGuard)", `<p class="small">Na aba <b>VPN (WireGuard)</b>, gere um acesso para este computador, importe no app WireGuard e ative o túnel. O PC fica em <b class="mono">${esc(m.wg.server_ip)}</b> (ex.: Área de Trabalho Remota, se ligada no PC).</p>
        <button class="btn" type="button" id="goVpn">Abrir aba VPN</button>`) : ""}
      ${m.tailscale_ip ? box("Pela sua Tailnet", `<p class="small">O PC está na sua Tailnet com o IP <b class="mono">${esc(m.tailscale_ip)}</b>${m.tailscale_dns ? ` (<span class="mono">${esc(m.tailscale_dns.split(".")[0])}</span>)` : ""}. Seu computador precisa estar na mesma Tailnet.</p>
        <a class="btn" href="${tsDl}" target="_blank" rel="noopener">⬇ Instalar Tailscale (${OS_NAME[os]})</a>`) : ""}
      ${box("Terminal no navegador (PowerShell)", `<p class="small">O painel entra pela chave dele${m.wg || m.tailscale_ip ? " pelo túnel" : ", se alcançar o endereço do PC (defina em Configurações)"}. Abre o PowerShell já como administrador.</p>
        <button class="btn primary" id="goT">Abrir terminal no navegador</button>`)}
      ${box("SSH do seu computador", `<p class="small">O OpenSSH do PC aceita <b>só chave</b> (sem senha). Para entrar do seu computador, adicione a sua chave pública pelo terminal do navegador em <code>C:\\ProgramData\\ssh\\administrators_authorized_keys</code> e use:</p>
        <div class="row"><button class="btn" data-copy="${esc(cmd)}">Copiar comando</button></div>
        <pre class="log u-mt8">${esc(cmd)}</pre>`)}
    </div>`;
  $$("#ovlBody [data-copy]").forEach(b => b.onclick = async () => { try { await navigator.clipboard.writeText(b.dataset.copy); toast("Copiado!"); } catch (e) { toast(b.dataset.copy); } });
  $("#goT").onclick = () => showTab("terminal");
  const gt = $("#goTela"); if (gt) gt.onclick = () => showTab("tela");
  const gv = $("#goVpn"); if (gv) gv.onclick = () => showTab("vpn");
}

/* ---------- configurações */
function tabConfig() {
  const m = OVL.m;
  $("#ovlBody").innerHTML = `<form id="mf">
    <label class="f"><span>Apelido</span><input name="label" value="${esc(m.label)}" placeholder="ex.: Servidor da filial Centro"></label>
    <label class="f"><span>Notas</span><textarea name="notes" placeholder="ex.: rack 2, nobreak 1, responsável: suporte">${esc(m.notes)}</textarea></label>
    <div class="grid">
      <label class="f"><span>Endereço SSH manual (opcional)</span><input name="ssh_host" value="${esc(m.ssh_host)}" placeholder="${m.platform === "windows" ? `ex.: ${esc(((m.local_ips[0] || "").split(" ").pop() || "192.168.0.50").split("/")[0])}` : `vazio = IP do Tailscale (${esc(m.tailscale_ip || "ainda não informado")})`}"><small>${m.platform === "windows" ? "Endereço que o painel alcance para o terminal (OpenSSH do Windows)." : "Use se quiser conectar por outro IP/nome."}</small></label>
      <label class="f"><span>Porta SSH</span><input name="ssh_port" type="number" min="1" max="65535" value="${esc(m.ssh_host ? m.ssh_port : m.ssh_port_real || 22)}"></label>
    </div>
    <div id="merr"></div>
    <div class="row"><button class="btn primary" type="submit">Salvar</button></div></form>
    <h3>Detalhes técnicos</h3>
    <div class="grid2">${kv("ID da máquina", `<span class="mono small">${esc(m.id)}</span>`)}${kv(m.build_os === "windows" ? "Configuração de origem" : "ISO de origem", m.build_hostname ? `<a href="#/iso/${esc(m.build_id)}">${esc(m.build_hostname)}</a>` : esc(m.build_id))}
      ${kv("Acesso pelo painel", m.panel_access ? (m.platform === "windows" ? "sim (chave do painel no OpenSSH)" : "sim (chave do painel na ISO)") : "não")}${kv("Último IP de origem", `<span class="mono">${esc(m.source_ip)}</span>`)}</div>
    <details><summary>Relatório completo (JSON)</summary><pre class="log">${esc(JSON.stringify(m.report, null, 2))}</pre></details>
    ${ME.role !== "admin" ? "" : m.archived_at
      ? `<h3>Máquina arquivada</h3><p class="muted small">Arquivada em ${fmtDate(m.archived_at)}. Terminal, VPN e acesso de sócios estão pausados.</p>
        <div class="row"><button class="btn primary" id="mrestore">Restaurar</button><button class="btn danger" id="mforget">Esquecer de vez</button></div>`
      : `<h3>Tirar da lista</h3><p class="muted small">Arquivar não mexe no servidor e não apaga nada: dá para restaurar quando quiser, com o mesmo IP da VPN e os mesmos acessos.</p>
        <button class="btn danger" id="marchive">Arquivar máquina</button>`}`;
  $("#mf").onsubmit = async ev => {
    ev.preventDefault();
    try { await api("/api/machines/" + OVL.id, { method: "POST", body: Object.fromEntries(new FormData(ev.target)) }); await refreshMachine(); toast("Salvo."); $("#merr").innerHTML = ""; }
    catch (e) { $("#merr").innerHTML = `<div class="note err">${esc(e.message)}</div>`; }
  };
  if ($("#marchive")) $("#marchive").onclick = () => machineAction(OVL.id, "archive");
  if ($("#mrestore")) $("#mrestore").onclick = () => machineAction(OVL.id, "restore");
  if ($("#mforget")) $("#mforget").onclick = () => machineAction(OVL.id, "forget");
}

route();

/* -------------------------------------------------- PWA: instalar + puxar para atualizar */
if ("serviceWorker" in navigator) {
  addEventListener("load", () => navigator.serviceWorker.register("/sw.js").catch(() => { }));
}
let installEvt = null;
addEventListener("beforeinstallprompt", e => {
  e.preventDefault(); installEvt = e;
  $("#installBtn").classList.remove("hidden");
  if (ME) shell();  // mostra "Instalar app" também no menu do celular
});
addEventListener("appinstalled", () => { installEvt = null; $("#installBtn").classList.add("hidden"); if (ME) shell(); });
async function installApp() {
  if (!installEvt) return;
  installEvt.prompt();
  try { await installEvt.userChoice; } catch (e) { }
  installEvt = null; $("#installBtn").classList.add("hidden"); if (ME) shell();
}
$("#installBtn").onclick = installApp;

// Puxar para atualizar (celular/tablet): arrastar para baixo no topo da página.
// Funciona também com o app instalado, onde o navegador não oferece o gesto.
(function pullToRefresh() {
  const ind = document.createElement("div");
  ind.id = "ptr"; ind.setAttribute("aria-hidden", "true");
  ind.innerHTML = `<svg viewBox="0 0 24 24" width="22" height="22" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12a9 9 0 1 1-3-6.7"/><path d="M21 3v6h-6"/></svg>`;
  document.body.appendChild(ind);
  const TRIGGER = 70, MAX = 110;
  let y0 = null, dist = 0, pulling = false, busy = false;
  const blocked = t => busy || document.body.classList.contains("noscroll") || document.body.classList.contains("menu-open")
    || t.closest("#ovl, .term, textarea, select, pre.log, .mnav");
  const show = d => {  // sai de baixo do cabeçalho (que fica por cima do indicador)
    ind.style.setProperty("--ptr-top", ($("#top").offsetHeight || 60) + "px");
    ind.style.transform = `translate(-50%, ${d - 50}px) rotate(${d * 3}deg)`;
    ind.style.opacity = Math.min(1, d / TRIGGER);
    ind.classList.toggle("ready", d >= TRIGGER);
  };
  const reset = () => { pulling = false; y0 = null; ind.classList.remove("ready", "spin"); ind.style.transform = ""; ind.style.opacity = ""; };
  addEventListener("touchstart", e => {
    if (e.touches.length !== 1 || scrollY > 0 || blocked(e.target)) { y0 = null; return; }
    y0 = e.touches[0].clientY; dist = 0; pulling = false;
  }, { passive: true });
  addEventListener("touchmove", e => {
    if (y0 === null) return;
    const dy = e.touches[0].clientY - y0;
    if (dy <= 0 || scrollY > 0) { if (pulling) reset(); else y0 = null; return; }
    pulling = true;
    dist = Math.min(MAX, dy * 0.5);  // resistência: o indicador anda metade do dedo
    show(dist);
    if (e.cancelable) e.preventDefault();  // sem "rebote" nem recarga dupla do navegador
  }, { passive: false });
  addEventListener("touchend", async () => {
    if (!pulling) { y0 = null; return; }
    if (dist < TRIGGER) return reset();
    busy = true; ind.classList.add("spin"); show(TRIGGER);
    // No assistente, só relê os dados (não apaga o que já foi digitado); no resto, recarrega a página.
    if (curRoute() === "#/" && ME && ME.mode !== "download") {
      if ($("#wf")) collect();  // guarda o que já foi digitado antes de redesenhar
      try { await route(); } catch (e) { }
      setTimeout(() => { busy = false; reset(); }, 350);
    } else {
      location.reload();
    }
  });
  addEventListener("touchcancel", () => { if (!busy) reset(); });
})();

/* -------------------------------------------------- ISOs em geração (aviso em todas as telas)
   A geração roda no SERVIDOR: fechar ou trocar de aba não a interrompe. Este aviso
   mostra isso ao voltar (inclusive reabrindo o app) e leva ao acompanhamento. */
let jobsTimer = null;
async function refreshJobs() {
  clearTimeout(jobsTimer);
  const box = $("#jobs");
  if (!ME) { box.classList.add("hidden"); return; }
  let run = [];
  try { run = (await api("/api/builds")).builds.filter(b => b.status === "queued" || b.status === "running"); } catch (e) { }
  const here = curRoute().replace("#/iso/", "");
  const show = run.filter(b => b.id !== here);  // na própria página da ISO a barra já aparece
  box.innerHTML = show.map(b => `<a class="job" href="#/iso/${b.id}"><span class="job-spin" aria-hidden="true"></span>
    <span class="job-txt">Gerando a ISO de <b>${esc(b.hostname)}</b> · ${Math.round(b.progress)}%<span class="muted small"> · ${esc(b.message || "")}</span></span>
    <span class="job-go">Acompanhar</span></a>`).join("");
  box.classList.toggle("hidden", !show.length);
  if (run.length && !document.hidden) jobsTimer = setTimeout(refreshJobs, 4000);
}
// Voltou para a aba (ou o celular "descongelou" a página): atualiza na hora,
// sem esperar o próximo ciclo (o navegador pausa os timers de abas em segundo plano).
function onBack() {
  if (document.hidden || !ME) return;
  refreshJobs();
  if (curRoute().startsWith("#/iso/")) route();
}
document.addEventListener("visibilitychange", onBack);
addEventListener("pageshow", e => { if (e.persisted) onBack(); });
addEventListener("focus", () => { if (!jobsTimer) onBack(); });
