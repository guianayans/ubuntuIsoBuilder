#!/usr/bin/env bash
# Baixa (se necessário) a ISO oficial do Ubuntu Server LTS e verifica:
#   1. assinatura GPG do SHA256SUMS com a chave oficial de imagens do Ubuntu;
#   2. SHA256 da ISO contra o SHA256SUMS assinado.
# Imprime o caminho da ISO verificada na última linha do stdout.
# Uso: scripts/download-ubuntu.sh <serie>   (ex.: 24.04, 26.04)
# Variáveis: UAB_UBUNTU_ISO=/caminho/local.iso  (usa ISO local, ainda verificada)
#            UAB_SKIP_GPG=1 (NÃO recomendado; só SHA256 via HTTPS)
source "$(dirname "$0")/common.sh"

SERIES="${1:-${UBUNTU_SERIES:-24.04}}"
case "$SERIES" in 2[0-9].04) ;; *) die "Série não suportada: $SERIES (use uma LTS, ex.: 24.04)";; esac
BASE_URL="https://releases.ubuntu.com/$SERIES"
# "Ubuntu CD Image Automatic Signing Key (2012) <cdimage@ubuntu.com>"
UBUNTU_CD_FPR="843938DF228D22F7B3742BC0D94AA3F0EFE21092"

SDIR="$CACHE_DIR/$SERIES"
mkdir -p "$SDIR"; chmod 755 "$CACHE_DIR" "$SDIR"

info "Obtendo SHA256SUMS oficiais de $BASE_URL"
curl -fsSL --retry 3 -o "$SDIR/SHA256SUMS.new" "$BASE_URL/SHA256SUMS" || die "Falha ao baixar SHA256SUMS"
curl -fsSL --retry 3 -o "$SDIR/SHA256SUMS.gpg.new" "$BASE_URL/SHA256SUMS.gpg" || die "Falha ao baixar SHA256SUMS.gpg"
mv -f "$SDIR/SHA256SUMS.new" "$SDIR/SHA256SUMS"; mv -f "$SDIR/SHA256SUMS.gpg.new" "$SDIR/SHA256SUMS.gpg"

if [ "${UAB_SKIP_GPG:-0}" = 1 ]; then
  warn "Verificação GPG DESATIVADA por UAB_SKIP_GPG=1."
else
  command -v gpg >/dev/null 2>&1 || die "gpg não encontrado (necessário para verificar a assinatura). Instale gnupg."
  export GNUPGHOME="$CACHE_DIR/gnupg"
  mkdir -p "$GNUPGHOME"; chmod 700 "$GNUPGHOME"
  if ! gpg --batch --list-keys "$UBUNTU_CD_FPR" >/dev/null 2>&1; then
    info "Importando chave de assinatura do Ubuntu ($UBUNTU_CD_FPR) de keyserver.ubuntu.com"
    curl -fsSL --retry 3 "https://keyserver.ubuntu.com/pks/lookup?op=get&options=mr&search=0x$UBUNTU_CD_FPR" -o "$GNUPGHOME/ubuntu-cd.asc" \
      || die "Falha ao obter a chave de assinatura."
    got="$(gpg --batch --show-keys --with-colons "$GNUPGHOME/ubuntu-cd.asc" 2>/dev/null | awk -F: '/^fpr:/{print $10; exit}')"
    [ "$got" = "$UBUNTU_CD_FPR" ] || die "Fingerprint inesperado da chave: $got"
    gpg --batch --quiet --import "$GNUPGHOME/ubuntu-cd.asc" 2>/dev/null
  fi
  status="$(gpg --batch --status-fd 1 --verify "$SDIR/SHA256SUMS.gpg" "$SDIR/SHA256SUMS" 2>/dev/null || true)"
  printf '%s\n' "$status" | grep -q "^\[GNUPG:\] VALIDSIG $UBUNTU_CD_FPR " \
    || printf '%s\n' "$status" | grep -q "^\[GNUPG:\] VALIDSIG .* $UBUNTU_CD_FPR\$" \
    || die "Assinatura GPG do SHA256SUMS INVÁLIDA. Abortando."
  ok "Assinatura GPG do SHA256SUMS válida (Ubuntu CD Image Signing Key)."
fi

if [ -n "${UAB_UBUNTU_ISO:-}" ]; then
  ISO="$UAB_UBUNTU_ISO"; NAME="$(basename "$ISO")"
  [ -f "$ISO" ] || die "UAB_UBUNTU_ISO não existe: $ISO"
else
  NAME="$(grep -Eo "ubuntu-${SERIES//./\\.}(\.[0-9]+)*-live-server-amd64\.iso" "$SDIR/SHA256SUMS" | sort -V | tail -n1)"
  [ -n "$NAME" ] || die "Nenhuma ISO live-server amd64 listada em $BASE_URL/SHA256SUMS"
  ISO="$CACHE_DIR/$NAME"
fi
EXPECTED="$(awk -v n="$NAME" '{f=$2; sub(/^\*/,"",f); if (f==n) print $1}' "$SDIR/SHA256SUMS")"
[ -n "$EXPECTED" ] || die "$NAME não consta no SHA256SUMS oficial."

verify() { [ "$(sha256_of "$1")" = "$EXPECTED" ]; }

if [ -f "$ISO" ]; then
  info "ISO em cache: $NAME — verificando SHA256…"
  if verify "$ISO"; then ok "SHA256 confere: $EXPECTED"; printf '%s\n' "$ISO"; exit 0; fi
  [ -n "${UAB_UBUNTU_ISO:-}" ] && die "SHA256 da ISO local NÃO confere."
  warn "SHA256 não confere; baixando novamente."
  rm -f "$ISO"
fi

info "Baixando $BASE_URL/$NAME (pode demorar)…"
curl -fL --retry 5 --retry-delay 5 -C - -o "$ISO.part" "$BASE_URL/$NAME" || die "Falha no download."
info "Verificando SHA256…"
if ! verify "$ISO.part"; then rm -f "$ISO.part"; die "SHA256 NÃO confere após download. Arquivo removido."; fi
mv -f "$ISO.part" "$ISO"; chmod 644 "$ISO"
ok "SHA256 confere: $EXPECTED"
printf '%s\n' "$ISO"
