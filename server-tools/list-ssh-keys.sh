#!/bin/bash
# Lista as chaves públicas autorizadas (número, fingerprint, tipo, comentário).
# Uso: uab-list-ssh-keys [--user USUARIO]
set -euo pipefail

TARGET_USER="${SUDO_USER:-$(id -un)}"
[ -r /etc/uab/uab.conf ] && [ "$(id -u)" = 0 ] && [ -z "${SUDO_USER:-}" ] && TARGET_USER="$(. /etc/uab/uab.conf; echo "$UAB_ADMIN_USER")"
while [ $# -gt 0 ]; do
  case "$1" in
    --user) TARGET_USER="$2"; shift 2 ;;
    -h|--help) sed -n '2,3p' "$0"; exit 0 ;;
    *) echo "argumento desconhecido: $1" >&2; exit 2 ;;
  esac
done

HOME_DIR="$(getent passwd "$TARGET_USER" | cut -d: -f6)"
AK="$HOME_DIR/.ssh/authorized_keys"
[ -r "$AK" ] || { echo "Sem acesso a $AK (use sudo?)" >&2; exit 1; }

echo "Chaves autorizadas para $TARGET_USER ($AK):"
i=0; tmp="$(mktemp)"; trap 'rm -f "$tmp"' EXIT
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in ''|'#'*) continue ;; esac
  i=$((i+1))
  printf '%s\n' "$line" >"$tmp"
  if fp="$(ssh-keygen -l -f "$tmp" 2>/dev/null)"; then
    printf '  %2d) %s\n' "$i" "$fp"
  else
    printf '  %2d) (linha inválida) %.50s…\n' "$i" "$line"
  fi
done <"$AK"
[ "$i" -gt 0 ] || echo "  (nenhuma)"
