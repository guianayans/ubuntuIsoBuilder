# shellcheck shell=bash
# Funções comuns a todos os scripts do ubuntu-autoinstall-builder.
# Compatível com bash 3.2 (macOS) e bash 5 (Linux).

set -o errexit -o nounset -o pipefail
umask 077

UAB_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export UAB_ROOT
# UAB_STATE_DIR permite isolar config/segredos/saída por build (usado pela
# interface web: um diretório por job). Padrão: o próprio projeto.
UAB_STATE="${UAB_STATE_DIR:-$UAB_ROOT}"
CONFIG_FILE="${UAB_CONFIG_FILE:-$UAB_STATE/config/install.env}"
SECRETS_DIR="$UAB_STATE/secrets"
CACHE_DIR="${UAB_CACHE_DIR:-$UAB_ROOT/cache}"
WORK_DIR="$UAB_STATE/work"
OUTPUT_DIR="$UAB_STATE/output"
AUTHKEYS_FILE="$UAB_STATE/config/authorized_keys"
PASSWORD_HASH_FILE="$SECRETS_DIR/password.hash"
TS_AUTHKEY_FILE="$SECRETS_DIR/tailscale-authkey"
COOLIFY_PW_FILE="$SECRETS_DIR/coolify-password"
DDNS_PW_FILE="$SECRETS_DIR/ddns-password"
CF_TOKEN_FILE="$SECRETS_DIR/cloudflare-token"
REMOTE_MESHID_FILE="$SECRETS_DIR/remote-meshid"
TSEC_TOKEN_FILE="$SECRETS_DIR/tsec-token"

# Arquivos temporários ficam dentro do projeto (work/tmp, 700), nunca em /tmp.
TMPDIR="$WORK_DIR/tmp"
export TMPDIR
mkdir -p "$TMPDIR" && chmod 700 "$WORK_DIR" "$TMPDIR" 2>/dev/null || true

if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YEL=$'\033[33m'; C_BLU=$'\033[34m'
  C_BLD=$'\033[1m'; C_RST=$'\033[0m'
else
  C_RED=""; C_GRN=""; C_YEL=""; C_BLU=""; C_BLD=""; C_RST=""
fi

info()  { printf '%s[INFO]%s %s\n' "$C_BLU" "$C_RST" "$*" >&2; }
warn()  { printf '%s[AVISO]%s %s\n' "$C_YEL" "$C_RST" "$*" >&2; }
error() { printf '%s[ERRO]%s %s\n' "$C_RED" "$C_RST" "$*" >&2; }
ok()    { printf '%s[OK]%s %s\n' "$C_GRN" "$C_RST" "$*" >&2; }
fail()  { printf '%s[FALHA]%s %s\n' "$C_RED" "$C_RST" "$*" >&2; }
die()   { error "$*"; exit 1; }
header() { printf '\n%s== %s ==%s\n' "$C_BLD" "$*" "$C_RST" >&2; }

# Mostra apenas o início e o fim de um segredo.
mask_secret() {
  local s="${1:-}" n
  n=${#s}
  if [ "$n" -le 12 ]; then printf '****'; else printf '%s…%s' "${s:0:8}" "${s:$((n-4))}"; fi
}

# ---------------------------------------------------------------- prompts
# ask VAR "Pergunta" "default"
ask() {
  local __var="$1" __q="$2" __def="${3:-}" __ans
  if [ -n "$__def" ]; then
    printf '%s [%s]: ' "$__q" "$__def" >&2
  else
    printf '%s: ' "$__q" >&2
  fi
  IFS= read -r __ans || die "Entrada encerrada."
  [ -z "$__ans" ] && __ans="$__def"
  printf -v "$__var" '%s' "$__ans"
}

# ask_yn "Pergunta" S|N  -> retorno 0 = sim
ask_yn() {
  local q="$1" def="${2:-S}" ans hint
  if [ "$def" = "S" ]; then hint="S/n"; else hint="s/N"; fi
  while true; do
    printf '%s [%s]: ' "$q" "$hint" >&2
    IFS= read -r ans || die "Entrada encerrada."
    [ -z "$ans" ] && ans="$def"
    case "$ans" in
      [SsYy]|[Ss]im|[Yy]es) return 0 ;;
      [Nn]|[Nn]ao|[Nn]ão|[Nn]o) return 1 ;;
    esac
    warn "Responda s ou n."
  done
}

# ask_choice VAR "Pergunta" default opção1 opção2 ...  (VAR recebe o número)
ask_choice() {
  local __var="$1" __q="$2" __def="$3" __i=1 __ans __n
  shift 3
  __n=$#
  printf '%s\n' "$__q" >&2
  for opt in "$@"; do printf '  %d. %s\n' "$__i" "$opt" >&2; __i=$((__i+1)); done
  while true; do
    printf 'Escolha [%s]: ' "$__def" >&2
    IFS= read -r __ans || die "Entrada encerrada."
    [ -z "$__ans" ] && __ans="$__def"
    case "$__ans" in
      ''|*[!0-9]*) ;;
      *) if [ "$__ans" -ge 1 ] && [ "$__ans" -le "$__n" ]; then printf -v "$__var" '%s' "$__ans"; return 0; fi ;;
    esac
    warn "Opção inválida."
  done
}

# ask_secret VAR "Pergunta"  (sem eco)
ask_secret() {
  local __var="$1" __q="$2" __ans
  printf '%s: ' "$__q" >&2
  IFS= read -r -s __ans || die "Entrada encerrada."
  printf '\n' >&2
  printf -v "$__var" '%s' "$__ans"
  __ans=""
}

# ---------------------------------------------------------------- config
# Escreve KEY='valor' com escape seguro para shell.
shell_quote() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }

load_config() {
  [ -f "$CONFIG_FILE" ] || die "Configuração não encontrada: $CONFIG_FILE. Execute ./setup.sh primeiro."
  # Rejeita qualquer linha que não seja comentário, vazia ou KEY='valor'.
  if grep -Ev "^[[:space:]]*(#.*)?$|^[A-Z_][A-Z0-9_]*='([^']|'\\\\'')*'$" "$CONFIG_FILE" >/dev/null; then
    die "Formato inválido em $CONFIG_FILE (apenas linhas KEY='valor' são aceitas)."
  fi
  # shellcheck disable=SC1090
  . "$CONFIG_FILE"
}

# Grava arquivo com permissão 600 de forma atômica.
write_private_file() {
  local dest="$1" tmp
  tmp="$(mktemp "${dest}.XXXXXX")"
  chmod 600 "$tmp"
  cat >"$tmp"
  mv -f "$tmp" "$dest"
  chmod 600 "$dest"
}

secure_delete() {
  local f
  for f in "$@"; do
    [ -e "$f" ] || continue
    if command -v shred >/dev/null 2>&1; then shred -u -z "$f" 2>/dev/null || rm -f "$f"; else rm -f "$f"; fi
  done
}

# ---------------------------------------------------------------- utilidades
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  else shasum -a 256 "$1" | awk '{print $1}'; fi
}

python_ok() { command -v python3 >/dev/null 2>&1 && python3 -c 'import yaml' >/dev/null 2>&1; }

os_kind() {
  case "$(uname -s)" in
    Darwin) echo macos ;;
    Linux) if command -v apt-get >/dev/null 2>&1; then echo debian; elif command -v dnf >/dev/null 2>&1; then echo fedora; else echo linux; fi ;;
    *) echo other ;;
  esac
}

is_valid_hostname() {
  printf '%s\n' "$1" | grep -Eq '^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$'
}

is_valid_username() {
  printf '%s\n' "$1" | grep -Eq '^[a-z_][a-z0-9_-]{0,31}$' || return 1
  case "$1" in root|daemon|bin|sys|sync|games|man|lp|mail|news|uucp|proxy|www-data|backup|list|irc|nobody|systemd-*|ubuntu|docker|tailscale|syslog|messagebus|sshd) return 1 ;; esac
  return 0
}

# Verifica se o arquivo/string é uma chave pública SSH válida (e NÃO privada).
# Uso: validate_pubkey_file arquivo
validate_pubkey_file() {
  local f="$1"
  [ -s "$f" ] || { error "Arquivo de chave vazio ou inexistente: $f"; return 1; }
  if grep -q -- '-----BEGIN' "$f" || grep -q 'PRIVATE KEY' "$f"; then
    error "O arquivo parece conter uma CHAVE PRIVADA. Nunca use a chave privada. Use o arquivo .pub."
    return 1
  fi
  local n=0 line tmp
  tmp="$(mktemp)"
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in ''|'#'*) continue ;; esac
    printf '%s\n' "$line" >"$tmp"
    if ! printf '%s\n' "$line" | grep -Eq '^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/=]+( .*)?$'; then
      rm -f "$tmp"; error "Linha não é uma chave pública SSH suportada: ${line:0:40}…"; return 1
    fi
    if command -v ssh-keygen >/dev/null 2>&1; then
      ssh-keygen -l -f "$tmp" >/dev/null 2>&1 || { rm -f "$tmp"; error "ssh-keygen rejeitou a chave: ${line:0:40}…"; return 1; }
      case "$line" in ssh-rsa*)
        local bits; bits="$(ssh-keygen -l -f "$tmp" | awk '{print $1}')"
        [ "$bits" -ge 3072 ] 2>/dev/null || { rm -f "$tmp"; error "Chave RSA com $bits bits é fraca (mínimo 3072). Prefira ed25519."; return 1; } ;;
      esac
    fi
    n=$((n+1))
  done <"$f"
  rm -f "$tmp"
  [ "$n" -ge 1 ] || { error "Nenhuma chave pública encontrada em $f"; return 1; }
  return 0
}

storage_confirm_token() {
  printf 'APAGAR:%s:%s' "${STORAGE_MATCH_TYPE:-}" "${STORAGE_MATCH_VALUE:-}"
}

# Porta do SSH: 22, ou 1024-65535 fora das portas dos programas da ISO
# (mesma regra de scripts/lib/uab.py e do painel).
valid_ssh_port() {
  case "$1" in ''|*[!0-9]*) return 1 ;; esac
  [ "$1" = 22 ] && return 0
  [ "$1" -ge 1024 ] && [ "$1" -le 65535 ] || return 1
  case " 6001 6002 8000 8080 9090 " in *" $1 "*) return 1 ;; esac
  return 0
}
