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

function go(h) { if (location.hash !== h) location.hash = h; else route(); }

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

/* -------------------------------------------------- layout */
function shell() {
  $("#top").classList.toggle("hidden", !ME);
  if (!ME) return;
  $("#whoEmail").textContent = ME.email + (ME.role === "admin" ? " · admin" : "");
  const rb = $("#remoteBtn"), ru = ME.role === "admin" ? FEATURES.remote_url : "";
  rb.classList.toggle("hidden", !ru);
  if (ru) {  // dentro do painel (se liberado) ou em nova aba
    rb.href = FEATURES.remote_embed ? "#/remoto" : ru;
    if (FEATURES.remote_embed) rb.removeAttribute("target"); else rb.target = "_blank";
  }
  const dl = ME.mode === "download";
  const links = dl ? [[ME.build_id ? "#/iso/" + ME.build_id : "#/", "Minha ISO"]] : [["#/", "Nova ISO"], ["#/isos", "Minhas ISOs"]];
  if (ME.role === "admin") links.push(["#/maquinas", "Máquinas"], ["#/acessos", "Acessos"], ["#/sistema", "Sistema"]);
  const cur = location.hash || "#/";
  $("#nav").innerHTML = links.map(([h, t]) => `<a href="${h}" class="${cur === h || (!dl && h === "#/isos" && cur.startsWith("#/iso/")) ? "on" : ""}">${t}</a>`).join("");
}
$("#logout").onclick = async () => { try { await api("/api/logout", { method: "POST" }); } catch (e) { } ME = null; route.featLoaded = false; go("#/login"); };

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
  const h = location.hash || "#/";
  if (!ME && h !== "#/login") return go("#/login");
  shell();
  window.scrollTo(0, 0);
  if (h === "#/login") return ME ? go("#/") : renderLogin();
  if (ME.role === "admin" && !route.domainLoaded) { route.domainLoaded = true; await loadGuestDomain(); }
  if (ME.mode === "download") {  // acesso "somente download": só a ISO vinculada
    try { ME = (await api("/api/me")).user; } catch (e) { return; }  // o admin pode ter trocado a ISO
    if (!ME.build_id) { view.innerHTML = `<div class="card narrow"><h1>Nenhuma ISO vinculada</h1><p class="muted">Peça ao administrador para vincular a ISO a este acesso.</p></div>`; return; }
    if (h !== "#/iso/" + ME.build_id) return go("#/iso/" + ME.build_id);
    return renderBuild(ME.build_id);
  }
  if (h === "#/isos") return renderBuilds();
  if (h.startsWith("#/iso/")) return renderBuild(h.slice(6));
  if (h === "#/acessos" && ME.role === "admin") return renderUsers();
  if (h === "#/sistema" && ME.role === "admin") return renderSystem();
  if (h === "#/remoto" && ME.role === "admin" && FEATURES.remote_url) return renderRemote();
  if (h === "#/maquinas" && ME.role === "admin") return renderMachines();
  return renderWizard();
}
window.addEventListener("hashchange", route);

/* -------------------------------------------------- login */
function renderLogin() {
  view.innerHTML = `
  <div class="login-wrap"><div class="card narrow u-w100">
    <div class="center"><div class="logo u-logo-lg">◎</div>
    <h1>ISO Builder</h1><p class="muted">Instalador automático do Ubuntu Server</p></div>
    <form id="lf">
      <label class="f"><span>E-mail</span><input name="email" type="email" autocomplete="username" required></label>
      <label class="f"><span>Senha</span><input name="password" type="password" autocomplete="current-password" required></label>
      <div id="lerr"></div>
      <button class="btn primary big" type="submit">Entrar</button>
    </form>
    <p class="muted small center u-mt14">Acesso somente por convite do administrador.</p>
  </div></div>`;
  $("#lf").onsubmit = async ev => {
    ev.preventDefault();
    const f = new FormData(ev.target);
    try {
      ME = (await api("/api/login", { method: "POST", body: { email: f.get("email"), password: f.get("password") } })).user;
      go("#/");
    } catch (e) { $("#lerr").innerHTML = `<div class="note err">${esc(e.message)}</div>`; }
  };
}


/* -------------------------------------------------- assistente */
const W = {
  series: "24.04", hostname: "", username: "", realname: "", password: "", password2: "",
  timezone: "America/Sao_Paulo", locale: "pt_BR.UTF-8", keyboard: "br", keyboard_variant: "",
  net_mode: "dhcp", net_match_type: "mac", net_match_value: "", net_address: "", net_gateway: "", net_dns: "1.1.1.1,8.8.8.8", net_search: "",
  storage_type: "single", storage_value: "", storage_min_gb: 20, storage_layout: "lvm", storage_allow_usb: false,
  ssh_auth: "key", ssh_keys: "", tailscale: true, wireguard: false, tailscale_hostname: "", tailscale_extra: "", tailscale_key: "",
  docker: true, apps: "docker", coolify_email: "", coolify_password: "", coolify_password2: "",
  extra_packages: "", confirm_erase: "", panel_access: true,
  deliver_on: false, deliver_prefix: "", deliver_duration: "7d", deliver_label: ""
};
let step = 0;
let GUEST_DOMAIN = "gvtserver.online";
async function loadGuestDomain() { try { GUEST_DOMAIN = (await api("/api/admin/guest-domain")).domain; } catch (e) { } }
let autoSwitched = false;  // chave deixada em branco: mudou para usuário e senha
const STEPS = ["Sistema", "Acesso", "Rede", "Disco", "Acesso remoto", "Programas", "Revisar"];
let FEATURES = { wireguard: false, remote_url: "", remote_embed: false };

const field = (name, label, attrs = "", help = "") =>
  `<label class="f"><span>${label}</span><input name="${name}" value="${esc(W[name])}" ${attrs}>${help ? `<small>${help}</small>` : ""}</label>`;
const radio = (name, opts) => `<div class="choice">${opts.map(([v, t, d]) =>
  `<label><input type="radio" name="${name}" value="${v}" ${String(W[name]) === v ? "checked" : ""}><span><b>${t}</b>${d ? `<span class="muted small">${d}</span>` : ""}</span></label>`).join("")}</div>`;
const toggle = (name, text, desc) =>
  `<label class="switch"><input type="checkbox" name="${name}" ${W[name] ? "checked" : ""}><span><b>${text}</b><br><span class="muted small">${desc}</span></span></label>`;

function stepHTML() {
  switch (step) {
    case 0: return `<h2>Sistema</h2>
      ${radio("series", [["24.04", "Ubuntu 24.04 LTS", "Mais madura — recomendada"], ["26.04", "Ubuntu 26.04 LTS", "Mais nova"]])}
      <div class="grid">${field("hostname", "Nome do servidor (hostname)", 'placeholder="ex.: srv-arquivos-01" autocapitalize="off" required', "Minúsculas, números e hífen.")}
      ${field("username", "Usuário administrador", 'placeholder="ex.: operador" autocapitalize="off" required')}</div>
      ${field("realname", "Nome completo (opcional)", 'placeholder="ex.: Equipe de TI"')}
      <div class="grid">${field("timezone", "Fuso horário")}${field("locale", "Idioma (locale)")}${field("keyboard", "Teclado")}</div>`;
    case 1: {
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
      <label class="f"><span>Chave(s) SSH pública(s) <span class="muted small" id="keyopt">${pwMode ? "(opcional)" : "(opcional: em branco = usuário e senha)"}</span></span><textarea name="ssh_keys" placeholder="ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIG7kQv2m... notebook-suporte">${esc(W.ssh_keys)}</textarea>
      <small>Uma por linha. Arquivo <code>id_ed25519.pub</code> (nunca a chave sem .pub).</small></label>
      <div class="row"><label class="btn sm">Carregar arquivo .pub<input type="file" id="pubfile" accept=".pub,text/plain" class="hidden"></label></div>
      <details><summary>Não tenho chave SSH — como criar?</summary>
        <p class="small">No <b>Windows</b> (PowerShell) ou <b>Mac</b> (Terminal):</p>
        <pre class="log">ssh-keygen -t ed25519 -C "notebook-suporte"</pre>
        <p class="small">Aperte Enter nas perguntas. Depois carregue o arquivo <code>id_ed25519.pub</code> da pasta <code>.ssh</code> da sua pasta de usuário (no Mac: <code>cat ~/.ssh/id_ed25519.pub</code>; no Windows: <code>type $env:USERPROFILE\\.ssh\\id_ed25519.pub</code>) e cole acima.</p>
      </details>`;
    }
    case 2: return `<h2>Rede</h2>
      <p class="muted">Use cabo de rede. Wi-Fi não é configurado.</p>
      ${radio("net_mode", [["dhcp", "DHCP (automático)", "Recomendado: o roteador entrega o IP"], ["static", "IP fixo", "Você informa IP, gateway e DNS"]])}
      <div id="static" class="${W.net_mode === "static" ? "" : "hidden"}">
        ${radio("net_match_type", [["mac", "Pelo endereço MAC da placa", "Mais seguro"], ["name", "Pelo nome da interface", "ex.: enp3s0"], ["glob", "Primeira placa de rede", "Só se houver apenas uma"]])}
        <div class="grid">${field("net_match_value", "MAC ou nome da interface", 'placeholder="ex.: 3c:52:82:a1:4f:09 ou enp2s0"')}
        ${field("net_address", "IP/prefixo", 'placeholder="ex.: 10.20.30.40/24"')}${field("net_gateway", "Gateway", 'placeholder="ex.: 10.20.30.1"')}
        ${field("net_dns", "DNS (vírgula)")}${field("net_search", "Domínio de busca (opcional)", 'placeholder="ex.: escritorio.local"')}</div>
      </div>`;
    case 3: return `<h2>Disco de instalação</h2>
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
    case 4: return `<h2>Acesso remoto</h2>
      <p class="muted">Como você vai alcançar este servidor de longe. Os dois juntos = um é reserva do outro (recomendado para máquinas distantes).</p>
      ${FEATURES.wireguard ? toggle("wireguard", "WireGuard pelo seu servidor", "O servidor conecta para fora até o seu servidor (funciona atrás de qualquer roteador/CGNAT do cliente). Em Máquinas você gera o QR code para o seu Mac/celular e acessa a máquina, e opcionalmente a rede local do cliente.")
        : `<div class="note">WireGuard pelo seu servidor: desligado neste painel (defina <code>HUB_ENDPOINT</code> no Coolify).</div>`}
      ${toggle("tailscale", "Tailscale", "Acesso remoto pela sua Tailnet, sem depender do seu servidor estar no ar.")}
      <div id="ts" class="${W.tailscale ? "" : "hidden"}">
        <label class="f"><span>Auth key</span><input type="password" name="tailscale_key" value="${esc(W.tailscale_key)}" placeholder="tskey-auth-…" autocomplete="off">
        <small>Gere em <a href="https://login.tailscale.com/admin/settings/keys" target="_blank" rel="noopener">login.tailscale.com → Settings → Keys</a>: <b>Reusable desligado</b>, <b>Ephemeral desligado</b>, <b>Pre-approved ligado</b>, tag <code>tag:server</code> (recomendado). A chave vai dentro da ISO e é apagada do servidor após o uso.</small></label>
        <div class="grid">${field("tailscale_hostname", "Nome na Tailnet", `placeholder="${esc(W.hostname || "igual ao hostname")}"`)}${field("tailscale_extra", "Argumentos extras (opcional)", 'placeholder="--advertise-tags=tag:server"')}</div>
      </div>`;
    case 5: return `<h2>Programas</h2>
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
      ${field("extra_packages", "Pacotes extras (separados por espaço)", 'placeholder="ex.: vim tmux ncdu"')}`;
    case 6: {
      const disk = { single: "Único disco interno", serial: "Serial " + W.storage_value, path: W.storage_value, model: "Modelo " + W.storage_value }[W.storage_type];
      return `<h2>Revisar e gerar</h2>
      <div class="table-wrap"><table>
        <tr><th>Ubuntu</th><td>${esc(W.series)} LTS</td></tr>
        <tr><th>Servidor</th><td>${esc(W.hostname)} · usuário ${esc(W.username)}</td></tr>
        <tr><th>Região</th><td>${esc(W.timezone)} · ${esc(W.locale)} · teclado ${esc(W.keyboard)}</td></tr>
        <tr><th>Rede</th><td>${W.net_mode === "dhcp" ? "DHCP" : esc(W.net_address + " via " + W.net_gateway)}</td></tr>
        <tr><th>Disco</th><td>${esc(disk)} · mín. ${esc(W.storage_min_gb)} GB · ${esc(W.storage_layout)}</td></tr>
        <tr><th>SSH</th><td>${W.ssh_auth === "password"
          ? "Usuário e senha (só rede local e Tailscale; fail2ban)" + (W.ssh_keys.trim() ? ` + ${W.ssh_keys.split("\n").filter(l => l.trim()).length} chave(s)` : "")
          : W.ssh_keys.split("\n").filter(l => l.trim()).length + " chave(s); senha desativada no SSH"}</td></tr>
        <tr><th>Acesso remoto</th><td>${[W.wireguard ? "WireGuard pelo seu servidor" : "", W.tailscale ? "Tailscale (" + esc(W.tailscale_hostname || W.hostname) + ")" : ""].filter(Boolean).join(" + ") || "só rede local"}</td></tr>
        <tr><th>Programas</th><td>${{ none: "Nenhum", docker: "Docker + Compose", coolify: "Coolify (com Docker)", coolify_cockpit: "Coolify + Cockpit completo" }[W.apps]}${W.extra_packages ? " · extras: " + esc(W.extra_packages) : ""}</td></tr>
      </table></div>
      ${ME.role === "admin" ? `<div class="card u-mt16">
        <label class="switch"><input type="checkbox" name="deliver_on" ${W.deliver_on ? "checked" : ""}><span><b>Entregar esta ISO para uma pessoa</b><br><span class="muted small">Cria agora um acesso temporário “somente download” já vinculado a esta ISO: a pessoa entra, baixa o gravador e grava o pendrive. A máquina aparece em “Máquinas” com esse e-mail.</span></span></label>
        <div id="deliverBox" class="${W.deliver_on ? "" : "hidden"}">
          <div class="grid">
            <label class="f"><span>E-mail de acesso</span><div class="suffix"><input name="deliver_prefix" value="${esc(W.deliver_prefix)}" placeholder="ex.: filial-centro" autocapitalize="off" autocomplete="off"><span>@${esc(GUEST_DOMAIN)}</span></div><small>Vazio = prefixo aleatório. A senha é gerada automaticamente.</small></label>
            <label class="f"><span>Validade do acesso</span><select name="deliver_duration">${[["24h", "24 horas"], ["3d", "3 dias"], ["7d", "7 dias"], ["30d", "30 dias"]].map(([v, t]) => `<option value="${v}" ${W.deliver_duration === v ? "selected" : ""}>${t}</option>`).join("")}</select></label>
            ${field("deliver_label", "Para quem (opcional)", 'placeholder="ex.: Suporte da filial Centro"')}
          </div></div></div>` : ""}
      <div class="note warn u-mt16"><b>Atenção:</b> ao dar boot com o pendrive, o disco de destino da máquina será <b>completamente apagado</b>.</div>
      <label class="f"><span>Digite APAGAR para confirmar</span><input name="confirm_erase" value="${esc(W.confirm_erase)}" autocomplete="off" placeholder="APAGAR"></label>
      <div id="werr"></div>`;
    }
  }
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
  const e = [];
  if (step === 0) {
    if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(W.hostname)) e.push("Nome do servidor: use minúsculas, números e hífen.");
    if (!/^[a-z_][a-z0-9_-]{0,31}$/.test(W.username)) e.push("Usuário: minúsculas, números, _ ou -.");
  }
  if (step === 1) {
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

  }
  if (step === 2 && W.net_mode === "static" && (!W.net_address.includes("/") || !W.net_gateway)) e.push("Informe IP/prefixo e gateway.");
  if (step === 3 && W.storage_type !== "single" && W.storage_value.length < 3) e.push("Informe o valor do critério do disco.");
  if (step === 4 && W.tailscale && !/^tskey-/.test(W.tailscale_key.trim())) e.push("Cole a auth key do Tailscale (tskey-auth-…) ou desligue o Tailscale.");
  if (step === 4 && !W.tailscale && !W.wireguard && !confirm("Sem WireGuard e sem Tailscale, o acesso será só pela rede local do cliente. Continuar?")) e.push("Escolha pelo menos uma forma de acesso remoto.");
  if (step === 5 && W.apps.startsWith("coolify")) {
    const p = W.coolify_password;
    if (!/^[^@\s]+@[^@\s]+\.[a-z]{2,}$/i.test(W.coolify_email)) e.push("Informe o e-mail do admin do Coolify.");
    if (p.length < 12 || !/[a-z]/.test(p) || !/[A-Z]/.test(p) || !/\d/.test(p) || !/[^A-Za-z0-9]/.test(p)) e.push("Senha do Coolify: 12+ caracteres com maiúscula, minúscula, número e símbolo.");
    else if (p !== W.coolify_password2) e.push("As senhas do Coolify não conferem.");
    if (p.includes("'")) e.push("A senha do Coolify não pode ter aspas simples.");
  }
  if (step === 6 && W.deliver_on && W.deliver_prefix && !/^[a-z0-9]([a-z0-9._-]{0,38}[a-z0-9])?$/.test(W.deliver_prefix)) e.push("Prefixo do e-mail: letras minúsculas, números, ponto, hífen ou _.");
  return e;
}

function renderWizard() {
  view.innerHTML = `
  <h1>Nova ISO</h1><p class="muted">Responda às perguntas; no final o servidor gera a ISO e você baixa com o gravador de pendrive.</p>
  <div class="wizard">
    <ol class="steps">${STEPS.map((s, i) => `<li data-i="${i}" class="${i === step ? "on" : i < step ? "ok" : ""}"><span class="n">${i < step ? "✓" : i + 1}</span>${s}</li>`).join("")}</ol>
    <div class="card"><form id="wf" autocomplete="off">${stepHTML()}
      <div id="serr"></div>
      <div class="nav-btns"><button type="button" class="btn" id="back" ${step === 0 ? "disabled" : ""}>Voltar</button>
      <button type="submit" class="btn primary">${step === STEPS.length - 1 ? "Gerar ISO" : "Continuar"}</button></div>
    </form></div>
  </div>`;
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
  $$('input[name="apps"]').forEach(r => r.onchange = () => $("#coolifyBox").classList.toggle("hidden", !r.value.startsWith("coolify")));
  const dl = $('input[name="deliver_on"]'); if (dl) dl.onchange = () => $("#deliverBox").classList.toggle("hidden", !dl.checked);
  const ts = $('input[name="tailscale"]'); if (ts) ts.onchange = () => $("#ts").classList.toggle("hidden", !ts.checked);
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
    if (step < STEPS.length - 1) { step++; return renderWizard(); }
    if (W.confirm_erase !== "APAGAR") { $("#serr").innerHTML = `<div class="note err">Digite APAGAR para confirmar.</div>`; return; }
    const btn = $('#wf button[type="submit"]'); btn.disabled = true; btn.textContent = "Enviando…";
    try {
      const body = { ...W, storage_min_gb: +W.storage_min_gb };
      if (W.deliver_on) body.deliver = { prefix: W.deliver_prefix, duration: W.deliver_duration, label: W.deliver_label };
      const r = await api("/api/builds", { method: "POST", body });
      if (r.delivery) { try { sessionStorage.setItem("uab-delivery-" + r.id, JSON.stringify(r.delivery)); } catch (e) { } }
      W.password = W.password2 = W.tailscale_key = W.confirm_erase = W.coolify_password = W.coolify_password2 = ""; step = 0;
      W.deliver_on = false; W.deliver_prefix = W.deliver_label = "";
      go("#/iso/" + r.id);
    } catch (e) { $("#serr").innerHTML = `<div class="note err">${esc(e.message)}</div>`; btn.disabled = false; btn.textContent = "Gerar ISO"; }
  };
}

/* -------------------------------------------------- lista de ISOs */
const STATUS = { queued: "Na fila", running: "Gerando", done: "Pronta", error: "Erro", expired: "Expirada" };
async function renderBuilds() {
  view.innerHTML = `<h1>Minhas ISOs</h1><div id="bl" class="card">Carregando…</div>`;
  const d = await api("/api/builds");
  const q = d.quota ? `<p class="muted small">Você usou ${d.quota.used} de ${d.quota.max} ISOs deste acesso.</p>` : "";
  $("#bl").innerHTML = q + (d.builds.length ? `<div class="table-wrap"><table><tr><th>Servidor</th><th>Ubuntu</th><th>Status</th><th>Criada</th>${ME.role === "admin" ? "<th>Conta</th>" : ""}<th></th></tr>
    ${d.builds.map(b => `<tr><td><b>${esc(b.hostname)}</b></td><td>${esc(b.series)}</td><td><span class="pill ${b.status}">${STATUS[b.status] || esc(b.status)}</span></td>
      <td>${fmtDate(b.created_at)}</td>${ME.role === "admin" ? `<td class="small">${esc(b.owner)}</td>` : ""}<td><div class="row"><a class="btn sm" href="#/iso/${b.id}">Abrir</a>${ME.mode === "download" || ["queued", "running"].includes(b.status) ? "" : `<button class="btn sm danger" data-del="${b.id}">Excluir</button>`}</div></td></tr>`).join("")}
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
  if (b.status === "queued" || b.status === "running") {
    view.innerHTML = deliveryCard(id) + `<div class="card narrow"><h1>Gerando a ISO de ${esc(b.hostname)}</h1>
      <p class="muted">Pode levar alguns minutos (na primeira vez, o servidor também baixa o Ubuntu oficial, ~3–4 GB). Você pode sair desta página: a geração continua.</p>
      <div class="bar"><i id="pb"></i></div><p id="pm"><b>${Math.round(b.progress)}%</b> · ${esc(b.message)}</p></div>`;
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
    view.innerHTML = `<div class="card"><h1>Não foi possível gerar a ISO</h1><div class="note err">${esc(b.message)}</div>
      <div class="row"><a class="btn primary" href="#/">Tentar de novo</a><button class="btn" id="showlog">Ver detalhes técnicos</button><button class="btn danger" id="del">Excluir</button></div><pre class="log hidden" id="log"></pre></div>`;
    $("#showlog").onclick = async () => { $("#log").textContent = await api(`/api/builds/${id}/log`); $("#log").classList.remove("hidden"); };
    $("#del").onclick = () => delBuild(id);
    return;
  }
  if (b.status === "expired") {
    view.innerHTML = `<div class="card"><h1>ISO expirada</h1><p class="muted">${esc(b.message)}. Gere novamente se precisar.</p><a class="btn primary" href="#/">Nova ISO</a></div>`;
    return;
  }
  renderDownload(b);
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

/* -------------------------------------------------- admin: acessos */
function credText(c) {
  const extra = c.mode === "download" && c.iso ? `\nISO:      ${c.iso} (entre e siga os passos para gravar o pendrive)` : "";
  return `Acesso ao ISO Builder\n\nEndereço: ${c.url}\nE-mail:   ${c.email}\nSenha:    ${c.password}\nVálido até: ${fmtDate(c.expires_at)}${extra}`;
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
    </div>
    <label class="f hidden" id="isoPick"><span>ISO para este acesso</span><select name="build_id"></select>
      <small>A ISO fica guardada até o fim da validade do acesso.</small></label>
    <div class="grid">
      <label class="f"><span>Para quem (opcional)</span><input name="label" placeholder="ex.: Suporte da filial Centro"></label>
      <label class="f"><span>E-mail de login</span><div class="suffix"><input name="prefix" placeholder="ex.: filial-centro" autocapitalize="off" autocomplete="off"><span>@${esc(GUEST_DOMAIN)}</span></div><small>Só o prefixo; vazio = aleatório.</small></label>
      <label class="f"><span>Validade</span><select name="duration"><option value="1h">1 hora</option><option value="24h" selected>24 horas</option><option value="3d">3 dias</option><option value="7d">7 dias</option><option value="30d">30 dias</option></select></label>
      <label class="f" id="maxB"><span>Máximo de ISOs</span><input name="max_builds" type="number" min="1" max="20" value="3"></label>
    </div>
    <div id="uerr"></div><button class="btn primary" type="submit">Criar acesso</button></form></div>
  <div id="cred"></div>
  <div class="card"><h2>Acessos criados</h2><div id="ul">Carregando…</div></div>`;
  const mine = (await api("/api/builds")).builds.filter(b => b.status === "done");
  $('select[name="build_id"]').innerHTML = mine.length
    ? mine.map(b => `<option value="${b.id}">${esc(b.hostname)} · Ubuntu ${esc(b.series)} · ${fmtDate(b.created_at)}</option>`).join("")
    : `<option value="">Nenhuma ISO pronta: gere uma em “Nova ISO”</option>`;
  $$('#uf input[name="mode"]').forEach(r => r.onchange = () => {
    const dl = $('#uf input[name="mode"]:checked').value === "download";
    $("#isoPick").classList.toggle("hidden", !dl); $("#maxB").classList.toggle("hidden", dl);
  });
  $("#uf").onsubmit = async ev => {
    ev.preventDefault();
    const f = Object.fromEntries(new FormData(ev.target));
    try { const c = await api("/api/admin/users", { method: "POST", body: f }); ev.target.reset(); $("#isoPick").classList.add("hidden"); $("#maxB").classList.remove("hidden"); showCred(c, "Acesso criado"); loadUsers(); $("#uerr").innerHTML = ""; }
    catch (e) { $("#uerr").innerHTML = `<div class="note err">${esc(e.message)}</div>`; }
  };
  loadUsers();
}

async function loadUsers() {
  const [d, bl] = await Promise.all([api("/api/admin/users"), api("/api/builds")]);
  const ready = bl.builds.filter(b => b.status === "done");
  const isoOptions = cur => ready.length
    ? ready.map(b => `<option value="${b.id}" ${b.id === cur ? "selected" : ""}>${esc(b.hostname)} · Ubuntu ${esc(b.series)} · ${fmtDate(b.created_at)}</option>`).join("")
    : `<option value="">Nenhuma ISO pronta: gere uma em “Nova ISO”</option>`;
  if (!d.users.length) { $("#ul").innerHTML = `<p class="muted">Nenhum acesso criado.</p>`; return; }
  $("#ul").innerHTML = `<div class="table-wrap"><table><tr><th>Login</th><th>Situação</th><th>Validade</th><th>Último acesso</th><th>Tipo / ISOs</th><th></th></tr>
    ${d.users.map(u => `<tr><td><b>${esc(u.email)}</b>${u.label ? `<br><span class="muted small">${esc(u.label)}</span>` : ""}</td>
      <td><span class="pill ${u.state === "ativa" ? "done" : "error"}">${esc(u.state)}</span></td><td>${fmtDate(u.expires_at)}</td>
      <td>${fmtDate(u.last_login)}</td><td>${u.mode === "download"
        ? `Só download<br><span class="small ${u.build_ok ? "muted" : "err"}">${u.build_hostname ? esc(u.build_hostname) : "ISO removida"}${!u.build_ok ? " (indisponível)" : u.build_status !== "done" ? " (sendo gerada)" : ""}</span>
          <br><button class="btn sm ${u.build_ok ? "" : "primary"} u-mt8" data-a="relink" data-id="${u.id}">${u.build_ok ? "Trocar ISO" : "Vincular outra ISO"}</button>
          <div class="hidden u-mt8" id="rl-${u.id}"><select>${isoOptions(u.build_ok ? u.build_id : "")}</select>
            <div class="row u-mt8"><button class="btn sm primary" data-a="relink-save" data-id="${u.id}" ${ready.length ? "" : "disabled"}>Vincular</button>
            <button class="btn sm" data-a="relink-cancel" data-id="${u.id}">Cancelar</button></div></div>`
        : `${u.builds}/${u.max_builds} ISOs`}</td>
      <td><div class="row"><button class="btn sm" data-a="reset" data-id="${u.id}">Nova senha</button>
      <button class="btn sm" data-a="extend" data-id="${u.id}">+24h</button>
      ${u.state === "ativa" ? `<button class="btn sm danger" data-a="revoke" data-id="${u.id}">Revogar</button>` : ""}
      <button class="btn sm danger" data-a="delete" data-id="${u.id}">Excluir</button></div></td></tr>`).join("")}</table></div>`;
  $$("#ul [data-a]").forEach(btn => btn.onclick = async () => {
    const id = btn.dataset.id, a = btn.dataset.a;
    const box = $("#rl-" + id);
    if (a === "relink" || a === "relink-cancel") { box.classList.toggle("hidden", a === "relink-cancel"); btn.closest("td").querySelector('[data-a="relink"]').classList.toggle("hidden", a === "relink"); return; }
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
      if (a === "delete") { if (!confirm("Excluir a conta e as ISOs dela?")) return; await api(`/api/admin/users/${id}`, { method: "DELETE" }); }
      loadUsers();
    } catch (e) { toast(e.message); }
  });
}

/* -------------------------------------------------- admin: sistema */
function renderRemote() {
  const u = FEATURES.remote_url;
  view.innerHTML = `<div class="spread"><div><h1>Acesso remoto</h1><p class="muted">${esc(u.replace(/^https:\/\//, "").replace(/\/$/, ""))}</p></div>
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
  $("#pbanner").innerHTML = d.hub && d.hub.online
    ? `<div class="note ok">Terminal no navegador pelo <b>WireGuard do seu servidor</b>${d.panel.state === "Running" ? " e pelo Tailscale" : ""}. Máquinas com o túnel conectado abrem direto.</div>`
    : panelBanner(d.panel);
  if (!d.machines.length) {
    $("#ml").innerHTML = `<p class="muted">Nenhuma máquina se reportou ainda. As ISOs geradas a partir de agora fazem o servidor aparecer aqui alguns minutos depois do primeiro boot.</p>`;
    return;
  }
  $("#ml").innerHTML = `<div class="table-wrap"><table><tr><th></th><th>Servidor</th><th>Conta</th><th>VPN</th><th>Rede local</th><th>Provisionamento</th><th>Visto</th><th></th></tr>
    ${d.machines.map(m => `<tr class="clickable" data-m="${m.id}">
      <td>${dot(m.online, mstate(m))}</td>
      <td><b>${esc(m.label || m.hostname)}</b>${m.label ? `<br><span class="muted small">${esc(m.hostname)}</span>` : ""}<br><span class="muted small">${esc(m.os)}</span>${m.online ? "" : `<br><span class="err small">${mstate(m)} · ${fmtAgo(m.off_at || m.last_seen)}</span>`}</td>
      <td class="small">${m.account ? esc(m.account) : `<span class="muted">—</span>`}</td>
      <td>${m.wg_ip ? `<span class="mono">${esc(m.wg_ip)}</span> <span class="muted small">WG</span><br>` : ""}${m.tailscale_ip ? `<span class="mono">${esc(m.tailscale_ip)}</span> <span class="muted small">TS</span>` : (m.wg_ip ? "" : `<span class="muted">—</span>`)}</td>
      <td class="small">${esc((m.local_ips[0] || "—").split(" ").pop())}</td>
      <td>${m.provisioned.startsWith("ok") ? `<span class="pill done">concluído</span>` : `<span class="pill running">${esc(m.provisioned || "em andamento")}</span>`}</td>
      <td class="small">${fmtAgo(m.last_seen)}</td>
      <td><button class="btn sm primary" data-open="${m.id}">Abrir</button></td></tr>`).join("")}</table></div>`;
  $$("#ml [data-m]").forEach(tr => tr.onclick = () => openMachine(tr.dataset.m));
}

/* ---------- overlay */
let OVL = null;  // {id, tab, term, ws, fit, ro}

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
  showTab(tab);
  OVL.timer = setInterval(() => { if (OVL && OVL.tab === "resumo" && !document.hidden) refreshMachine().then(() => OVL && OVL.tab === "resumo" && showTab("resumo")); }, 15000);
}

async function refreshMachine() {
  const d = await api("/api/machines/" + OVL.id);
  OVL.m = d.machine; OVL.panel = d.panel;
  const m = OVL.m;
  $("#ovlTitle").innerHTML = `${dot(m.online, mstate(m))} <b>${esc(m.label || m.hostname)}</b> <span class="muted small">${mstate(m)} · ${m.off_at ? "avisou " + fmtAgo(m.off_at) : "visto " + fmtAgo(m.last_seen)}</span>`;
}

function showTab(t) {
  if (OVL.tab === "terminal" && t !== "terminal") stopTerminal();
  OVL.tab = t;
  $$("#ovlTabs button").forEach(b => b.classList.toggle("on", b.dataset.t === t));
  ({ resumo: tabResumo, terminal: tabTerminal, vpn: tabVpn, conectar: tabConectar, config: tabConfig })[t]();
}

const kv = (k, v) => `<div class="kv"><span>${k}</span><b>${v}</b></div>`;

function tabResumo() {
  const m = OVL.m, r = m.resources || {}, sv = m.services || {};
  const gb = n => n ? (n / 1e9).toFixed(1) + " GB" : "—";
  const svc = (n, on) => `<span class="pill ${on ? "done" : "error"}">${n} ${on ? "ativo" : "inativo"}</span>`;
  $("#ovlBody").innerHTML = `
    ${appLinks(m)}
    <div class="grid2">
      ${kv("Conta", esc(m.account || "—"))}${kv("ISO gerada por", esc(m.owner_email || "—"))}
      ${kv("Hostname", esc(m.hostname))}${kv("Usuário admin", `<span class="mono">${esc(m.admin_user)}</span>`)}
      ${kv("IP Tailscale", m.tailscale_ip ? `<span class="mono">${esc(m.tailscale_ip)}</span>` : "—")}${kv("Nome Tailscale", esc(m.tailscale_dns || "—"))}
      ${kv("Rede local", esc(m.local_ips.map(x => x.split(" ").pop()).join(", ") || "—"))}${kv("Gateway", esc(m.report.gateway || "—"))}
      ${kv("Sistema", esc(m.os))}${kv("Kernel", esc(m.kernel))}
      ${kv("Ligado há", fmtUptime(m.uptime))}${kv("Acesso SSH", m.ssh_auth === "password" ? "usuário e senha (rede local/Tailscale)" : "chave SSH")}
      ${kv("CPU", `${r.cpus || "—"} núcleos · carga ${r.load1 ?? "—"}`)}${kv("Memória", `${gb(r.mem_available)} livres de ${gb(r.mem_total)}`)}
      ${kv("Disco /", `${gb(r.disk_free)} livres de ${gb(r.disk_total)}`)}${kv("Primeiro contato", fmtDate(m.first_seen))}
    </div>
    <h3>Serviços</h3><div class="row">${svc("SSH", sv.ssh)}${svc("Tailscale", sv.tailscaled)}${m.apps.docker ? svc("Docker", sv.docker) : ""}${m.apps.coolify ? svc("Coolify", sv.coolify) : ""}${m.apps.cockpit ? svc("Cockpit", sv.cockpit) : ""}${m.ssh_auth === "password" ? svc("fail2ban", sv.fail2ban) : ""}
      ${m.report.docker_containers != null ? `<span class="pill">${m.report.docker_containers} container(s)</span>` : ""}</div>
    <h3>Provisionamento</h3>
    <div class="table-wrap"><table>${Object.entries(m.steps || {}).map(([k, v]) => `<tr><td>${esc(k)}</td><td>${String(v).startsWith("ok") || /^100\./.test(v) ? `<span class="ok">${esc(v)}</span>` : esc(v)}</td></tr>`).join("") || "<tr><td class='muted'>sem dados</td></tr>"}
      <tr><td><b>geral</b></td><td>${esc(m.provisioned || "—")}</td></tr></table></div>
    ${m.downloads.length ? `<h3>Quem baixou</h3><div class="table-wrap"><table>${m.downloads.slice().reverse().map(x => `<tr><td>${esc(x.email)}</td><td class="small">${esc(x.kind === "iso" ? "ISO" : "gravador " + x.kind.replace("gravador-", ""))}</td><td class="small">${fmtDate(x.at)}</td></tr>`).join("")}</table></div>` : ""}
    <div class="row u-mt16"><button class="btn primary" id="goTerm">Abrir terminal</button><button class="btn" id="goConn">Conectar do meu computador</button></div>`;
  $("#goTerm").onclick = () => showTab("terminal");
  $("#goConn").onclick = () => showTab("conectar");
}

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
  const m = OVL.m, p = OVL.panel;
  const viaWg = m.wg && m.wg.connected;
  const viaTs = p.state === "Running" && m.tailscale_ip;
  const reason = !m.panel_access ? "Esta ISO foi gerada sem “acesso pelo painel”: a chave do painel não está no servidor. Use “Conectar do meu computador”."
    : m.ssh_host || viaWg || viaTs ? ""
    : m.wg ? "O túnel WireGuard desta máquina está desconectado agora (servidor desligado ou sem internet?)."
    : !m.tailscale_ip ? "A máquina ainda não informou IP do Tailscale nem se conectou ao hub WireGuard. Aguarde ou defina um endereço em Configurações."
    : (p.message || "O painel não está na Tailnet.");
  $("#ovlBody").innerHTML = `
    <div class="spread"><p class="muted small">Entra como <b class="mono">${esc(m.admin_user)}</b> em <b class="mono">${esc(sshTarget(m))}</b> usando a chave do painel (sem digitar IP, usuário ou senha). Para comandos de administrador: <code>sudo</code> (pede a senha do servidor).</p>
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
  const m = OVL.m, w = d.machine;
  if (!d.enabled) { $("#ovlBody").innerHTML = `<div class="note warn">WireGuard pelo seu servidor está desligado neste painel. Defina <code>HUB_ENDPOINT</code> no Coolify (e abra a porta UDP no roteador).</div>`; return; }
  if (!w) { $("#ovlBody").innerHTML = `<div class="note">Esta máquina ainda não se registrou no hub. Isso acontece sozinho no primeiro boot se a ISO foi gerada com “WireGuard pelo seu servidor”${d.hub.online ? "" : " — e o hub precisa estar no ar (" + esc(d.hub.message || "") + ")"}.</div>`; return; }
  const lans = (m.local_ips || []).map(x => { const [ifn, cidr] = x.split(" "); return { ifn, cidr }; }).filter(x => x.cidr && x.cidr.endsWith("/24"));
  $("#ovlBody").innerHTML = `
    <div class="grid2">
      ${kv("Túnel", w.connected ? `<span class="ok">● conectado</span> <span class="muted small">(${fmtAgo(w.last_handshake)})</span>` : `<span class="err">● sem conexão</span> <span class="muted small">${w.last_handshake ? "último contato " + fmtAgo(w.last_handshake) : "nunca conectou"}</span>`)}
      ${kv("IP do servidor na VPN", `<span class="mono">${esc(w.server_ip)}</span>`)}
      ${kv("Hub", d.hub.online ? `<span class="ok">no ar</span> · <span class="mono small">${esc(d.endpoint)}</span>` : `<span class="err">${esc(d.hub.message || "fora do ar")}</span>`)}
      ${kv("Rede do cliente", w.lan ? `<span class="mono">${esc(w.lan_cidr)}</span> → <span class="mono">${esc(w.lan_map)}</span>` : "desligada")}
    </div>
    <h3>Conectar um dispositivo seu (Mac, Windows, celular)</h3>
    <p class="small muted">Gera uma configuração só para este servidor. Instale o app oficial do WireGuard, importe o arquivo ou escaneie o QR code e ative. A chave privada é mostrada <b>uma única vez</b>.</p>
    <form id="wgNew" class="row"><input name="name" placeholder="ex.: MacBook do suporte" maxlength="40"><button class="btn primary" type="submit">Gerar acesso</button></form>
    <div id="wgOut"></div>
    <h3>Dispositivos com acesso</h3>
    <div id="wgList">${d.clients.length ? `<div class="table-wrap"><table>${d.clients.map(c => `<tr><td><b>${esc(c.name)}</b><br><span class="muted small mono">${esc(c.ip)}</span></td><td class="small">${c.last_handshake ? "último uso " + fmtAgo(c.last_handshake) : "nunca conectou"}</td><td class="small">${fmtDate(c.created_at)}</td><td><button class="btn sm danger" data-rev="${c.id}">Revogar</button></td></tr>`).join("")}</table></div>` : `<p class="muted small">Nenhum ainda.</p>`}</div>
    <h3>Acesso à rede local do cliente (opcional)</h3>
    <p class="small muted">Com isso ligado, pela VPN você alcança também os equipamentos da rede do cliente (impressoras, câmeras, sistemas). Para não conflitar com redes iguais de outros clientes, cada rede ganha um endereço próprio: <span class="mono">192.168.0.50</span> vira <span class="mono">${esc(w.lan_map.replace(/\.0\/24$/, ".50"))}</span>. Aplicado pelo servidor em até 10 min; depois, gere o acesso do dispositivo de novo para incluir essa rota.</p>
    ${lans.length ? `<form id="wgLan" class="row"><select name="lan">${lans.map(l => `<option value="${esc(l.ifn)}|${esc(l.cidr)}" ${w.lan_if === l.ifn ? "selected" : ""}>${esc(l.ifn)} · ${esc(l.cidr)}</option>`).join("")}</select>
      <button class="btn ${w.lan ? "" : "primary"}" type="submit" name="on" value="1">${w.lan ? "Atualizar" : "Ligar acesso à rede"}</button>
      ${w.lan ? `<button class="btn danger" type="button" id="wgLanOff">Desligar</button>` : ""}</form>`
      : `<p class="muted small">A máquina não informou nenhuma rede local /24.</p>`}`;
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
        <li>Ative o túnel e conecte: <code>ssh ${esc(m.admin_user)}@${esc(c.server_ip)}</code>${c.lan_map ? ` · rede do cliente em <span class="mono">${esc(c.lan_map)}</span>` : ""}</li></ol>
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
  const host = sshTarget(m), user = m.admin_user;
  const port = m.ssh_host && +m.ssh_port !== 22 ? +m.ssh_port : 22;
  const cmd = `ssh ${port !== 22 ? `-p ${port} ` : ""}${user}@${host}`;
  const dnsCmd = m.tailscale_dns ? `ssh ${user}@${m.tailscale_dns.split(".")[0]}` : "";
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

/* ---------- configurações */
function tabConfig() {
  const m = OVL.m;
  $("#ovlBody").innerHTML = `<form id="mf">
    <label class="f"><span>Apelido</span><input name="label" value="${esc(m.label)}" placeholder="ex.: Servidor da filial Centro"></label>
    <label class="f"><span>Notas</span><textarea name="notes" placeholder="ex.: rack 2, nobreak 1, responsável: suporte">${esc(m.notes)}</textarea></label>
    <div class="grid">
      <label class="f"><span>Endereço SSH manual (opcional)</span><input name="ssh_host" value="${esc(m.ssh_host)}" placeholder="vazio = IP do Tailscale (${esc(m.tailscale_ip || "ainda não informado")})"><small>Use se quiser conectar por outro IP/nome.</small></label>
      <label class="f"><span>Porta SSH</span><input name="ssh_port" type="number" min="1" max="65535" value="${esc(m.ssh_port)}"></label>
    </div>
    <div id="merr"></div>
    <div class="row"><button class="btn primary" type="submit">Salvar</button></div></form>
    <h3>Detalhes técnicos</h3>
    <div class="grid2">${kv("ID da máquina", `<span class="mono small">${esc(m.id)}</span>`)}${kv("ISO de origem", m.build_hostname ? `<a href="#/iso/${esc(m.build_id)}">${esc(m.build_hostname)}</a>` : esc(m.build_id))}
      ${kv("Acesso pelo painel", m.panel_access ? "sim (chave do painel na ISO)" : "não")}${kv("Último IP de origem", `<span class="mono">${esc(m.source_ip)}</span>`)}</div>
    <details><summary>Relatório completo (JSON)</summary><pre class="log">${esc(JSON.stringify(m.report, null, 2))}</pre></details>
    <div class="note warn u-mt16">Remover da lista não mexe no servidor. Se ele continuar ligado, reaparece no próximo relatório (a cada 10 min).</div>
    <button class="btn danger" id="mdel">Remover da lista</button>`;
  $("#mf").onsubmit = async ev => {
    ev.preventDefault();
    try { await api("/api/machines/" + OVL.id, { method: "POST", body: Object.fromEntries(new FormData(ev.target)) }); await refreshMachine(); toast("Salvo."); $("#merr").innerHTML = ""; }
    catch (e) { $("#merr").innerHTML = `<div class="note err">${esc(e.message)}</div>`; }
  };
  $("#mdel").onclick = async () => { if (!confirm("Remover esta máquina da lista?")) return; await api("/api/machines/" + OVL.id, { method: "DELETE" }); closeOverlay(); loadMachines(); };
}

route();
