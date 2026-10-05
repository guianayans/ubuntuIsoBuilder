#!/bin/bash
# ubuntu-autoinstall-builder — provisionador de primeiro boot.
#
# Executado pelo systemd (uab-provision.service) a cada boot até concluir.
# Cada etapa é idempotente e registra /var/lib/uab/<etapa>.done. Se alguma
# etapa falhar, o script sai com erro e o systemd tenta de novo em 60 s,
# indefinidamente, sem exigir interação.
#
# Ordem pensada para NUNCA perder o acesso remoto:
#   1. chaves SSH  -> 2. endurecimento SSH (só com chave válida confirmada)
#   3. rede        -> 4. pacotes base -> 5. Tailscale -> 6. Docker
set -uo pipefail

CONF=/etc/uab/uab.conf
STATE=/var/lib/uab
LOG=/var/log/uab-provision.log
TS_KEY=/etc/uab/tailscale-authkey
SSHD_DROPIN=/etc/ssh/sshd_config.d/10-uab-hardening.conf
TAILSCALE_FPR=2596A99EAAB33821893C0A79458CA832957F5868
DOCKER_FPR=9DC858229FC7DD38854AE2D88D81803C0EBFCD88
CLOUDFLARE_FPR=CC94B39C77AE7342A68B89628A682D308D4E5E73   # CloudFlare Software Packaging 2025
NVIDIA_CT_FPR=C95B321B61E88C1809C4F759DDCAE044F796ECB0    # NVIDIA (libnvidia-container)

export DEBIAN_FRONTEND=noninteractive
APT_OPTS=(-y -q -o DPkg::Lock::Timeout=900 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold)

mkdir -p "$STATE"; chmod 755 "$STATE"
touch "$LOG"; chmod 640 "$LOG"
exec > >(tee -a "$LOG") 2>&1

log() { printf '%s uab-provision: %s\n' "$(date '+%F %T')" "$*"; }
# shellcheck disable=SC1090
. "$CONF"
USER_NAME="$UAB_ADMIN_USER"
SSH_AUTH="${UAB_SSH_AUTH:-key}"   # key | password
SSH_ROOT="${UAB_SSH_ROOT:-no}"    # yes = root por SSH, SÓ por chave (prohibit-password); no = root bloqueado
SSH_PORT="${UAB_SSH_PORT:-22}"
SSHD_PORT_DROPIN=/etc/ssh/sshd_config.d/05-uab-port.conf
F2B_PORT_CONF=/etc/fail2ban/jail.d/uab-sshd-port.conf
# Redes de onde o login por SENHA é aceito (modo password): privadas + Tailscale.
PW_NETS="10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,100.64.0.0/10,127.0.0.0/8,fd7a:115c:a1e0::/48,fc00::/7,fe80::/10,::1"
# Coolify gerencia o próprio servidor entrando como root (por chave) a partir
# dos containers dele. Root só é aceito, por chave, vindo das redes do Docker.
COOLIFY_POOL="172.30.0.0/16"
DOCKER_NETS="172.16.0.0/12"
UAB_COOLIFY="${UAB_COOLIFY:-no}"; UAB_COCKPIT="${UAB_COCKPIT:-no}"
UAB_DDNS="${UAB_DDNS:-no}"; UAB_CLOUDFLARE="${UAB_CLOUDFLARE:-no}"; UAB_GPU="${UAB_GPU:-no}"
UAB_VIRT="${UAB_VIRT:-no}"; UAB_VIRT_BRIDGE="${UAB_VIRT_BRIDGE:-no}"; UAB_TWEAKS="${UAB_TWEAKS:-no}"
UAB_CLAUDE_CODE="${UAB_CLAUDE_CODE:-no}"
CLAUDE_ALL="${UAB_CLAUDE_ALL:-no}"   # yes = 'claude' para todos os usuários (link em /usr/local/bin, inclui root)
UAB_REMOTE_AGENT="${UAB_REMOTE_AGENT:-no}"; UAB_REMOTE_URL="${UAB_REMOTE_URL:-}"
UAB_TSEC_AGENT="${UAB_TSEC_AGENT:-no}"; UAB_TSEC_URL="${UAB_TSEC_URL:-}"
UAB_HOSTNAME="${UAB_HOSTNAME:-$(hostname -s)}"
UAB_EXTRA_PACKAGES="${UAB_EXTRA_PACKAGES:-}"
. /etc/os-release
CODENAME="${VERSION_CODENAME:-${UBUNTU_CODENAME:-}}"

FAILED=""
status_set() {  # grava chave=valor em $STATE/status
  local k="$1" v="$2" f="$STATE/status"
  touch "$f"
  grep -v "^$k=" "$f" >"$f.tmp" 2>/dev/null || true
  printf '%s=%s\n' "$k" "$v" >>"$f.tmp"
  mv -f "$f.tmp" "$f"
}
# Motivo curto da falha de uma etapa: a última linha útil que ela escreveu no log
# (o painel mostra ao lado de "falhou"). Casos comuns ganham uma frase clara.
fail_reason() {
  local from="$1" out
  sleep 0.5  # a saída passa por um tee: deixa chegar ao log
  out="$(tail -n +"$from" "$LOG" 2>/dev/null | grep -v '^\s*$' | grep -v ' uab-provision: >>> ' \
         | sed -E 's/^[0-9-]+ [0-9:]+ uab-provision: //' | tail -n 40)"
  if grep -qi "dpkg was interrupted" <<<"$out"; then echo "instalação de pacotes interrompida (reinício no meio); corrigindo sozinho"
  elif grep -qiE "Could not resolve|Temporary failure resolving|Failed to fetch|Network is unreachable" <<<"$out"; then echo "sem internet ou repositório fora do ar"
  elif grep -qiE "Unable to acquire the dpkg|Could not get lock" <<<"$out"; then echo "outra instalação de pacotes em andamento; tento de novo"
  else tail -n 1 <<<"$out" | tr -d '\r' | cut -c1-160
  fi
}
step() {
  local name="$1"; shift
  if [ -f "$STATE/$name.done" ]; then return 0; fi
  local from; from=$(( $(wc -l <"$LOG") + 1 ))
  status_set current "$name"
  log ">>> $name"
  if "$@"; then
    touch "$STATE/$name.done"; status_set "$name" ok; log "<<< $name: OK"
  else
    FAILED="$FAILED $name"; status_set "$name" "falhou $(date '+%F %T'): $(fail_reason "$from")"
    log "<<< $name: FALHOU (nova tentativa automática)"
  fi
}

# Reinício no meio de uma instalação deixa o dpkg "interrompido" e, a partir daí,
# TODA instalação de pacote recusa rodar. Conserta antes das etapas (idempotente).
apt_repair() {
  [ -n "$(ls -A /var/lib/dpkg/updates 2>/dev/null)" ] || [ -n "$(dpkg --audit 2>/dev/null)" ] || return 0
  log "dpkg ficou interrompido (reinício no meio de uma instalação?): corrigindo com dpkg --configure -a"
  local i
  for i in $(seq 1 30); do  # espera se outra instalação (ex.: atualização automática) estiver rodando
    dpkg --force-confdef --force-confold --configure -a && break
    sleep 10
  done
  apt-get "${APT_OPTS[@]}" -f install || true
}

# Aviso ao entrar por SSH enquanto a instalação não termina (não reiniciar no meio).
install_motd() {
  [ -d /etc/update-motd.d ] || return 0
  cat >/etc/update-motd.d/99-uab-provision <<'MOTD'
#!/bin/sh
# ISO Builder: avisa enquanto o provisionamento não terminou.
s=/var/lib/uab/status
[ -r "$s" ] || exit 0
p=$(sed -n 's/^provisioned=//p' "$s")
case "$p" in ok*) exit 0 ;; esac
c=$(sed -n 's/^current=//p' "$s")
echo
echo "  *** ISO Builder: a instalação dos programas ainda NÃO terminou${p:+ ($p)}."
[ -n "$c" ] && echo "  *** Etapa atual: $c"
echo "  *** Evite reiniciar até terminar. Acompanhe: sudo tail -f /var/log/uab-provision.log"
echo
MOTD
  chmod 755 /etc/update-motd.d/99-uab-provision
}

# Etapas que esta instalação vai rodar (o painel mostra "X de Y").
plan_steps() {
  local p="ssh_keys ssh_harden ssh_port base_packages"
  [ -n "$UAB_EXTRA_PACKAGES" ] && p="$p extra_packages"
  [ "$UAB_TAILSCALE" = yes ] && p="$p tailscale_install tailscale_login"
  [ "${UAB_WG:-no}" = yes ] && p="$p wireguard_install"
  [ "$UAB_DOCKER" = yes ] && p="$p docker_install"
  [ "$UAB_COCKPIT" = yes ] && p="$p cockpit_install"
  [ "$UAB_COOLIFY" = yes ] && p="$p coolify_install"
  [ "$UAB_VIRT" = yes ] && p="$p virt_install" && [ "$UAB_VIRT_BRIDGE" = yes ] && p="$p virt_bridge"
  [ "$UAB_TWEAKS" = yes ] && p="$p server_tweaks"
  [ "$UAB_CLAUDE_CODE" = yes ] && p="$p claude_code"
  [ "$UAB_GPU" = yes ] && p="$p gpu_driver gpu_toolkit"
  [ "$UAB_DDNS" = yes ] && p="$p ddns_setup"
  [ "$UAB_REMOTE_AGENT" = yes ] && [ -n "$UAB_REMOTE_URL" ] && p="$p remote_agent"
  [ "$UAB_TSEC_AGENT" = yes ] && [ -n "$UAB_TSEC_URL" ] && p="$p terminalsec_agent"
  [ "$UAB_CLOUDFLARE" = yes ] && p="$p cloudflare_setup"
  [ "$UAB_GPU" = yes ] && p="$p gpu_check"
  echo "$p"
}

apt_install() { apt-get "${APT_OPTS[@]}" install "$@"; }

fetch_key() {  # url destino fingerprint
  local url="$1" dest="$2" fpr="$3" tmp got
  tmp="$(mktemp)"
  curl -fsSL --retry 3 --max-time 60 "$url" -o "$tmp" || { rm -f "$tmp"; return 1; }
  got="$(gpg --show-keys --with-colons "$tmp" 2>/dev/null | awk -F: '/^fpr:/{print $10; exit}')"
  if [ "$got" != "$fpr" ]; then
    log "fingerprint inesperado para $url: '$got' (esperado $fpr)"; rm -f "$tmp"; return 1
  fi
  install -D -m 0644 "$tmp" "$dest"; rm -f "$tmp"
}

# ------------------------------------------------------------------ etapas
wait_cloud_init() {
  if command -v cloud-init >/dev/null 2>&1; then
    timeout 900 cloud-init status --wait >/dev/null 2>&1 || log "cloud-init ainda não terminou/retornou erro; seguindo"
  fi
  local i
  for i in $(seq 1 60); do id "$USER_NAME" >/dev/null 2>&1 && return 0; sleep 5; done
  log "usuário $USER_NAME não existe"; return 1
}

ssh_keys() {
  wait_cloud_init || return 1
  local home ak tmp line n=0
  home="$(getent passwd "$USER_NAME" | cut -d: -f6)"
  [ -n "$home" ] && [ -d "$home" ] || { log "home de $USER_NAME não encontrado"; return 1; }
  install -d -m 0700 -o "$USER_NAME" -g "$(id -gn "$USER_NAME")" "$home/.ssh"
  ak="$home/.ssh/authorized_keys"
  touch "$ak"
  # Garante que todas as chaves embarcadas na ISO estão presentes (sem duplicar).
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in ''|'#'*) continue ;; esac
    grep -qxF -- "$line" "$ak" || printf '%s\n' "$line" >>"$ak"
  done </etc/uab/authorized_keys
  # Chave do painel (terminal no navegador): só aceita conexões vindas do
  # Tailscale (opção from=). Não conta como chave do usuário.
  if [ -s /etc/uab/panel_key ]; then
    line="$(head -n1 /etc/uab/panel_key)"
    grep -qxF -- "$line" "$ak" || printf '%s\n' "$line" >>"$ak"
  fi
  chown "$USER_NAME:$(id -gn "$USER_NAME")" "$ak"; chmod 600 "$ak"
  chmod go-w "$home"
  # Opção da ISO: liberar o root por SSH, SÓ por chave (a mesma do usuário). Nunca por senha —
  # o ssh_harden usa PermitRootLogin prohibit-password. O root NÃO recebe a chave do painel
  # (from=): essa é exclusiva do usuário/terminal do painel.
  if [ "$SSH_ROOT" = yes ]; then
    local rak=/root/.ssh/authorized_keys
    install -d -m 0700 -o root -g root /root/.ssh
    touch "$rak"
    while IFS= read -r line || [ -n "$line" ]; do
      case "$line" in ''|'#'*) continue ;; esac
      grep -qxF -- "$line" "$rak" || printf '%s\n' "$line" >>"$rak"
    done </etc/uab/authorized_keys
    chown root:root "$rak"; chmod 600 "$rak"
    log "root por SSH liberado (só chave); chaves do usuário copiadas para $rak"
  fi
  tmp="$(mktemp)"
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in ''|'#'*|from=*) continue ;; esac   # from= = chave do painel
    printf '%s\n' "$line" >"$tmp"
    if ssh-keygen -l -f "$tmp" >/dev/null 2>&1; then n=$((n+1)); fi
  done <"$ak"
  rm -f "$tmp"
  log "$n chave(s) pública(s) válida(s) em $ak"
  # No modo senha as chaves são opcionais (podem ser adicionadas depois).
  [ "$n" -ge 1 ] || [ "$SSH_AUTH" = password ]
}

coolify_ssh_block() {
  [ "$UAB_COOLIFY" = yes ] || return 0
  echo "# Coolify: root SÓ por chave e SÓ a partir das redes do Docker (containers do Coolify)."
  echo "Match Address $DOCKER_NETS"
  echo "    PermitRootLogin prohibit-password"
}

ssh_harden() {
  # Modo chave: só roda depois de ssh_keys confirmar ao menos uma chave válida.
  [ -f "$STATE/ssh_keys.done" ] || { log "chave SSH ainda não confirmada; senha continua habilitada"; return 1; }
  # root por SSH: 'no' (bloqueado) por padrão; 'prohibit-password' (SÓ chave, NUNCA senha) se
  # a opção foi marcada na ISO (UAB_SSH_ROOT=yes). prohibit-password recusa senha do root em
  # qualquer rede — inclusive a local — então o root entra apenas com a chave conhecida.
  local rootline=no
  [ "$SSH_ROOT" = yes ] && rootline=prohibit-password
  if [ "$SSH_AUTH" = password ]; then
    {
      echo "# ubuntu-autoinstall-builder (modo usuário e senha)."
      echo "# Lido antes de 50-cloud-init.conf, por isso prevalece."
      echo "# Senha aceita SOMENTE da rede local e do Tailscale; de IPs públicos, só chave."
      echo "PermitRootLogin $rootline"
      echo "PubkeyAuthentication yes"
      echo "KbdInteractiveAuthentication no"
      echo "PasswordAuthentication no"
      echo "MaxAuthTries 4"
      echo "LoginGraceTime 30"
      echo "Match Address $PW_NETS"
      echo "    PasswordAuthentication yes"
    } >"$SSHD_DROPIN.new"
    coolify_ssh_block >>"$SSHD_DROPIN.new"
  else
    {
      echo "# ubuntu-autoinstall-builder: aplicado após confirmar chave pública válida."
      echo "# Lido antes de 50-cloud-init.conf, por isso prevalece."
      echo "PermitRootLogin $rootline"
      echo "PubkeyAuthentication yes"
      echo "PasswordAuthentication no"
      echo "KbdInteractiveAuthentication no"
    } >"$SSHD_DROPIN.new"
    coolify_ssh_block >>"$SSHD_DROPIN.new"
  fi
  chmod 644 "$SSHD_DROPIN.new"
  # Com ativação por socket (Ubuntu 24.04+), /run/sshd só existe após a
  # primeira conexão; sshd -t/-T exigem o diretório.
  install -d -m 0755 /run/sshd
  mv -f "$SSHD_DROPIN.new" "$SSHD_DROPIN"
  if ! sshd -t; then
    log "sshd -t falhou; removendo endurecimento para não perder acesso"; rm -f "$SSHD_DROPIN"; return 1
  fi
  local eff rootpat
  eff="$(sshd -T -C "user=$USER_NAME,host=localhost,addr=127.0.0.1" 2>/dev/null)"
  # prohibit-password e without-password são sinônimos; o sshd -T pode imprimir qualquer um.
  rootpat="permitrootlogin $rootline"
  [ "$rootline" = prohibit-password ] && rootpat='permitrootlogin (without-password|prohibit-password)'
  printf '%s\n' "$eff" | grep -qiE "^$rootpat" || { rm -f "$SSHD_DROPIN"; log "PermitRootLogin não ficou '$rootline'; revertendo"; return 1; }
  if [ "$SSH_AUTH" = password ]; then
    # rede local: senha aceita; IP público: senha recusada
    sshd -T -C "user=$USER_NAME,host=lan,addr=192.168.1.10" 2>/dev/null | grep -qi '^passwordauthentication yes' \
      || { rm -f "$SSHD_DROPIN"; log "senha não ficou liberada na rede local; revertendo"; return 1; }
    sshd -T -C "user=$USER_NAME,host=wan,addr=203.0.113.10" 2>/dev/null | grep -qi '^passwordauthentication no' \
      || { log "senha não ficou bloqueada para IPs públicos"; return 1; }
  else
    printf '%s\n' "$eff" | grep -qi '^passwordauthentication no' || { log "PasswordAuthentication não ficou 'no'"; return 1; }
  fi
  printf '%s\n' "$eff" | grep -qi '^pubkeyauthentication yes' || { rm -f "$SSHD_DROPIN"; log "pubkey desativado?! revertendo"; return 1; }
  if [ "$SSH_ROOT" = yes ]; then
    # opção da ISO: root liberado em qualquer rede, porém SÓ por chave (prohibit-password),
    # NUNCA por senha. Confirma nas duas pontas (rede local e IP público).
    for ctx in "host=lan,addr=192.168.1.10" "host=wan,addr=203.0.113.10"; do
      sshd -T -C "user=root,$ctx" 2>/dev/null | grep -qiE '^permitrootlogin (without-password|prohibit-password)' \
        || { rm -f "$SSHD_DROPIN"; log "root não ficou só-chave ($ctx); revertendo"; return 1; }
    done
  elif [ "$UAB_COOLIFY" = yes ]; then
    # root: só chave e só das redes do Docker; da rede local e da internet, bloqueado
    sshd -T -C "user=root,host=coolify,addr=172.30.0.10" 2>/dev/null | grep -qiE '^permitrootlogin (without-password|prohibit-password)' \
      || { log "regra de root para o Coolify não aplicada"; return 1; }
    sshd -T -C "user=root,host=lan,addr=192.168.1.10" 2>/dev/null | grep -qi '^permitrootlogin no' \
      || { rm -f "$SSHD_DROPIN"; log "root ficou liberado fora do Docker; revertendo"; return 1; }
  fi
  systemctl enable ssh.socket >/dev/null 2>&1 || systemctl enable ssh.service >/dev/null 2>&1 || true
  if systemctl is-active --quiet ssh.service; then systemctl reload ssh.service || systemctl restart ssh.service; fi
  systemctl is-active --quiet ssh.socket || systemctl is-active --quiet ssh.service || systemctl start ssh.socket || systemctl start ssh.service
}

# Porta do SSH escolhida na ISO. Muda só depois de confirmar que o sshd aceita a
# configuração E está escutando na porta nova; senão volta para a 22 (sem perder acesso).
ssh_listening() { ss -Hltn "sport = :$1" 2>/dev/null | grep -q .; }
ssh_apply_port() {
  systemctl daemon-reload  # o sshd-socket-generator refaz o ssh.socket com as portas do sshd_config
  if systemctl is-enabled --quiet ssh.socket 2>/dev/null; then systemctl restart ssh.socket
  else systemctl restart ssh.service; fi
}
ssh_port() {
  if [ "$SSH_PORT" = 22 ]; then rm -f "$SSHD_PORT_DROPIN" "$F2B_PORT_CONF"; return 0; fi
  printf '# ubuntu-autoinstall-builder: porta do SSH escolhida na ISO\nPort %s\n' "$SSH_PORT" >"$SSHD_PORT_DROPIN.new"
  chmod 644 "$SSHD_PORT_DROPIN.new"; mv -f "$SSHD_PORT_DROPIN.new" "$SSHD_PORT_DROPIN"
  install -d -m 0755 /run/sshd
  if ! sshd -t || [ "$(sshd -T 2>/dev/null | awk '$1=="port"{print $2}' | sort -u)" != "$SSH_PORT" ]; then
    rm -f "$SSHD_PORT_DROPIN"; log "sshd não aceitou a porta $SSH_PORT; SSH continua na 22"; return 1
  fi
  # fail2ban (modo senha) precisa bloquear na porta certa
  install -d -m 0755 /etc/fail2ban/jail.d
  printf '# ubuntu-autoinstall-builder: SSH na porta %s\n[sshd]\nport = %s\n' "$SSH_PORT" "$SSH_PORT" >"$F2B_PORT_CONF"
  ssh_apply_port
  local i
  for i in $(seq 1 15); do ssh_listening "$SSH_PORT" && break; sleep 1; done
  if ! ssh_listening "$SSH_PORT"; then
    log "SSH não abriu a porta $SSH_PORT; voltando para a 22"
    rm -f "$SSHD_PORT_DROPIN" "$F2B_PORT_CONF"; ssh_apply_port; return 1
  fi
  systemctl is-active --quiet fail2ban 2>/dev/null && systemctl reload fail2ban 2>/dev/null || true
  log "SSH na porta $SSH_PORT"
}

# ------------------------------------------------------------------ extras (vindos do host-mirror)
# Ajustes de base iguais aos do servidor de referência: fsck automático no boot
# (repara sozinho após queda de energia), console sem apagar e, com virtualização,
# IOMMU ligado conforme o fabricante da CPU (para passar dispositivos às VMs).
server_tweaks() {
  local extra="fsck.mode=force fsck.repair=yes consoleblank=0"
  if [ "$UAB_VIRT" = yes ]; then
    if grep -q GenuineIntel /proc/cpuinfo; then extra="$extra intel_iommu=on iommu=pt"
    elif grep -q AuthenticAMD /proc/cpuinfo; then extra="$extra amd_iommu=on iommu=pt"; fi
  fi
  install -d /etc/default/grub.d
  printf '# ubuntu-autoinstall-builder: ajustes de servidor\nGRUB_CMDLINE_LINUX_DEFAULT="$GRUB_CMDLINE_LINUX_DEFAULT %s"\n' "$extra" \
    >/etc/default/grub.d/90-uab-server.cfg
  update-grub >/dev/null 2>&1 || { log "update-grub falhou"; rm -f /etc/default/grub.d/90-uab-server.cfg; return 1; }
  grep -q "consoleblank=0" /boot/grub/grub.cfg || { log "ajustes não entraram no grub.cfg"; return 1; }
  log "ajustes de boot aplicados (valem a partir do próximo boot): $extra"
}

# Claude Code (terminal) para o usuário administrador, pelo script oficial da Anthropic.
# Instala no HOME do usuário (não do root), como o instalador recomenda.
claude_code() {
  command -v curl >/dev/null 2>&1 || apt_install curl || return 1
  local home bin
  home="$(getent passwd "$USER_NAME" | cut -d: -f6)"
  # instala no HOME do usuário (não do root); -H ajusta o HOME para o do usuário
  sudo -u "$USER_NAME" -H bash -lc 'curl -fsSL https://claude.ai/install.sh | bash' >>"$LOG" 2>&1 || return 1
  sudo -u "$USER_NAME" -H bash -lc 'command -v claude >/dev/null 2>&1 || [ -x "$HOME/.local/bin/claude" ]' \
    || { log "Claude Code instalado, mas o binário não apareceu"; return 1; }
  log "Claude Code instalado para $USER_NAME"
  # Opção da ISO: disponibilizar o 'claude' para TODOS os usuários — inclusive root (via
  # 'sudo -i' ou root por SSH) — com um link em /usr/local/bin (PATH padrão, até em shell
  # não-interativo). É um só binário; cada usuário tem sua própria config em ~/.claude e faz
  # seu login (nada compartilhado além do executável). Sem a opção, fica só no $USER_NAME.
  if [ "$CLAUDE_ALL" = yes ]; then
    bin="$home/.local/bin/claude"
    [ -x "$bin" ] && { ln -sf "$bin" /usr/local/bin/claude; log "Claude Code em /usr/local/bin/claude (todos os usuários, inclusive root)"; }
  else
    rm -f /usr/local/bin/claude 2>/dev/null  # garante que não sobrou link de uma config anterior
  fi
}

# DDNS No-IP com ddclient. A senha fica só em /etc/ddclient.conf (0600, root).
ddns_setup() {
  local pwf=/etc/uab/ddns-password pw
  if [ ! -s "$pwf" ]; then
    grep -qs "ubuntu-autoinstall-builder" /etc/ddclient.conf && return 0
    log "senha do No-IP ausente"; return 1
  fi
  apt_install ddclient || return 1
  pw="$(head -n1 "$pwf")"
  (umask 077; {
    echo "# ubuntu-autoinstall-builder: DDNS No-IP"
    echo "daemon=300"
    echo "syslog=yes"
    echo "ssl=yes"
    echo "usev4=webv4"
    echo "webv4=ipify-ipv4"
    echo "protocol=noip"
    echo "login=$UAB_DDNS_LOGIN"
    printf "password='%s'\n" "${pw//\'/\\\'}"
    echo "$UAB_DDNS_HOSTNAME"
  } >/etc/ddclient.conf.new) || return 1
  pw=""
  chmod 600 /etc/ddclient.conf.new; mv -f /etc/ddclient.conf.new /etc/ddclient.conf
  [ -f /etc/default/ddclient ] && sed -i 's/^run_daemon=.*/run_daemon="true"/; s/^daemon_interval=.*/daemon_interval="300"/' /etc/default/ddclient
  systemctl enable ddclient >/dev/null 2>&1 || true
  systemctl restart ddclient || return 1
  sleep 3; systemctl is-active --quiet ddclient || { log "ddclient não ficou ativo"; return 1; }
  shred -u -z "$pwf" 2>/dev/null || rm -f "$pwf"
  log "DDNS No-IP: $UAB_DDNS_HOSTNAME atualizado a cada 5 min (ddclient)"
}

# Cloudflare Tunnel (túnel gerenciado pelo painel da Cloudflare: os domínios são
# definidos lá). O token fica em /etc/cloudflared/token.env (0600), fora da linha de comando.
cloudflare_setup() {
  local tf=/etc/uab/cloudflare-token env=/etc/cloudflared/token.env i
  if [ -s "$tf" ]; then
    install -d -m 0700 /etc/cloudflared
    (umask 077; printf 'TUNNEL_TOKEN=%s\n' "$(head -n1 "$tf")" >"$env") || return 1
  fi
  [ -s "$env" ] || { log "token do Cloudflare Tunnel ausente"; return 1; }
  command -v gpg >/dev/null 2>&1 || apt_install gnupg || return 1
  fetch_key https://pkg.cloudflare.com/cloudflare-public-v2.gpg /usr/share/keyrings/cloudflare-public-v2.gpg "$CLOUDFLARE_FPR" || return 1
  echo "deb [signed-by=/usr/share/keyrings/cloudflare-public-v2.gpg] https://pkg.cloudflare.com/cloudflared any main" \
    >/etc/apt/sources.list.d/cloudflared.list
  apt-get -q -o DPkg::Lock::Timeout=900 update || return 1
  apt_install cloudflared || return 1
  printf '%s\n' \
    '[Unit]' \
    'Description=Cloudflare Tunnel (ubuntu-autoinstall-builder)' \
    'After=network-online.target' \
    'Wants=network-online.target' \
    '' \
    '[Service]' \
    'EnvironmentFile=/etc/cloudflared/token.env' \
    'ExecStart=/usr/bin/cloudflared --no-autoupdate tunnel run' \
    'Restart=on-failure' \
    'RestartSec=10' \
    '' \
    '[Install]' \
    'WantedBy=multi-user.target' >/etc/systemd/system/cloudflared.service
  systemctl daemon-reload
  systemctl enable --now cloudflared || return 1
  if [ -s "$tf" ]; then shred -u -z "$tf" 2>/dev/null || rm -f "$tf"; fi
  for i in $(seq 1 20); do
    if journalctl -u cloudflared --since "-3min" --no-pager 2>/dev/null | grep -q "Registered tunnel connection"; then
      status_set cloudflare_tunnel "conectado"; log "Cloudflare Tunnel conectado"; return 0
    fi
    sleep 3
  done
  # token inválido ou Cloudflare fora do ar: o serviço continua tentando sozinho
  status_set cloudflare_tunnel "instalado; ainda sem conexão (confira o token no painel da Cloudflare)"
  log "Cloudflare Tunnel instalado, mas ainda sem conexão confirmada"
}

# Virtualização: KVM/libvirt (+ Cockpit Máquinas, se o Cockpit foi escolhido).
virt_install() {
  local pool dir
  apt_install qemu-kvm qemu-utils libvirt-daemon-system libvirt-clients virtinst ovmf swtpm-tools || return 1
  systemctl enable --now libvirtd || return 1
  usermod -aG libvirt,kvm "$USER_NAME" || return 1
  install -d -m 0711 /var/lib/libvirt/images /var/lib/libvirt/isos
  for pool in default isos; do
    dir=/var/lib/libvirt/images; [ "$pool" = isos ] && dir=/var/lib/libvirt/isos
    virsh pool-info "$pool" >/dev/null 2>&1 || virsh pool-define-as "$pool" dir --target "$dir" >/dev/null || return 1
    virsh pool-autostart "$pool" >/dev/null 2>&1 || true
    virsh pool-start "$pool" >/dev/null 2>&1 || true
  done
  if [ -e /dev/kvm ]; then status_set virt_info "KVM ok"; else status_set virt_info "sem /dev/kvm: ative VT-x/AMD-V na BIOS"; fi
  virsh -c qemu:///system list --all >/dev/null
}

# Ponte br0 para as VMs ficarem na rede local. Aplicada com proteção: se a rede
# não voltar em 90 s, restaura o netplan anterior (não perde o acesso).
virt_bridge() {
  local nic mac bk="$STATE/netplan-before-br0" tpl=/opt/uab/netplan-br0.yaml gw i ok=0
  if ip route show default 2>/dev/null | grep -q " dev br0"; then return 0; fi
  if [ -f "$STATE/virt_bridge.failed" ]; then log "ponte br0 já falhou antes; não tento de novo (veja o log)"; return 0; fi
  [ -s "$tpl" ] || { log "modelo da ponte ausente"; return 1; }
  nic="$(ip route show default | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
  { [ -n "$nic" ] && [ -e "/sys/class/net/$nic/device" ]; } || { log "placa de rede principal não encontrada"; return 1; }
  mac="$(cat "/sys/class/net/$nic/address")"
  rm -rf "$bk"; mkdir -p "$bk"; cp -a /etc/netplan/. "$bk/"
  sed "s/__MAC__/$mac/g" "$tpl" >"$STATE/netplan-br0.yaml"
  find /etc/netplan -maxdepth 1 -name '*.yaml' -delete
  install -m 0600 "$STATE/netplan-br0.yaml" /etc/netplan/50-cloud-init.yaml
  if ! netplan generate 2>>"$LOG"; then
    log "netplan da ponte inválido; restaurando"
    find /etc/netplan -maxdepth 1 -name '*.yaml' -delete; cp -a "$bk/." /etc/netplan/; netplan generate; return 1
  fi
  log "aplicando ponte br0 sobre $nic ($mac)"
  netplan apply 2>>"$LOG"
  for i in $(seq 1 45); do
    gw="$(ip route show default dev br0 2>/dev/null | awk '{print $3; exit}')"
    if [ -n "$gw" ] && ping -c1 -W2 "$gw" >/dev/null 2>&1 && getent hosts archive.ubuntu.com >/dev/null 2>&1; then ok=1; break; fi
    sleep 2
  done
  if [ "$ok" != 1 ]; then
    log "rede não voltou com a ponte br0; restaurando a configuração anterior"
    find /etc/netplan -maxdepth 1 -name '*.yaml' -delete; cp -a "$bk/." /etc/netplan/; netplan apply 2>>"$LOG"
    touch "$STATE/virt_bridge.failed"; status_set virt_bridge_info "falhou: rede restaurada sem a ponte"
    return 0
  fi
  # rede do libvirt que usa a ponte (as VMs recebem IP do seu roteador)
  if ! virsh net-info br0 >/dev/null 2>&1; then
    printf '<network><name>br0</name><forward mode="bridge"/><bridge name="br0"/></network>\n' >"$STATE/br0-net.xml"
    virsh net-define "$STATE/br0-net.xml" >/dev/null && virsh net-autostart br0 >/dev/null && virsh net-start br0 >/dev/null
  fi
  status_set virt_bridge_info "br0 $(ip -4 -o addr show br0 | awk '{print $4; exit}')"
  log "ponte br0 ativa ($(ip -4 -o addr show br0 | awk '{print $4; exit}'))"
}

# Placa de vídeo (qualquer marca): detecta pelo fabricante no PCI e instala o que
# cada uma precisa. Sem placa (VM, servidor sem vídeo), não instala nada.
#   NVIDIA (10de): driver recomendado pelo Ubuntu (ubuntu-drivers) + GPU no Docker
#                  (nvidia-container-toolkit); o driver só carrega reiniciando, então
#                  o provisionador reinicia UMA vez sozinho (gpu_check).
#   AMD (1002):    driver já vem no kernel (amdgpu); instala firmware, vídeo por
#                  hardware (VA-API) e 3D/Vulkan (Mesa). ROCm não vai no host: os
#                  containers ROCm já trazem o seu, só precisam de /dev/kfd e /dev/dri.
#   Intel (8086):  driver no kernel (i915/xe); instala firmware, QuickSync (VA-API)
#                  e OpenCL (containers com /dev/dri, ex.: Jellyfin, Frigate).
# Máquina híbrida (ex.: Intel + NVIDIA) recebe as duas coisas.
gpu_vendors() {  # fabricantes de placas de vídeo (classe PCI 03xx), sem repetir
  lspci -n 2>/dev/null | awk '$2 ~ /^03/ { split($3, id, ":"); v = id[1]
    if (v == "10de") n = "nvidia"; else if (v == "1002") n = "amd"; else if (v == "8086") n = "intel"; else next
    if (!(n in seen)) { seen[n] = 1; printf "%s ", n } }'
}
gpu_has() { case " $(gpu_vendors) " in *" $1 "*) return 0 ;; esac; return 1; }
gpu_nvidia_present() { gpu_has nvidia; }
apt_has() { [ -n "$(apt-cache policy "$1" 2>/dev/null | awk '/Candidate:/{print $2}' | grep -v none)" ]; }
apt_install_avail() {  # instala só os pacotes que existem nesta versão do Ubuntu
  local p ok=()
  for p in "$@"; do apt_has "$p" && ok+=("$p"); done
  [ ${#ok[@]} -eq 0 ] || apt_install "${ok[@]}"
}
gpu_driver() {
  command -v lspci >/dev/null 2>&1 || apt_install pciutils || return 1
  local vendors info=() drv
  vendors="$(gpu_vendors)"
  if [ -z "$vendors" ]; then status_set gpu_info "nenhuma placa de vídeo encontrada"; log "GPU: nenhuma placa encontrada; nada a instalar"; return 0; fi
  log "GPU: encontrada(s): $vendors"
  if gpu_has amd || gpu_has intel; then
    apt_install_avail linux-firmware mesa-va-drivers mesa-vulkan-drivers vainfo clinfo || return 1
  fi
  if gpu_has intel; then
    # QuickSync: driver completo (multiverse) com recuo para o livre; i965 cobre as gerações antigas
    if apt_has intel-media-va-driver-non-free; then apt_install intel-media-va-driver-non-free || return 1
    else apt_install_avail intel-media-va-driver || return 1; fi
    apt_install_avail i965-va-driver intel-opencl-icd || return 1
    info+=("Intel: vídeo por hardware (QuickSync) e OpenCL")
  fi
  if gpu_has amd; then
    apt_install_avail mesa-opencl-icd rocm-smi rocminfo || true  # extras: nunca travam a etapa
    info+=("AMD: vídeo por hardware (VA-API) e Vulkan")
  fi
  if gpu_has nvidia; then
    if command -v mokutil >/dev/null 2>&1 && mokutil --sb-state 2>/dev/null | grep -qi "enabled"; then
      log "Secure Boot ligado: usando módulos assinados do Ubuntu (se não houver, desligue o Secure Boot na BIOS)"
    fi
    apt_install ubuntu-drivers-common || return 1
    ubuntu-drivers install || return 1
    drv="$(dpkg -l 'nvidia-driver-*' 2>/dev/null | awk '/^ii/{print $2; exit}')"
    [ -n "$drv" ] || { log "nenhum driver NVIDIA foi instalado"; return 1; }
    info+=("NVIDIA: $drv")
  fi
  # containers e o usuário admin acessam /dev/dri (e /dev/kfd da AMD) pelos grupos video/render
  getent group render >/dev/null || groupadd -r render
  usermod -aG video,render "$USER_NAME" 2>/dev/null || true
  status_set gpu_info "$(IFS=';'; echo "${info[*]}" | sed 's/;/ · /g')"
}
gpu_toolkit() {  # GPU NVIDIA nos containers: usa o Docker/Coolify já instalado (não instala Docker de novo)
  gpu_nvidia_present || return 0
  command -v docker >/dev/null 2>&1 || return 0
  command -v gpg >/dev/null 2>&1 || apt_install gnupg || return 1
  fetch_key https://nvidia.github.io/libnvidia-container/gpgkey /usr/share/keyrings/nvidia-container-toolkit.asc "$NVIDIA_CT_FPR" || return 1
  echo "deb [signed-by=/usr/share/keyrings/nvidia-container-toolkit.asc] https://nvidia.github.io/libnvidia-container/stable/deb/\$(ARCH) /" \
    >/etc/apt/sources.list.d/nvidia-container-toolkit.list
  apt-get -q -o DPkg::Lock::Timeout=900 update || return 1
  apt_install nvidia-container-toolkit || return 1
  nvidia-ctk runtime configure --runtime=docker >/dev/null || return 1
  systemctl restart docker
}
gpu_check() {  # o que ficou funcionando de fato (vai para o Resumo da máquina)
  local out=()
  if gpu_has nvidia; then
    nvidia-smi -L >/dev/null 2>&1 || { log "driver NVIDIA instalado, mas ainda não carregado (precisa reiniciar)"; return 1; }
    out+=("$(nvidia-smi --query-gpu=name,driver_version --format=csv,noheader 2>/dev/null | head -n1 | sed 's/, / · driver /')")
  fi
  if gpu_has amd || gpu_has intel; then
    if ls /dev/dri/renderD* >/dev/null 2>&1; then
      local va
      va="$(vainfo --display drm 2>/dev/null | sed -n 's/.*Driver version: *//p' | head -n1)"
      gpu_has amd && out+=("AMD $( [ -e /dev/kfd ] && echo "(ROCm pronto p/ containers)" ) ${va:+· $va}")
      gpu_has intel && out+=("Intel ${va:+· $va}")
    else
      log "placa AMD/Intel detectada, mas sem /dev/dri (o driver do kernel não carregou; veja 'dmesg | grep -i drm')"
      out+=("AMD/Intel sem /dev/dri: driver do kernel não carregou")
    fi
  fi
  [ ${#out[@]} -eq 0 ] || status_set gpu_info "$(printf '%s\n' "${out[@]}" | sed 's/  */ /g; s/ *$//' | paste -sd'|' | sed 's/|/ · /g')"
}

# Agente do Remoto (MeshCentral do ISO Builder): o servidor aparece no grupo
# "Servidores ISO Builder" em <painel>/remoto/. O id do grupo vem em /etc/uab (0600)
# e é apagado depois de instalar (o agente guarda o que precisa no .msh dele).
remote_agent() {
  local f=/etc/uab/remote-meshid tmp i
  if systemctl is-active --quiet meshagent 2>/dev/null; then
    [ -f "$f" ] && { shred -u -z "$f" 2>/dev/null || rm -f "$f"; }
    return 0
  fi
  [ -s "$f" ] || { log "id do grupo do Remoto ausente"; return 1; }
  tmp="$(mktemp -d)"
  if ! curl -fsSL --retry 3 --max-time 120 "$UAB_REMOTE_URL/meshagents?script=1" -o "$tmp/meshinstall.sh"; then
    rm -rf "$tmp"; log "não foi possível baixar o instalador do agente de $UAB_REMOTE_URL"; return 1
  fi
  (cd "$tmp" && bash ./meshinstall.sh "$UAB_REMOTE_URL" "$(head -n1 "$f")") >>"$LOG" 2>&1
  rm -rf "$tmp"
  for i in $(seq 1 30); do systemctl is-active --quiet meshagent && break; sleep 2; done
  systemctl is-active --quiet meshagent || { log "agente do Remoto não ficou ativo"; return 1; }
  shred -u -z "$f" 2>/dev/null || rm -f "$f"
  log "agente do Remoto instalado: o servidor aparece em $UAB_REMOTE_URL/"
}

# ------------------------------------------------------------------ TerminalSec
# Instala o agente de monitoramento de segurança. O servidor aparece no TerminalSec como
# "aguardando aprovação" até alguém aceitar no painel. Códigos do install.sh:
#   0 ok · 3 token recusado (revogado: NÃO tenta de novo) · outro = rede/painel fora do ar
TSEC_TOKEN_FILE=/etc/uab/tsec-token

tsec_forget_token() { [ -f "$TSEC_TOKEN_FILE" ] && { shred -u -z "$TSEC_TOKEN_FILE" 2>/dev/null || rm -f "$TSEC_TOKEN_FILE"; }; return 0; }

tsec_try_install() {
  local tmp rc
  tmp="$(mktemp -d)"
  if ! curl -fsSL --retry 3 --max-time 120 "$UAB_TSEC_URL/install.sh" -o "$tmp/install.sh" || ! grep -q TerminalSec "$tmp/install.sh"; then
    rm -rf "$tmp"; log "não foi possível baixar o instalador do TerminalSec de $UAB_TSEC_URL"; return 2
  fi
  TSEC_TOKEN="$(head -n1 "$TSEC_TOKEN_FILE")" TSEC_NAME="$UAB_HOSTNAME" \
    bash "$tmp/install.sh" --panel "$UAB_TSEC_URL" >>"$LOG" 2>&1
  rc=$?
  rm -rf "$tmp"
  return $rc
}

tsec_schedule_retry() {  # painel fora do ar por muito tempo: tenta de hora em hora, fora do provisionamento
  cat >/usr/local/sbin/uab-tsec-retry <<'SH'
#!/bin/bash
# Nova tentativa de instalar o agente do TerminalSec (criado pelo uab-provision). Se remove ao terminar.
. /etc/uab/uab.conf
F=/etc/uab/tsec-token
done_() { shred -u -z "$F" 2>/dev/null || rm -f "$F"; systemctl disable --now uab-tsec-retry.timer >/dev/null 2>&1; exit 0; }
systemctl is-active --quiet tsec-agent && done_
[ -s "$F" ] || done_
T="$(mktemp -d)"
curl -fsSL --retry 3 --max-time 120 "$UAB_TSEC_URL/install.sh" -o "$T/install.sh" && grep -q TerminalSec "$T/install.sh" || { rm -rf "$T"; exit 0; }
TSEC_TOKEN="$(head -n1 "$F")" TSEC_NAME="$UAB_HOSTNAME" bash "$T/install.sh" --panel "$UAB_TSEC_URL"
rc=$?; rm -rf "$T"
[ "$rc" -eq 0 ] || [ "$rc" -eq 3 ] && done_
exit 0
SH
  chmod 700 /usr/local/sbin/uab-tsec-retry
  cat >/etc/systemd/system/uab-tsec-retry.service <<'UNIT'
[Unit]
Description=TerminalSec: nova tentativa de instalar o agente
After=network-online.target
[Service]
Type=oneshot
ExecStart=/usr/local/sbin/uab-tsec-retry
UNIT
  cat >/etc/systemd/system/uab-tsec-retry.timer <<'UNIT'
[Unit]
Description=TerminalSec: tenta instalar o agente de hora em hora
[Timer]
OnBootSec=5min
OnUnitActiveSec=1h
[Install]
WantedBy=timers.target
UNIT
  systemctl daemon-reload && systemctl enable --now uab-tsec-retry.timer
}

terminalsec_agent() {
  local rc tries
  if systemctl is-active --quiet tsec-agent 2>/dev/null; then tsec_forget_token; return 0; fi
  if [ ! -s "$TSEC_TOKEN_FILE" ]; then
    log "token do TerminalSec ausente: servidor segue sem o agente (instale pelo painel do TerminalSec)"
    status_set terminalsec "sem token"; return 0
  fi
  tsec_try_install; rc=$?
  case "$rc" in
    0) tsec_forget_token
       log "agente do TerminalSec instalado: aprove o servidor em $UAB_TSEC_URL (aba servidores)"
       status_set terminalsec "aguardando aprovação"; return 0 ;;
    3) tsec_forget_token
       log "o TerminalSec recusou o token (revogado ou inválido): servidor segue sem o agente — gere um comando no painel"
       status_set terminalsec "token recusado"; return 0 ;;
  esac
  tries=$(( $(cat "$STATE/tsec.tries" 2>/dev/null || echo 0) + 1 ))
  echo "$tries" >"$STATE/tsec.tries"
  if [ "$tries" -ge 5 ]; then
    tsec_schedule_retry
    log "TerminalSec inacessível após $tries tentativas: o provisionamento segue; nova tentativa de hora em hora (uab-tsec-retry.timer)"
    status_set terminalsec "tentando de hora em hora"; return 0
  fi
  log "TerminalSec inacessível (tentativa $tries de 5)"; return 1
}

network_ready() {
  local i
  for i in $(seq 1 30); do
    if getent hosts archive.ubuntu.com >/dev/null 2>&1 && curl -fsS -o /dev/null --max-time 15 http://archive.ubuntu.com/ubuntu/; then
      return 0
    fi
    sleep 10
  done
  log "sem conectividade com a internet (DNS/HTTP)"; return 1
}

base_packages() {
  apt-get -q -o DPkg::Lock::Timeout=900 update || return 1
  # gnupg verifica as chaves dos repositórios oficiais; fail2ban bloqueia
  # tentativas repetidas de senha (modo password).
  local extra="gnupg"
  [ "$SSH_AUTH" = password ] && extra="$extra fail2ban"
  # shellcheck disable=SC2086
  apt_install $UAB_PACKAGES $extra || return 1
  if [ "$SSH_AUTH" = password ]; then
    systemctl enable --now fail2ban || return 1
  fi
}

# Pacotes extras escolhidos no assistente. Separados dos básicos: um nome que não
# existe nesta versão do Ubuntu é ignorado (com aviso) em vez de travar a instalação.
extra_packages() {
  local p ok=() skip=()
  apt-get -q -o DPkg::Lock::Timeout=900 update || return 1
  for p in $UAB_EXTRA_PACKAGES; do
    if [ -n "$(apt-cache policy "$p" 2>/dev/null | awk '/Candidate:/ && $2 != "(none)"')" ]; then ok+=("$p"); else skip+=("$p"); fi
  done
  if [ ${#ok[@]} -gt 0 ]; then apt_install "${ok[@]}" || return 1; fi
  if [ ${#skip[@]} -gt 0 ]; then
    log "pacotes extras ignorados (não existem nesta versão do Ubuntu): ${skip[*]}"
    status_set extra_packages_info "ignorados (não existem no Ubuntu $VERSION_ID): ${skip[*]}"
  fi
  return 0
}

tailscale_install() {
  command -v gpg >/dev/null 2>&1 || apt_install gnupg || return 1
  fetch_key "https://pkgs.tailscale.com/stable/ubuntu/$CODENAME.noarmor.gpg" \
    /usr/share/keyrings/tailscale-archive-keyring.gpg "$TAILSCALE_FPR" || return 1
  printf 'deb [signed-by=/usr/share/keyrings/tailscale-archive-keyring.gpg] https://pkgs.tailscale.com/stable/ubuntu %s main\n' "$CODENAME" \
    >/etc/apt/sources.list.d/tailscale.list
  apt-get -q -o DPkg::Lock::Timeout=900 update || return 1
  apt_install tailscale || return 1
  systemctl enable --now tailscaled || return 1
  systemctl is-enabled --quiet tailscaled
}

tailscale_state() { tailscale status --json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin).get("BackendState",""))' 2>/dev/null; }

tailscale_login() {
  local st ip
  st="$(tailscale_state)"
  if [ "$st" != "Running" ]; then
    if [ ! -s "$TS_KEY" ]; then
      log "Tailscale instalado, mas sem auth key. Rode no servidor: sudo tailscale up --hostname=$UAB_TAILSCALE_HOSTNAME"
      status_set tailscale_ip "nao-autenticado"
      return 0
    fi
    # A chave é lida do arquivo (file:) e nunca aparece na linha de comando/logs.
    # shellcheck disable=SC2086
    if ! timeout 180 tailscale up --auth-key="file:$TS_KEY" --hostname="$UAB_TAILSCALE_HOSTNAME" $UAB_TAILSCALE_EXTRA_ARGS; then
      log "tailscale up falhou (auth key inválida/expirada/já usada? veja README > Recuperação)"
      return 1
    fi
    st="$(tailscale_state)"
  fi
  [ "$st" = "Running" ] || { log "Tailscale em estado '$st'"; return 1; }
  ip="$(tailscale ip -4 2>/dev/null | head -n1)"
  status_set tailscale_ip "$ip"
  log "Tailscale conectado: $ip ($UAB_TAILSCALE_HOSTNAME)"
  if [ -f "$TS_KEY" ]; then shred -u -z "$TS_KEY" 2>/dev/null || rm -f "$TS_KEY"; log "auth key apagada do disco"; fi
  return 0
}

docker_install() {
  local p
  for p in docker.io docker-doc docker-compose docker-compose-v2 podman-docker containerd runc; do
    dpkg -s "$p" >/dev/null 2>&1 && apt-get "${APT_OPTS[@]}" remove "$p"
  done
  command -v gpg >/dev/null 2>&1 || apt_install gnupg || return 1
  fetch_key https://download.docker.com/linux/ubuntu/gpg /etc/apt/keyrings/docker.asc "$DOCKER_FPR" || return 1
  cat >/etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $CODENAME
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
  apt-get -q -o DPkg::Lock::Timeout=900 update || return 1
  apt_install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin || return 1
  systemctl enable --now containerd docker || return 1
  usermod -aG docker "$USER_NAME" || return 1
  docker version >/dev/null || return 1
  docker compose version || return 1
  systemctl is-enabled --quiet docker
}

wireguard_install() {
  # Túnel até o hub (seu servidor). A chave privada é gerada AQUI e nunca sai do servidor;
  # o painel recebe só a pública, pelo relatório, e devolve a configuração do túnel.
  command -v wg >/dev/null 2>&1 || apt_install wireguard-tools || return 1
  install -d -m 0700 /etc/uab/wg
  if [ ! -s /etc/uab/wg/private.key ]; then
    (umask 077; wg genkey >/etc/uab/wg/private.key) || return 1
  fi
  wg pubkey </etc/uab/wg/private.key >/etc/uab/wg/public.key || return 1
  chmod 644 /etc/uab/wg/public.key
  # registra no painel já agora (o relatório aplica o túnel quando o hub responder)
  timeout 60 /usr/local/sbin/uab-report || true
  if [ -s /etc/wireguard/uabwg.conf ]; then
    systemctl is-enabled --quiet wg-quick@uabwg || return 1
    log "WireGuard: túnel configurado ($(grep -m1 '^Address' /etc/wireguard/uabwg.conf | cut -d= -f2 | tr -d ' '))"
  else
    log "WireGuard: aguardando o painel/hub; o relatório (a cada 10 min) aplica o túnel assim que possível"
  fi
}

cockpit_install() {
  local pkgs="cockpit cockpit-bridge cockpit-machines cockpit-files cockpit-networkmanager cockpit-packagekit cockpit-storaged cockpit-podman cockpit-sosreport"
  local target=()
  apt-get -q -o DPkg::Lock::Timeout=900 update || return 1
  # Mesmo método do host-mirror: tudo numa transação a partir do -backports
  # (cockpit-files exige cockpit-bridge >= 318, que só existe no -backports da 24.04;
  # cockpit-pcp fica de fora porque conflita com esse cockpit-bridge).
  if apt-cache policy 2>/dev/null | grep -q "${CODENAME}-backports"; then target=(-t "${CODENAME}-backports"); fi
  # shellcheck disable=SC2086
  apt_install ${target[@]+"${target[@]}"} $pkgs || return 1
  systemctl enable --now cockpit.socket || return 1
  systemctl is-active --quiet cockpit.socket || return 1
  # cockpit-networkmanager instala o NetworkManager: a rede (netplan/networkd) precisa continuar de pé.
  ip route show default | grep -q . || { log "ATENÇÃO: sem rota padrão após instalar o Cockpit"; return 1; }
  log "Cockpit: https://<ip>:9090 (login com o usuário e a senha do servidor)"
}

coolify_install() {
  [ -f "$STATE/docker_install.done" ] || { log "Coolify aguarda o Docker"; return 1; }
  if curl -fsS -o /dev/null --max-time 10 http://127.0.0.1:8000/api/health 2>/dev/null; then
    log "Coolify já responde em :8000"
  else
    local f=/var/lib/uab/coolify-install.sh
    curl -fsSL --retry 3 --max-time 180 https://cdn.coollabs.io/coolify/install.sh -o "$f" || return 1
    log "instalador oficial do Coolify baixado (sha256 $(sha256sum "$f" | cut -c1-16)…)"
    (
      # variáveis exportadas (não aparecem na linha de comando/ps)
      export DOCKER_ADDRESS_POOL_BASE="$COOLIFY_POOL" DOCKER_ADDRESS_POOL_SIZE=24
      if [ -s /etc/uab/coolify-password ]; then
        ROOT_USER_PASSWORD="$(head -n1 /etc/uab/coolify-password)"
        export ROOT_USERNAME=admin ROOT_USER_EMAIL="${UAB_COOLIFY_EMAIL:-admin@localhost}" ROOT_USER_PASSWORD
      fi
      timeout 2700 bash "$f" </dev/null
    ) || { log "instalador do Coolify falhou (log: /data/coolify/source/installation-*.log)"; return 1; }
    local i
    for i in $(seq 1 60); do
      curl -fsS -o /dev/null --max-time 10 http://127.0.0.1:8000/api/health 2>/dev/null && break
      sleep 10
    done
    curl -fsS -o /dev/null --max-time 10 http://127.0.0.1:8000/api/health || { log "Coolify não respondeu em :8000"; return 1; }
  fi
  # Admin predefinido: com ele criado, o Coolify fecha o cadastro público
  # (/register passa a redirecionar para /login). Sem isso, o 1º visitante vira admin.
  if [ -s /etc/uab/coolify-password ]; then
    local reg=""
    for i in $(seq 1 24); do
      reg="$(curl -s -o /dev/null -w '%{redirect_url}' --max-time 10 http://127.0.0.1:8000/register 2>/dev/null)"
      case "$reg" in */login*) break ;; esac
      sleep 10
    done
    case "$reg" in
      */login*) status_set coolify_admin "ok (${UAB_COOLIFY_EMAIL}); cadastro público fechado"; log "Coolify: admin criado e cadastro público fechado" ;;
      *) status_set coolify_admin "ATENÇÃO: admin não criado; abra http://<ip>:8000 e crie o admin JÁ"
         log "ATENÇÃO: o Coolify não criou o admin predefinido (e-mail precisa ter domínio real; senha não pode estar em vazamentos conhecidos)" ;;
    esac
  fi
  if [ -f /etc/uab/coolify-password ]; then shred -u -z /etc/uab/coolify-password 2>/dev/null || rm -f /etc/uab/coolify-password; fi
  # o instalador do Coolify mexe no SSH/Docker: confere que o acesso continua intacto
  install -d -m 0755 /run/sshd
  sshd -t || { log "sshd -t falhou após o Coolify"; return 1; }
  sshd -T -C "user=$USER_NAME,host=lan,addr=192.168.1.10" 2>/dev/null | grep -qi '^pubkeyauthentication yes' || return 1
  if [ "$SSH_PORT" != 22 ]; then
    # o Coolify gerencia este servidor por SSH (servidor "localhost", id 0): usa a porta escolhida
    if docker exec coolify-db psql -U coolify -d coolify -tAc "UPDATE servers SET port=$SSH_PORT WHERE id=0 RETURNING port" 2>/dev/null | grep -qx "$SSH_PORT"; then
      log "Coolify: servidor local configurado na porta SSH $SSH_PORT"
    else
      log "AVISO: não foi possível ajustar a porta SSH no Coolify; em Servers > localhost, use a porta $SSH_PORT"
    fi
  fi
  # grava no disco os arquivos que o Coolify acabou de criar: sem isso, uma queda de
  # energia logo após a instalação deixa os caches dele vazios e ele não sobe mais
  sync
  log "Coolify: http://<ip>:8000 (admin: ${UAB_COOLIFY_EMAIL:-definido no primeiro acesso})"
}

update_issue() {
  # Mensagem no console local (útil para quem estiver na frente da máquina).
  local ip
  ip="$(tailscale ip -4 2>/dev/null | head -n1 || true)"
  mkdir -p /etc/issue.d
  {
    echo "ubuntu-autoinstall-builder: $UAB_HOSTNAME"
    echo "IP local: \\4"
    [ -n "$ip" ] && echo "Tailscale: $ip"
    if [ -n "$FAILED" ]; then echo "Provisionamento pendente:$FAILED (veja /var/log/uab-provision.log)"; else echo "Provisionamento: concluido"; fi
    echo
  } >/etc/issue.d/uab.issue
}
# ------------------------------------------------------------------ execução
if grep -q '^provisioned=ok' "$STATE/status" 2>/dev/null; then
  update_issue
  [ -x /usr/local/sbin/uab-report ] && timeout 60 /usr/local/sbin/uab-report || true
  exit 0
fi
log "=== início (build $UAB_BUILD_ID, $PRETTY_NAME) ==="
install_motd
status_set plan "$(plan_steps)"
apt_repair
step ssh_keys ssh_keys
step ssh_harden ssh_harden
step ssh_port ssh_port
if network_ready; then
  step base_packages base_packages
  if [ -n "$UAB_EXTRA_PACKAGES" ]; then step extra_packages extra_packages; fi
  if [ "$UAB_TAILSCALE" = yes ]; then
    step tailscale_install tailscale_install
    [ -f "$STATE/tailscale_install.done" ] && step tailscale_login tailscale_login
  fi
  if [ "${UAB_WG:-no}" = yes ]; then step wireguard_install wireguard_install; fi
  if [ "$UAB_DOCKER" = yes ]; then step docker_install docker_install; fi
  if [ "$UAB_COCKPIT" = yes ]; then step cockpit_install cockpit_install; fi
  if [ "$UAB_COOLIFY" = yes ]; then step coolify_install coolify_install; fi
  if [ "$UAB_VIRT" = yes ]; then
    step virt_install virt_install
    [ "$UAB_VIRT_BRIDGE" = yes ] && [ -f "$STATE/virt_install.done" ] && step virt_bridge virt_bridge
  fi
  if [ "$UAB_TWEAKS" = yes ]; then step server_tweaks server_tweaks; fi
  if [ "$UAB_CLAUDE_CODE" = yes ]; then step claude_code claude_code; fi
  if [ "$UAB_GPU" = yes ]; then
    step gpu_driver gpu_driver
    [ -f "$STATE/gpu_driver.done" ] && step gpu_toolkit gpu_toolkit
  fi
  if [ "$UAB_DDNS" = yes ]; then step ddns_setup ddns_setup; fi
  if [ "$UAB_REMOTE_AGENT" = yes ] && [ -n "$UAB_REMOTE_URL" ]; then step remote_agent remote_agent; fi
  if [ "$UAB_TSEC_AGENT" = yes ] && [ -n "$UAB_TSEC_URL" ]; then step terminalsec_agent terminalsec_agent; fi
  if [ "$UAB_CLOUDFLARE" = yes ]; then step cloudflare_setup cloudflare_setup; fi
  if [ "$UAB_GPU" = yes ] && [ -f "$STATE/gpu_driver.done" ]; then step gpu_check gpu_check; fi
else
  FAILED="$FAILED network"
fi
status_set current ""
update_issue
sync  # tudo o que esta rodada instalou vai para o disco (resiste a queda de energia)
# avisa o painel (se a ISO foi gerada por ele) DEPOIS de gravar o resultado final
# desta rodada; nunca bloqueia o provisionamento
report_now() { [ -x /usr/local/sbin/uab-report ] && timeout 60 /usr/local/sbin/uab-report || true; }

# Driver NVIDIA recém-instalado só carrega reiniciando: reinicia UMA vez, sozinho,
# quando só isso falta (o provisionador continua no próximo boot).
if [ "$(echo $FAILED)" = gpu_check ] && [ ! -f "$STATE/gpu-reboot.done" ]; then
  touch "$STATE/gpu-reboot.done"; status_set provisioned "reiniciando para carregar o driver da GPU"
  report_now
  log "=== reiniciando para carregar o driver NVIDIA ==="
  systemctl --no-block reboot; exit 0
fi
if [ -n "$FAILED" ]; then
  status_set provisioned "pendente:$FAILED"
  report_now
  log "=== etapas pendentes:$FAILED — nova tentativa em 60 s ==="
  exit 1
fi
status_set provisioned "ok $(date '+%F %T')"
log "=== provisionamento concluído ==="
report_now
exit 0
