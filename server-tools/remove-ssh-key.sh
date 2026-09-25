#!/bin/bash
# Remove uma chave pública SSH do authorized_keys.
# Uso:
#   uab-remove-ssh-key <numero>            (número mostrado por uab-list-ssh-keys)
#   uab-remove-ssh-key SHA256:xxxxxxxx     (fingerprint)
#   uab-remove-ssh-key --user outro <...>
# Segurança: recusa remover a ÚLTIMA chave (evita perder o acesso remoto) e
# faz backup antes de alterar.
set -euo pipefail

TARGET_USER="${SUDO_USER:-$(id -un)}"
[ -r /etc/uab/uab.conf ] && [ "$(id -u)" = 0 ] && [ -z "${SUDO_USER:-}" ] && TARGET_USER="$(. /etc/uab/uab.conf; echo "$UAB_ADMIN_USER")"
SEL=""; YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --user) TARGET_USER="$2"; shift 2 ;;
    -y|--yes) YES=1; shift ;;
    -h|--help) sed -n '2,8p' "$0"; exit 0 ;;
    *) SEL="$1"; shift ;;
  esac
done
[ -n "$SEL" ] || { sed -n '2,8p' "$0"; exit 2; }

HOME_DIR="$(getent passwd "$TARGET_USER" | cut -d: -f6)"
AK="$HOME_DIR/.ssh/authorized_keys"
[ -w "$AK" ] || { echo "Sem permissão para alterar $AK (use sudo?)" >&2; exit 1; }

tmp="$(mktemp)"; out="$(mktemp)"; trap 'rm -f "$tmp" "$out"' EXIT
i=0; total=0; removed=""
while IFS= read -r line || [ -n "$line" ]; do
  case "$line" in ''|'#'*) printf '%s\n' "$line" >>"$out"; continue ;; esac
  i=$((i+1))
  printf '%s\n' "$line" >"$tmp"
  fp="$(ssh-keygen -l -f "$tmp" 2>/dev/null | awk '{print $2}')" || fp=""
  if [ "$SEL" = "$i" ] || { [ -n "$fp" ] && [ "$SEL" = "$fp" ]; }; then
    removed="$(ssh-keygen -l -f "$tmp" 2>/dev/null || echo "linha $i")"
    continue
  fi
  printf '%s\n' "$line" >>"$out"
  total=$((total+1))
done <"$AK"

[ -n "$removed" ] || { echo "Nenhuma chave corresponde a '$SEL'. Veja: uab-list-ssh-keys" >&2; exit 1; }
if [ "$total" -lt 1 ]; then
  echo "RECUSADO: esta é a última chave autorizada. Adicione outra chave antes (uab-add-ssh-key) para não perder o acesso." >&2
  exit 1
fi
echo "Remover: $removed"
if [ "$YES" != 1 ]; then
  printf 'Confirmar remoção? [s/N]: '; read -r a
  case "$a" in [sSyY]*) ;; *) echo "Cancelado."; exit 1 ;; esac
fi
cp -p "$AK" "$AK.bak.$(date +%Y%m%d%H%M%S)"
cat "$out" >"$AK"
echo "Removida. Restam $total chave(s). Backup salvo ao lado de $AK."
