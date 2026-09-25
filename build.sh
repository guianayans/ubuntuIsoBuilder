#!/usr/bin/env bash
# Gera (ou regenera) a ISO a partir de config/install.env + secrets/.
# Uso: ./build.sh [--keep-work] [--forget-secrets]
#   --keep-work       mantém work/ após o build (depuração; contém segredos!)
#   --forget-secrets  apaga secrets/tailscale-authkey após gerar a ISO
source "$(dirname "$0")/scripts/common.sh"

KEEP_WORK=0; FORGET=0
for a in "$@"; do
  case "$a" in
    --keep-work) KEEP_WORK=1 ;;
    --forget-secrets) FORGET=1 ;;
    -h|--help) sed -n '2,5p' "$0"; exit 0 ;;
    *) die "argumento desconhecido: $a" ;;
  esac
done

header "1/7 Dependências"
"$UAB_ROOT/scripts/check-deps.sh" --install

header "2/7 Configuração"
load_config
if [ "${TAILSCALE_ENABLED:-no}" = yes ] && [ ! -s "$TS_AUTHKEY_FILE" ]; then
  [ -t 0 ] || die "Auth key do Tailscale ausente e sem terminal para perguntar."
  warn "Tailscale ativado, mas a auth key não está salva (ex.: foi apagada após o build anterior)."
  while true; do
    ask_secret TSKEY "Cole a auth key do Tailscale (entrada oculta)"
    TSKEY="$(printf '%s' "$TSKEY" | tr -d '[:space:]')"
    printf '%s\n' "$TSKEY" | grep -Eq '^tskey-(auth-)?[A-Za-z0-9_-]{10,}$' && break
    warn "Formato inesperado."
  done
  mkdir -p "$SECRETS_DIR"; chmod 700 "$SECRETS_DIR"
  printf '%s\n' "$TSKEY" | write_private_file "$TS_AUTHKEY_FILE"
  ok "Auth key $(mask_secret "$TSKEY") salva temporariamente em secrets/."
  TSKEY=""; unset TSKEY
fi
"$UAB_ROOT/scripts/validate-config.sh"

header "3/7 ISO oficial do Ubuntu"
SRC_ISO="$("$UAB_ROOT/scripts/download-ubuntu.sh" "$UBUNTU_SERIES" | tail -n1)"
[ -f "$SRC_ISO" ] || die "ISO base indisponível."

BUILD_ID="$(date -u +%Y%m%d%H%M%S)-$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')"
BUILD_DIR="$WORK_DIR/build-$BUILD_ID"
STAGE="$BUILD_DIR/stage"
OUT="$OUTPUT_DIR/${HOSTNAME_}-autoinstall.iso"
mkdir -p "$BUILD_DIR"; chmod 700 "$WORK_DIR" "$BUILD_DIR" 2>/dev/null || true
cleanup_work() {
  if [ "$KEEP_WORK" = 1 ]; then warn "work/ mantido em $BUILD_DIR (contém segredos; apague depois)"; return; fi
  [ -d "$BUILD_DIR" ] && { find "$BUILD_DIR" -type f -path '*secrets*' -exec sh -c 'command -v shred >/dev/null && shred -u "$@" || rm -f "$@"' _ {} + ; rm -rf "$BUILD_DIR"; }
}
trap cleanup_work EXIT

header "4/7 Autoinstall + NoCloud (build $BUILD_ID)"
"$UAB_ROOT/scripts/generate-autoinstall.sh" "$STAGE" "$BUILD_ID"
TSARG=()
[ -f "$STAGE/uab/secrets/tailscale-authkey" ] && TSARG=(--ts-key "$STAGE/uab/secrets/tailscale-authkey")
python3 "$UAB_ROOT/scripts/lib/uab.py" check "$STAGE/nocloud" --payload "$STAGE/uab/payload" ${TSARG[@]+"${TSARG[@]}"} \
  >"$BUILD_DIR/check.txt" || { cat "$BUILD_DIR/check.txt" >&2; die "Validação do Autoinstall falhou."; }
ok "YAML e estrutura Autoinstall válidos ($(grep -c '^OK' "$BUILD_DIR/check.txt") verificações)."

header "5/7 Boot (GRUB)"
"$UAB_ROOT/scripts/customize-iso.sh" "$SRC_ISO" "$STAGE" "$BUILD_ID"

header "6/7 Reconstrução da ISO"
"$UAB_ROOT/scripts/build-iso.sh" "$SRC_ISO" "$STAGE" "$OUT"
( cd "$OUTPUT_DIR" && printf '%s  %s\n' "$(sha256_of "$(basename "$OUT")")" "$(basename "$OUT")" >"$(basename "$OUT").sha256" )
cp "$BUILD_DIR/iso-boot-report.txt" "$OUTPUT_DIR/${HOSTNAME_}-boot-report.txt"
{
  echo "build_id=$BUILD_ID"
  echo "source_iso=$(basename "$SRC_ISO")"
  echo "source_sha256=$(sha256_of "$SRC_ISO")"
  echo "hostname=$HOSTNAME_"
  echo "created=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} >"$OUTPUT_DIR/${HOSTNAME_}-build-info.txt"

header "7/7 Verificação da ISO gerada"
if "$UAB_ROOT/scripts/verify-iso.sh" "$OUT" "$SRC_ISO" >"$BUILD_DIR/verify.txt"; then
  ok "ISO verificada ($(grep -c '^OK' "$BUILD_DIR/verify.txt") verificações)."
else
  grep -v '^OK' "$BUILD_DIR/verify.txt" >&2
  die "A verificação da ISO falhou."
fi

if [ "$FORGET" = 1 ] && [ -f "$TS_AUTHKEY_FILE" ]; then
  secure_delete "$TS_AUTHKEY_FILE"; ok "secrets/tailscale-authkey apagado."
fi

printf '\n%sISO pronta:%s %s\n' "$C_GRN$C_BLD" "$C_RST" "${OUT#$UAB_ROOT/}"
echo "SHA256: $(awk '{print $1}' "$OUT.sha256")"
[ "${TAILSCALE_ENABLED:-no}" = yes ] && warn "A ISO contém a auth key do Tailscale: trate o arquivo e o pendrive como sensíveis."
echo "Próximos passos: ./validate.sh  |  ./test-iso.sh (VM descartável)  |  gravar no pendrive (README)"
