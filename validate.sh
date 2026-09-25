#!/usr/bin/env bash
# Valida configuração, Autoinstall gerado e a ISO final.
# Uso: ./validate.sh [--verbose] [caminho.iso]
source "$(dirname "$0")/scripts/common.sh"
umask 077

VERBOSE=0; ISO=""
for a in "$@"; do
  case "$a" in -v|--verbose) VERBOSE=1 ;; -h|--help) sed -n '2,3p' "$0"; exit 0 ;; *) ISO="$a" ;; esac
done

load_config
[ -n "$ISO" ] || ISO="$OUTPUT_DIR/${HOSTNAME_}-autoinstall.iso"

T="$(mktemp -d "$TMPDIR/uab-validate.XXXXXX")"
trap 'rm -rf "$T"' EXIT
RES="$T/results.tsv"; : >"$RES"

# 1. configuração
if "$UAB_ROOT/scripts/validate-config.sh" --quiet >"$T/cfg.log" 2>&1; then
  printf 'OK\tConfig\tconfig/install.env e segredos válidos\n' >>"$RES"
else
  sed 's/\x1b\[[0-9;]*m//g' "$T/cfg.log" | sed -n 's/^\[FALHA\] /ERR\tConfig\t/p; s/^\[ERRO\] /ERR\tConfig\t/p' >>"$RES"
fi

# 2. Autoinstall gerado a partir da configuração atual (sem montar ISO)
if [ -s "$PASSWORD_HASH_FILE" ] && [ -s "$AUTHKEYS_FILE" ]; then
  TSARG=()
  [ -s "$TS_AUTHKEY_FILE" ] && TSARG=(--ts-key "$TS_AUTHKEY_FILE")
  UAB_NO_TS_KEY=1 "$UAB_ROOT/scripts/generate-autoinstall.sh" "$T/stage" "validate" >/dev/null 2>&1 \
    && python3 "$UAB_ROOT/scripts/lib/uab.py" check "$T/stage/nocloud" --payload "$T/stage/uab/payload" \
        ${TSARG[@]+"${TSARG[@]}"} >>"$RES" \
    || printf 'ERR\tAutoinstall\tfalha ao gerar/validar user-data a partir da configuração\n' >>"$RES"
  if command -v cloud-init >/dev/null 2>&1; then
    if cloud-init schema -c "$T/stage/nocloud/user-data" >"$T/ci.log" 2>&1; then
      printf 'OK\tAutoinstall\tcloud-init schema: válido\n' >>"$RES"
    else
      printf 'ERR\tAutoinstall\tcloud-init schema: %s\n' "$(tail -n1 "$T/ci.log")" >>"$RES"
    fi
  fi
  # Validação real do netplan, quando disponível (Linux Ubuntu/Debian).
  if command -v netplan >/dev/null 2>&1; then
    mkdir -p "$T/np/etc/netplan"
    python3 -c 'import sys,yaml; ai=yaml.safe_load(open(sys.argv[1]))["autoinstall"]; print(yaml.safe_dump({"network": ai["network"]}))' \
      "$T/stage/nocloud/user-data" >"$T/np/etc/netplan/50-uab.yaml"
    chmod 600 "$T/np/etc/netplan/50-uab.yaml"
    if netplan generate --root-dir "$T/np" >"$T/np.log" 2>&1; then
      printf 'OK\tNetwork\tnetplan generate: configuração aceita\n' >>"$RES"
    else
      printf 'ERR\tNetwork\tnetplan generate: %s\n' "$(head -n1 "$T/np.log")" >>"$RES"
    fi
  fi
  secure_delete "$T/stage/nocloud/user-data"
fi

# 3. ISO
if [ -f "$ISO" ]; then
  SRC=""
  for f in "$CACHE_DIR"/ubuntu-"$UBUNTU_SERIES"*-live-server-amd64.iso; do [ -f "$f" ] && SRC="$f"; done
  "$UAB_ROOT/scripts/verify-iso.sh" "$ISO" "$SRC" >>"$RES" 2>/dev/null || true
else
  printf 'ERR\tISO\tISO não encontrada: %s (rode ./build.sh)\n' "${ISO#$UAB_ROOT/}" >>"$RES"
fi

# Seções de relatório
map_section() {
  case "$1" in
    Autoinstall|Config|NoCloud) echo Autoinstall ;;
    Hostname) echo Hostname ;;
    SSH) echo SSH ;;
    Network) echo Network ;;
    Storage) echo Storage ;;
    Tailscale) echo Tailscale ;;
    ISO|Bootloader|Checksum) echo ISO ;;
    *) echo "$1" ;;
  esac
}
echo
rc=0
for sec in Autoinstall Hostname SSH Network Storage Tailscale ISO; do
  errs=""; n=0
  while IFS=$'\t' read -r st s msg; do
    [ "$(map_section "$s")" = "$sec" ] || continue
    [ "$st" = INFO ] && { [ "$VERBOSE" = 1 ] && printf '        info: %s\n' "$msg"; continue; }
    n=$((n+1))
    if [ "$st" != OK ]; then errs="$errs\n        - $msg"; elif [ "$VERBOSE" = 1 ]; then printf '        %s: %s\n' "$s" "$msg"; fi
  done <"$RES"
  if [ "$n" = 0 ]; then
    printf '%s[--]%s %s (nada a verificar)\n' "$C_YEL" "$C_RST" "$sec"
  elif [ -z "$errs" ]; then
    printf '%s[OK]%s %s\n' "$C_GRN" "$C_RST" "$sec"
  else
    printf '%s[FALHA]%s %s' "$C_RED" "$C_RST" "$sec"; printf '%b\n' "$errs"; rc=1
  fi
done
grep -q '^INFO' "$RES" && grep '^INFO' "$RES" | cut -f3 | sed 's/^/[INFO] /'
exit $rc
