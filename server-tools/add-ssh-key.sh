#!/bin/bash
# Adiciona uma chave PÚBLICA SSH ao authorized_keys do usuário administrativo.
# Uso:
#   uab-add-ssh-key 'ssh-ed25519 AAAA... usuario@notebook'
#   uab-add-ssh-key -f /caminho/chave.pub
#   cat chave.pub | uab-add-ssh-key -
#   uab-add-ssh-key --user outro 'ssh-ed25519 ...'
set -euo pipefail

TARGET_USER="${SUDO_USER:-$(id -un)}"
[ -r /etc/uab/uab.conf ] && [ "$(id -u)" = 0 ] && [ -z "${SUDO_USER:-}" ] && TARGET_USER="$(. /etc/uab/uab.conf; echo "$UAB_ADMIN_USER")"
SRC=""
while [ $# -gt 0 ]; do
  case "$1" in
    --user) TARGET_USER="$2"; shift 2 ;;
    -f) SRC="$(cat -- "$2")"; shift 2 ;;
    -) SRC="$(cat)"; shift ;;
    -h|--help) sed -n '2,7p' "$0"; exit 0 ;;
    *) SRC="$1"; shift ;;
  esac
done
[ -n "$SRC" ] || { sed -n '2,7p' "$0"; exit 2; }

if printf '%s\n' "$SRC" | grep -q -e '-----BEGIN' -e 'PRIVATE KEY'; then
  echo "ERRO: isto é uma CHAVE PRIVADA. Nunca copie a chave privada para o servidor; use o arquivo .pub." >&2
  exit 1
fi

HOME_DIR="$(getent passwd "$TARGET_USER" | cut -d: -f6)"
[ -n "$HOME_DIR" ] || { echo "usuário $TARGET_USER não existe" >&2; exit 1; }
AK="$HOME_DIR/.ssh/authorized_keys"
GROUP="$(id -gn "$TARGET_USER")"

tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
added=0
while IFS= read -r line || [ -n "$line" ]; do
  line="$(printf '%s' "$line" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//')"
  case "$line" in ''|'#'*) continue ;; esac
  if ! printf '%s\n' "$line" | grep -Eq '^(ssh-ed25519|ssh-rsa|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com) [A-Za-z0-9+/=]+( .*)?$'; then
    echo "ERRO: formato de chave pública não reconhecido: ${line:0:40}…" >&2; exit 1
  fi
  printf '%s\n' "$line" >"$tmp"
  fp="$(ssh-keygen -l -f "$tmp" 2>/dev/null)" || { echo "ERRO: ssh-keygen rejeitou a chave." >&2; exit 1; }
  install -d -m 0700 -o "$TARGET_USER" -g "$GROUP" "$HOME_DIR/.ssh"
  touch "$AK"
  if grep -qF -- "$(printf '%s' "$line" | awk '{print $2}')" "$AK"; then
    echo "Já existe: $fp"; continue
  fi
  cp -p "$AK" "$AK.bak.$(date +%Y%m%d%H%M%S)"
  printf '%s\n' "$line" >>"$AK"
  echo "Adicionada: $fp"
  added=$((added+1))
done <<<"$SRC"
chown "$TARGET_USER:$GROUP" "$AK"; chmod 600 "$AK"
echo "$added chave(s) adicionada(s) para $TARGET_USER. Teste o login a partir do novo computador ANTES de remover chaves antigas."
