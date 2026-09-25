#!/usr/bin/env bash
# Assistente interativo: coleta todas as informações e grava
#   config/install.env        (configuração, sem segredos, chmod 600)
#   config/authorized_keys    (chaves PÚBLICAS SSH)
#   secrets/password.hash     (hash SHA-512 da senha, chmod 600)
#   secrets/tailscale-authkey (auth key do Tailscale, chmod 600)
source "$(dirname "$0")/common.sh"

[ -t 0 ] || die "Este assistente precisa de um terminal interativo."

# Valores anteriores servem de default ao reexecutar.
if [ -f "$CONFIG_FILE" ]; then
  info "Configuração existente encontrada; os valores atuais serão sugeridos como padrão."
  load_config
fi

# ============================================================ UBUNTU
header "Versão do Ubuntu Server LTS"
case "${UBUNTU_SERIES:-24.04}" in 26.04) d=2 ;; *) d=1 ;; esac
ask_choice n "Qual versão LTS usar?" "$d" \
  "Ubuntu 24.04 LTS (Noble) — mais madura, recomendada" \
  "Ubuntu 26.04 LTS (Resolute)"
if [ "$n" = 1 ]; then UBUNTU_SERIES=24.04; else UBUNTU_SERIES=26.04; fi
info "A última point release da série $UBUNTU_SERIES será baixada de releases.ubuntu.com e verificada (GPG + SHA256)."

# ============================================================ SISTEMA
header "Sistema"
while true; do
  ask HOSTNAME_ "Hostname do servidor (minúsculas, números e hífen)" "${HOSTNAME_:-}"
  is_valid_hostname "$HOSTNAME_" && break
  warn "Hostname inválido. Use apenas a-z, 0-9 e '-', até 63 caracteres, sem começar/terminar com '-'."
done
while true; do
  ask ADMIN_USER "Nome do usuário administrativo" "${ADMIN_USER:-}"
  is_valid_username "$ADMIN_USER" && break
  warn "Usuário inválido ou reservado. Use minúsculas, números, '_' ou '-' (ex.: operador)."
done
ask ADMIN_REALNAME "Nome completo (opcional)" "${ADMIN_REALNAME:-$ADMIN_USER}"
ADMIN_REALNAME="$(printf '%s' "$ADMIN_REALNAME" | tr -d "\"'\\\\:")"

while true; do
  ask TIMEZONE "Timezone" "${TIMEZONE:-America/Sao_Paulo}"
  if printf '%s\n' "$TIMEZONE" | grep -Eq '^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$'; then
    if [ -d /usr/share/zoneinfo ] && [ ! -f "/usr/share/zoneinfo/$TIMEZONE" ]; then
      warn "Timezone '$TIMEZONE' não existe em /usr/share/zoneinfo."; continue
    fi
    break
  fi
  warn "Timezone inválida (ex.: America/Sao_Paulo, Etc/UTC)."
done
while true; do
  ask LOCALE "Locale" "${LOCALE:-pt_BR.UTF-8}"
  printf '%s\n' "$LOCALE" | grep -Eq '^[a-z]{2,3}(_[A-Z]{2})?\.UTF-8$|^C\.UTF-8$' && break
  warn "Locale inválido (ex.: pt_BR.UTF-8, en_US.UTF-8)."
done
while true; do
  ask KEYBOARD_LAYOUT "Layout do teclado" "${KEYBOARD_LAYOUT:-br}"
  printf '%s\n' "$KEYBOARD_LAYOUT" | grep -Eq '^[a-z]{2,8}$' && break
  warn "Layout inválido (ex.: br, us, pt)."
done
ask KEYBOARD_VARIANT "Variante do teclado (vazio = padrão; ex.: abnt2, intl)" "${KEYBOARD_VARIANT:-}"
printf '%s\n' "$KEYBOARD_VARIANT" | grep -Eq '^[a-z0-9_-]*$' || die "Variante inválida."

header "Acesso SSH"
case "${SSH_AUTH:-key}" in password) d=2 ;; *) d=1 ;; esac
ask_choice n "Como você vai entrar no servidor por SSH?" "$d" \
  "Chave SSH (mais seguro; recomendado)" \
  "Usuário e senha (mais simples)"
PREV_SSH_AUTH="${SSH_AUTH:-key}"
if [ "$n" = 1 ]; then SSH_AUTH=key; else SSH_AUTH=password; fi
if [ "$SSH_AUTH" = password ]; then
  {
    echo "Modo usuário e senha:"
    echo "  - você entra com: ssh <usuario>@<servidor> e digita a senha;"
    echo "  - comandos de administrador (root): sudo <comando> (mesma senha);"
    echo "  - por segurança a senha só é aceita da REDE LOCAL e do TAILSCALE;"
    echo "    de IPs públicos da internet, só chave SSH;"
    echo "  - login direto como \"root\" continua bloqueado;"
    echo "  - fail2ban bloqueia quem errar a senha várias vezes;"
    echo "  - a senha precisa ter no mínimo 12 caracteres."
  } >&2
fi

while true; do
  ask SSH_PORT "Porta do SSH (22 = padrão; ou 1024-65535, ex.: 2222)" "${SSH_PORT:-22}"
  if valid_ssh_port "$SSH_PORT"; then break; fi
  warn "Porta inválida: use 22 ou 1024-65535 (exceto 6001, 6002, 8000, 8080 e 9090, usadas pelos programas)."
done
[ "$SSH_PORT" = 22 ] || info "SSH na porta $SSH_PORT: conecte com  ssh -p $SSH_PORT <usuario>@<servidor>"

header "Senha administrativa"
MIN_PW=10; [ "$SSH_AUTH" = password ] && MIN_PW=12
if [ -s "$PASSWORD_HASH_FILE" ] && [ "$PREV_SSH_AUTH" = "$SSH_AUTH" ] && ! ask_yn "Já existe um hash de senha salvo. Definir nova senha?" N; then
  info "Mantendo hash existente."
else
  UAB_MIN_PW="$MIN_PW" "$UAB_ROOT/scripts/generate-password.sh"
fi

# ============================================================ REDE
header "Rede"
echo "Interfaces Wi-Fi não são configuradas: use cabo de rede (Ethernet)."
case "${NET_MODE:-dhcp}" in static) d=2 ;; *) d=1 ;; esac
ask_choice n "Configuração inicial:" "$d" "DHCP (recomendado)" "IP estático"
if [ "$n" = 1 ]; then
  NET_MODE=dhcp
  NET_MATCH_TYPE=glob; NET_MATCH_VALUE='en*'
  NET_ADDRESS=""; NET_GATEWAY=""; NET_DNS=""; NET_SEARCH=""
  info "DHCP em todas as interfaces Ethernet (en*/eth*), sem depender do nome exato da placa."
else
  NET_MODE=static
  PREV_NET_TYPE="${NET_MATCH_TYPE:-}"; PREV_NET_VALUE="${NET_MATCH_VALUE:-}"
  case "${NET_MATCH_TYPE:-mac}" in name) d=2 ;; glob) d=3 ;; *) d=1 ;; esac
  ask_choice n "Como identificar a interface de rede do servidor?" "$d" \
    "Endereço MAC (mais robusto; recomendado)" \
    "Nome exato da interface (ex.: enp3s0 — só se você tiver certeza)" \
    "Primeira placa Ethernet (en*) — SOMENTE se a máquina tiver uma única placa cabeada"
  case "$n" in
    1) NET_MATCH_TYPE=mac
       while true; do
         ask NET_MATCH_VALUE "MAC (ex.: 00:1a:2b:3c:4d:5e)" "$( [ "$PREV_NET_TYPE" = mac ] && printf '%s' "$PREV_NET_VALUE" )"
         NET_MATCH_VALUE="$(printf '%s' "$NET_MATCH_VALUE" | tr 'A-F' 'a-f' | tr '-' ':')"
         printf '%s\n' "$NET_MATCH_VALUE" | grep -Eq '^([0-9a-f]{2}:){5}[0-9a-f]{2}$' && break
         warn "MAC inválido."
       done ;;
    2) NET_MATCH_TYPE=name
       while true; do
         ask NET_MATCH_VALUE "Nome da interface" "$( [ "$PREV_NET_TYPE" = name ] && printf '%s' "$PREV_NET_VALUE" )"
         printf '%s\n' "$NET_MATCH_VALUE" | grep -Eq '^[a-zA-Z0-9._-]{1,15}$' && break
         warn "Nome inválido."
       done ;;
    3) NET_MATCH_TYPE=glob; NET_MATCH_VALUE='en*'
       warn "Se houver mais de uma placa en*, todas receberiam o mesmo IP. Use MAC nesse caso." ;;
  esac
  while true; do
    ask NET_ADDRESS "Endereço IP/CIDR (ex.: 192.168.1.50/24)" "${NET_ADDRESS:-}"
    python3 -c 'import ipaddress,sys; i=ipaddress.ip_interface(sys.argv[1]); assert "/" in sys.argv[1] and i.version==4 and i.ip!=i.network.network_address' "$NET_ADDRESS" 2>/dev/null && break
    warn "Endereço inválido. Informe IPv4 com prefixo, ex.: 192.168.1.50/24"
  done
  while true; do
    ask NET_GATEWAY "Gateway" "${NET_GATEWAY:-}"
    python3 -c 'import ipaddress,sys; n=ipaddress.ip_interface(sys.argv[1]).network; g=ipaddress.ip_address(sys.argv[2]); assert g in n' "$NET_ADDRESS" "$NET_GATEWAY" 2>/dev/null && break
    warn "Gateway inválido ou fora da sub-rede de $NET_ADDRESS."
  done
  while true; do
    ask NET_DNS "Servidores DNS (separados por vírgula)" "${NET_DNS:-1.1.1.1,8.8.8.8}"
    NET_DNS="$(printf '%s' "$NET_DNS" | tr -d ' ')"
    python3 -c 'import ipaddress,sys; [ipaddress.ip_address(x) for x in sys.argv[1].split(",")]' "$NET_DNS" 2>/dev/null && break
    warn "Lista de DNS inválida."
  done
  ask NET_SEARCH "Domínio de busca (opcional)" "${NET_SEARCH:-}"
  printf '%s\n' "$NET_SEARCH" | grep -Eq '^([a-z0-9-]+(\.[a-z0-9-]+)*)?$' || die "Domínio de busca inválido."
fi

# ============================================================ STORAGE
header "Disco de instalação (ÁREA CRÍTICA)"
cat >&2 <<'EOF'
O disco de destino é o computador REMOTO, não esta máquina. Para descobrir os
discos dele, dê boot em qualquer Linux live nele (ou no próprio instalador
Ubuntu, Ctrl+Alt+F2) e rode:
    lsblk -d -o NAME,SIZE,TYPE,TRAN,MODEL,SERIAL
Observação: com o pendrive conectado, /dev/sda pode ser o PENDRIVE.
Identificar pelo número de série é o método mais estável.
Antes de apagar qualquer coisa, a instalação verifica que o critério escolhido
corresponde a EXATAMENTE UM disco e que ele não é o pendrive; caso contrário,
aborta sem tocar em nenhum disco.
EOF
case "${STORAGE_MATCH_TYPE:-}" in serial) d=1 ;; path) d=2 ;; model) d=3 ;; single) d=4 ;; *) d=1 ;; esac
ask_choice n "Como identificar o disco que receberá o Ubuntu?" "$d" \
  "Número de série (mais estável; recomendado)" \
  "Caminho do dispositivo (ex.: /dev/nvme0n1, /dev/sda)" \
  "Modelo do disco (ex.: Samsung SSD 980*)" \
  "Único disco interno (aborta se a máquina tiver mais de um disco interno)"
PREV_TYPE="${STORAGE_MATCH_TYPE:-}"; PREV_VALUE="${STORAGE_MATCH_VALUE:-}"
case "$n" in
  1) STORAGE_MATCH_TYPE=serial
     while true; do
       ask STORAGE_MATCH_VALUE "Número de série (coluna SERIAL do lsblk; aceita * como curinga)" "$( [ "$PREV_TYPE" = serial ] && printf '%s' "$PREV_VALUE" )"
       printf '%s\n' "$STORAGE_MATCH_VALUE" | grep -Eq '^[A-Za-z0-9_.*:-]{3,}$' && break
       warn "Serial inválido (use apenas letras, números, _ . - : e *)."
     done ;;
  2) STORAGE_MATCH_TYPE=path
     while true; do
       ask STORAGE_MATCH_VALUE "Dispositivo (ex.: /dev/nvme0n1, /dev/sda, /dev/vda)" "$( [ "$PREV_TYPE" = path ] && printf '%s' "$PREV_VALUE" )"
       printf '%s\n' "$STORAGE_MATCH_VALUE" | grep -Eq '^/dev/(sd[a-z]+|nvme[0-9]+n[0-9]+|vd[a-z]+|hd[a-z]+|mmcblk[0-9]+|xvd[a-z]+|disk/by-id/[A-Za-z0-9_.:-]+)$' && break
       warn "Informe o DISCO inteiro (não uma partição): /dev/sda, /dev/nvme0n1, /dev/mmcblk0..."
     done
     warn "Nomes como /dev/sda podem mudar entre boots (ex.: com o pendrive plugado)." ;;
  3) STORAGE_MATCH_TYPE=model
     while true; do
       ask STORAGE_MATCH_VALUE "Modelo (coluna MODEL do lsblk; espaços viram '_'; aceita *)" "$( [ "$PREV_TYPE" = model ] && printf '%s' "$PREV_VALUE" )"
       STORAGE_MATCH_VALUE="$(printf '%s' "$STORAGE_MATCH_VALUE" | tr ' ' '_')"
       printf '%s\n' "$STORAGE_MATCH_VALUE" | grep -Eq '^[A-Za-z0-9_.*-]{3,}$' && break
       warn "Modelo inválido."
     done ;;
  4) STORAGE_MATCH_TYPE=single; STORAGE_MATCH_VALUE=single
     warn "Será usado o único disco interno não-USB. Se houver mais de um, a instalação aborta sem apagar nada." ;;
esac
while true; do
  ask STORAGE_MIN_GB "Tamanho mínimo esperado do disco em GB (proteção contra disco errado)" "${STORAGE_MIN_GB:-20}"
  case "$STORAGE_MIN_GB" in ''|*[!0-9]*) warn "Número inválido." ;; *) [ "$STORAGE_MIN_GB" -ge 8 ] && break; warn "Mínimo 8 GB." ;; esac
done
case "${STORAGE_LAYOUT:-lvm}" in direct) d=2 ;; *) d=1 ;; esac
ask_choice n "Layout de particionamento:" "$d" \
  "LVM usando o disco inteiro (padrão Ubuntu; recomendado)" \
  "Partição ext4 direta (sem LVM)"
if [ "$n" = 1 ]; then STORAGE_LAYOUT=lvm; else STORAGE_LAYOUT=direct; fi
STORAGE_ALLOW_USB="${STORAGE_ALLOW_USB:-no}"
if [ "$STORAGE_MATCH_TYPE" != single ]; then
  if ask_yn "O disco de destino é EXTERNO/USB? (normalmente não; discos USB são recusados por segurança)" "$( [ "$STORAGE_ALLOW_USB" = yes ] && echo S || echo N )"; then
    STORAGE_ALLOW_USB=yes
  else
    STORAGE_ALLOW_USB=no
  fi
fi
info "Criptografia de disco (LUKS) não é oferecida: exigiria senha no console a cada boot e quebraria o acesso remoto."

if [ "$STORAGE_MATCH_TYPE" = single ]; then DISK_DESC="único disco interno (não-USB) do servidor"; else DISK_DESC="$STORAGE_MATCH_TYPE = $STORAGE_MATCH_VALUE"; fi
printf '\n%s' "$C_RED$C_BLD" >&2
cat >&2 <<EOF
ATENÇÃO

O disco selecionado será completamente apagado durante a instalação.

Disco:
$DISK_DESC  (mínimo ${STORAGE_MIN_GB} GB, layout $STORAGE_LAYOUT)

Digite exatamente:

APAGAR

para confirmar.
EOF
printf '%s' "$C_RST" >&2
ask CONFIRM "Confirmação" ""
if [ "$CONFIRM" != "APAGAR" ]; then
  STORAGE_CONFIRMED=""
  warn "Confirmação não recebida. A configuração será salva, mas NENHUMA ISO de instalação será gerada até você confirmar (rode ./setup.sh novamente)."
else
  STORAGE_CONFIRMED="$(storage_confirm_token)"
  ok "Confirmado."
fi

# ============================================================ SSH
if [ "$SSH_AUTH" = password ]; then header "Chave SSH pública (opcional)"; else header "Chave SSH pública"; fi
TMPKEYS="$(mktemp)"; trap 'rm -f "$TMPKEYS"' EXIT
if [ -s "$AUTHKEYS_FILE" ]; then
  info "Chaves já cadastradas:"
  while IFS= read -r l; do [ -n "$l" ] && printf '%s' "$l" >"$TMPKEYS.1" && ssh-keygen -l -f "$TMPKEYS.1" >&2 || true; done <"$AUTHKEYS_FILE"
  rm -f "$TMPKEYS.1"
  if ask_yn "Manter essas chaves?" S; then cp "$AUTHKEYS_FILE" "$TMPKEYS"; fi
fi
add_key() {
  local m p line tmp1
  ask_choice m "Como obter sua chave pública?" 1 \
    "~/.ssh/id_ed25519.pub" "Informar caminho de um arquivo .pub" "Colar a chave pública"
  tmp1="$(mktemp)"
  case "$m" in
    1) p="$HOME/.ssh/id_ed25519.pub"
       if [ ! -f "$p" ]; then
         warn "$p não existe. Crie com: ssh-keygen -t ed25519 -C \"seu-email\"  (e rode o setup de novo)"
         rm -f "$tmp1"; return 1
       fi
       cp "$p" "$tmp1" ;;
    2) ask p "Caminho do arquivo .pub" ""
       p="${p/#\~/$HOME}"
       case "$p" in *.pub) ;; *) warn "O arquivo não termina em .pub. Nunca informe a chave privada."; ;; esac
       [ -f "$p" ] || { warn "Arquivo não encontrado."; rm -f "$tmp1"; return 1; }
       cp "$p" "$tmp1" ;;
    3) ask line "Cole a chave pública (uma linha começando com ssh-ed25519/ssh-rsa/ecdsa-...)" ""
       printf '%s\n' "$line" >"$tmp1" ;;
  esac
  if validate_pubkey_file "$tmp1"; then
    ssh-keygen -l -f "$tmp1" >&2 2>/dev/null || true
    grep -Ev '^[[:space:]]*(#|$)' "$tmp1" >>"$TMPKEYS"
    ok "Chave adicionada."
  fi
  rm -f "$tmp1"
}
if [ "$SSH_AUTH" = password ] && [ ! -s "$TMPKEYS" ]; then
  if ask_yn "Adicionar também uma chave SSH (opcional)?" N; then add_key || true; fi
fi
while true; do
  if [ ! -s "$TMPKEYS" ]; then
    [ "$SSH_AUTH" = password ] && break
    add_key || true
    continue
  fi
  ask_yn "Adicionar outra chave pública (ex.: outro computador)?" N || break
  add_key || true
done
awk '!seen[$0]++' "$TMPKEYS" >"$AUTHKEYS_FILE.new" && mv "$AUTHKEYS_FILE.new" "$AUTHKEYS_FILE"
chmod 644 "$AUTHKEYS_FILE"
if [ -s "$AUTHKEYS_FILE" ]; then
  validate_pubkey_file "$AUTHKEYS_FILE" || die "Chaves inválidas."
  ok "$(grep -c . "$AUTHKEYS_FILE") chave(s) pública(s) em config/authorized_keys."
else
  info "Sem chave SSH: acesso por usuário e senha."
fi

# ============================================================ TAILSCALE
header "Tailscale"
if ask_yn "Instalar Tailscale?" "$( [ "${TAILSCALE_ENABLED:-yes}" = no ] && echo N || echo S )"; then
  TAILSCALE_ENABLED=yes
  ask TAILSCALE_HOSTNAME "Nome do servidor na Tailnet" "${TAILSCALE_HOSTNAME:-$HOSTNAME_}"
  is_valid_hostname "$TAILSCALE_HOSTNAME" || die "Nome inválido para Tailscale."
  ask TAILSCALE_EXTRA_ARGS "Argumentos extras para 'tailscale up' (opcional, ex.: --advertise-tags=tag:server)" "${TAILSCALE_EXTRA_ARGS:-}"
  printf '%s\n' "$TAILSCALE_EXTRA_ARGS" | grep -Eq '^([[:space:]]*--[a-z0-9-]+(=[A-Za-z0-9:,./_@-]+)?)*[[:space:]]*$' \
    || die "Argumentos extras inválidos (apenas --flag ou --flag=valor)."
  case " $TAILSCALE_EXTRA_ARGS " in *" --auth-key"*|*" --authkey"*|*" --hostname"*) die "Não informe --auth-key/--hostname aqui." ;; esac
  if [ -s "$TS_AUTHKEY_FILE" ] && ! ask_yn "Já existe uma auth key salva ($(mask_secret "$(cat "$TS_AUTHKEY_FILE")")). Substituir?" N; then
    info "Mantendo auth key existente."
  else
    echo "Crie a chave em https://login.tailscale.com/admin/settings/keys (veja o README):"
    echo "  - de preferência NÃO reutilizável, com tag (ex.: tag:server) e pré-aprovada;"
    echo "  - NÃO marque 'Ephemeral' (o servidor sumiria da Tailnet ao ficar offline)."
    while true; do
      ask_secret TSKEY "Cole a auth key (entrada oculta)"
      TSKEY="$(printf '%s' "$TSKEY" | tr -d '[:space:]')"
      if printf '%s\n' "$TSKEY" | grep -Eq '^tskey-(auth-)?[A-Za-z0-9_-]{10,}$'; then break; fi
      warn "Formato inesperado (deve começar com 'tskey-auth-')."; TSKEY=""
    done
    printf '%s\n' "$TSKEY" | write_private_file "$TS_AUTHKEY_FILE"
    ok "Auth key $(mask_secret "$TSKEY") salva em secrets/tailscale-authkey (600)."
    TSKEY=""; unset TSKEY
  fi
else
  TAILSCALE_ENABLED=no; TAILSCALE_HOSTNAME=""; TAILSCALE_EXTRA_ARGS=""
  warn "Sem Tailscale, o acesso remoto dependerá de SSH direto na rede local/roteador."
  if [ -f "$TS_AUTHKEY_FILE" ] && ask_yn "Apagar a auth key salva anteriormente?" S; then secure_delete "$TS_AUTHKEY_FILE"; fi
fi

# ============================================================ DOCKER / PACOTES
header "Programas"
if [ "${COCKPIT_ENABLED:-no}" = yes ]; then d=4; elif [ "${COOLIFY_ENABLED:-no}" = yes ]; then d=3
elif [ "${DOCKER_ENABLED:-yes}" = yes ]; then d=2; else d=1; fi
ask_choice n "O que instalar no servidor?" "$d" \
  "Nenhum programa extra" \
  "Docker + Docker Compose" \
  "Coolify (inclui Docker) — painel para publicar apps" \
  "Coolify + Cockpit completo (console web de administração com todos os módulos)"
DOCKER_ENABLED=no; COOLIFY_ENABLED=no; COCKPIT_ENABLED=no
case "$n" in 2) DOCKER_ENABLED=yes ;; 3) DOCKER_ENABLED=yes; COOLIFY_ENABLED=yes ;; 4) DOCKER_ENABLED=yes; COOLIFY_ENABLED=yes; COCKPIT_ENABLED=yes ;; esac
[ "$DOCKER_ENABLED" = yes ] && info "Docker Engine + Compose do repositório oficial download.docker.com (chave GPG com fingerprint verificado)."
[ "$COCKPIT_ENABLED" = yes ] && info "Cockpit completo (-backports): https://<ip>:9090, login com o usuário e a senha do servidor."
if [ "$COOLIFY_ENABLED" = yes ]; then
  info "Coolify: http://<ip>:8000. O admin do Coolify já é criado na instalação (senão, o primeiro a abrir a página vira admin)."
  while true; do
    ask COOLIFY_ADMIN_EMAIL "E-mail do admin do Coolify" "${COOLIFY_ADMIN_EMAIL:-}"
    printf '%s\n' "$COOLIFY_ADMIN_EMAIL" | grep -Eq '^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$' && break
    warn "E-mail inválido."
  done
  if [ -s "$COOLIFY_PW_FILE" ] && ask_yn "Manter a senha do Coolify já salva?" S; then :; else
    while true; do
      ask_secret CPW "Senha do admin do Coolify (12+, maiúscula, minúscula, número e símbolo)"
      if [ "${#CPW}" -ge 12 ] && printf '%s' "$CPW" | grep -q '[a-z]' && printf '%s' "$CPW" | grep -q '[A-Z]' \
         && printf '%s' "$CPW" | grep -q '[0-9]' && printf '%s' "$CPW" | grep -q '[^A-Za-z0-9]' \
         && ! printf '%s' "$CPW" | grep -q "'"; then
        # Mesma checagem do Coolify (Have I Been Pwned; só 5 caracteres do SHA-1 saem daqui)
        h="$(printf '%s' "$CPW" | sha1sum | cut -c1-40 | tr a-f A-F)"
        if curl -fsS --max-time 8 -H 'Add-Padding: true' "https://api.pwnedpasswords.com/range/${h:0:5}" 2>/dev/null \
             | tr -d '\r' | grep -q "^${h:5}:[1-9]"; then
          warn "Essa senha aparece em vazamentos conhecidos; o Coolify recusaria criar o admin. Escolha outra."
          CPW=""; h=""; continue
        fi
        h=""
        ask_secret CPW2 "Confirme a senha do Coolify"
        [ "$CPW" = "$CPW2" ] && break
        warn "As senhas não conferem."
      else
        warn "Senha fraca (ou com aspas simples)."
      fi
      CPW=""; CPW2=""
    done
    printf '%s\n' "$CPW" | write_private_file "$COOLIFY_PW_FILE"
    CPW=""; CPW2=""; unset CPW CPW2
    ok "Senha do Coolify salva em secrets/coolify-password (600); vai na ISO e é apagada do servidor após o uso."
  fi
else
  COOLIFY_ADMIN_EMAIL=""
  [ -f "$COOLIFY_PW_FILE" ] && secure_delete "$COOLIFY_PW_FILE"
fi
echo "Pacotes padrão: curl wget git ca-certificates openssh-server htop unzip rsync"
while true; do
  ask EXTRA_PACKAGES "Pacotes adicionais (separados por espaço; vazio = nenhum)" "${EXTRA_PACKAGES:-}"
  printf '%s\n' "$EXTRA_PACKAGES" | grep -Eq '^([[:space:]]*[a-z0-9][a-z0-9+.-]+)*[[:space:]]*$' && break
  warn "Nome de pacote inválido."
done
EXTRA_PACKAGES="$(printf '%s' "$EXTRA_PACKAGES" | tr -s ' ' | sed 's/^ //; s/ $//')"

# ============================================================ EXTRAS
header "Extras (opcionais)"
yn_default() { [ "${1:-no}" = yes ] && echo S || echo N; }
if ask_yn "DDNS No-IP (mantém um nome como meuservidor.ddns.net apontando para o IP público)?" "$(yn_default "${DDNS_ENABLED:-no}")"; then
  DDNS_ENABLED=yes
  while true; do
    ask DDNS_HOSTNAME "Hostname no No-IP (ex.: meuservidor.ddns.net)" "${DDNS_HOSTNAME:-}"
    DDNS_HOSTNAME="$(printf '%s' "$DDNS_HOSTNAME" | tr 'A-Z' 'a-z')"
    printf '%s\n' "$DDNS_HOSTNAME" | grep -Eq '^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$' && break
    warn "Hostname inválido."
  done
  while true; do
    ask DDNS_LOGIN "Usuário (ou e-mail) do No-IP" "${DDNS_LOGIN:-}"
    printf '%s\n' "$DDNS_LOGIN" | grep -Eq "^[^[:space:]'\\\\]{1,128}$" && break
    warn "Usuário inválido."
  done
  if [ -s "$DDNS_PW_FILE" ] && ask_yn "Manter a senha do No-IP já salva?" S; then :; else
    while true; do
      ask_secret DPW "Senha do No-IP"
      if [ -n "$DPW" ] && ! printf '%s' "$DPW" | grep -q "['\\\\]"; then break; fi
      warn "Senha vazia ou com aspas simples/barra invertida."
      DPW=""
    done
    printf '%s\n' "$DPW" | write_private_file "$DDNS_PW_FILE"; DPW=""; unset DPW
  fi
else
  DDNS_ENABLED=no; DDNS_HOSTNAME=""; DDNS_LOGIN=""
  [ -f "$DDNS_PW_FILE" ] && secure_delete "$DDNS_PW_FILE"
fi

if ask_yn "Cloudflare Tunnel (publica serviços sem abrir portas no roteador)?" "$(yn_default "${CLOUDFLARE_ENABLED:-no}")"; then
  CLOUDFLARE_ENABLED=yes
  info "Crie o túnel em Cloudflare Zero Trust > Networks > Tunnels e copie o token (o texto longo depois de --token)."
  if [ -s "$CF_TOKEN_FILE" ] && ask_yn "Manter o token já salvo?" S; then :; else
    while true; do
      ask_secret CFT "Token do túnel"
      # aceita o token puro ou o comando inteiro copiado da Cloudflare
      CFT="$(printf '%s' "$CFT" | grep -Eo '[A-Za-z0-9_=+/-]{80,4096}' | tail -n1)"
      printf '%s' "$CFT" | grep -Eq '^[A-Za-z0-9_=+/-]{80,4096}$' && break
      warn "Token inválido (cole o token inteiro)."; CFT=""
    done
    printf '%s\n' "$CFT" | write_private_file "$CF_TOKEN_FILE"; CFT=""; unset CFT
  fi
else
  CLOUDFLARE_ENABLED=no
  [ -f "$CF_TOKEN_FILE" ] && secure_delete "$CF_TOKEN_FILE"
fi

if ask_yn "GPU NVIDIA (driver recomendado; GPU nos containers se houver Docker/Coolify)?" "$(yn_default "${GPU_NVIDIA:-no}")"; then GPU_NVIDIA=yes; else GPU_NVIDIA=no; fi

if ask_yn "Virtualização KVM (libvirt/QEMU para rodar VMs)?" "$(yn_default "${VIRT_ENABLED:-no}")"; then
  VIRT_ENABLED=yes
  if ask_yn "Criar a ponte br0 (as VMs recebem IP do seu roteador, como os outros aparelhos)?" "$(yn_default "${VIRT_BRIDGE:-yes}")"; then VIRT_BRIDGE=yes; else VIRT_BRIDGE=no; fi
else
  VIRT_ENABLED=no; VIRT_BRIDGE=no
fi

if ask_yn "Ajustes de servidor (fsck automático no boot, console sem apagar, IOMMU com virtualização)?" "$(yn_default "${SERVER_TWEAKS:-yes}")"; then SERVER_TWEAKS=yes; else SERVER_TWEAKS=no; fi

# ============================================================ RESUMO
header "Resumo"
cat >&2 <<EOF
Ubuntu:        $UBUNTU_SERIES LTS (live-server amd64)
Hostname:      $HOSTNAME_
Usuário:       $ADMIN_USER ($ADMIN_REALNAME)
Timezone:      $TIMEZONE   Locale: $LOCALE   Teclado: $KEYBOARD_LAYOUT ${KEYBOARD_VARIANT}
Rede:          $NET_MODE $( [ "$NET_MODE" = static ] && printf '%s via %s (match %s=%s) DNS %s' "$NET_ADDRESS" "$NET_GATEWAY" "$NET_MATCH_TYPE" "$NET_MATCH_VALUE" "$NET_DNS" )
Disco:         $DISK_DESC, min ${STORAGE_MIN_GB}GB, $STORAGE_LAYOUT — $( [ -n "$STORAGE_CONFIRMED" ] && echo "CONFIRMADO (APAGAR)" || echo "NÃO CONFIRMADO" )
SSH:           porta ${SSH_PORT:-22}, $( if [ "$SSH_AUTH" = password ]; then echo "usuário e senha (só rede local/Tailscale) + $(grep -c . "$AUTHKEYS_FILE" || true) chave(s)"; else echo "$(grep -c . "$AUTHKEYS_FILE") chave(s); senha SSH desativada após confirmar a chave"; fi )
Tailscale:     $TAILSCALE_ENABLED $( [ "$TAILSCALE_ENABLED" = yes ] && printf '(nome %s, key %s)' "$TAILSCALE_HOSTNAME" "$( [ -s "$TS_AUTHKEY_FILE" ] && mask_secret "$(cat "$TS_AUTHKEY_FILE")" )" )
Programas:     docker=$DOCKER_ENABLED coolify=$COOLIFY_ENABLED cockpit=$COCKPIT_ENABLED
Pacotes extra: ${EXTRA_PACKAGES:-(nenhum)}
Extras:        ddns=$DDNS_ENABLED${DDNS_HOSTNAME:+ ($DDNS_HOSTNAME)} cloudflare=$CLOUDFLARE_ENABLED gpu=$GPU_NVIDIA virt=$VIRT_ENABLED br0=$VIRT_BRIDGE ajustes=$SERVER_TWEAKS
EOF
ask_yn "Salvar esta configuração?" S || die "Cancelado; nada foi salvo em config/install.env."

{
  echo "# Gerado por setup.sh em $(date -u +%Y-%m-%dT%H:%M:%SZ). NÃO contém segredos."
  echo "# Segredos ficam em secrets/ (fora do Git)."
  for v in UBUNTU_SERIES HOSTNAME_ ADMIN_USER ADMIN_REALNAME TIMEZONE LOCALE KEYBOARD_LAYOUT KEYBOARD_VARIANT \
           NET_MODE NET_MATCH_TYPE NET_MATCH_VALUE NET_ADDRESS NET_GATEWAY NET_DNS NET_SEARCH \
           STORAGE_MATCH_TYPE STORAGE_MATCH_VALUE STORAGE_MIN_GB STORAGE_LAYOUT STORAGE_ALLOW_USB STORAGE_CONFIRMED \
           SSH_AUTH SSH_PORT TAILSCALE_ENABLED TAILSCALE_HOSTNAME TAILSCALE_EXTRA_ARGS DOCKER_ENABLED \
           COOLIFY_ENABLED COCKPIT_ENABLED COOLIFY_ADMIN_EMAIL EXTRA_PACKAGES \
           DDNS_ENABLED DDNS_HOSTNAME DDNS_LOGIN CLOUDFLARE_ENABLED GPU_NVIDIA VIRT_ENABLED VIRT_BRIDGE SERVER_TWEAKS; do
    eval "val=\${$v:-}"
    # shellcheck disable=SC2154
    printf '%s=%s\n' "$v" "$(shell_quote "$val")"
  done
} | write_private_file "$CONFIG_FILE"
ok "Configuração salva em config/install.env"
