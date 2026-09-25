#!/usr/bin/env bash
# Inspeciona a ISO oficial e prepara os arquivos de boot modificados no stage:
#   <stage>/boot/grub/grub.cfg      (entrada autoinstall + proteção anti-reinstalação)
#   <stage>/boot/grub/loopback.cfg  (para boot via loopback/Ventoy)
#   <stage>/md5sum.txt              (atualizado para os arquivos alterados/adicionados)
#   <stage>/../iso-boot-report.txt  (parâmetros de boot da ISO original)
# Uso: scripts/customize-iso.sh <iso-original> <stage> <build-id> [--timeout N] [--extra-args "..."]
source "$(dirname "$0")/common.sh"
load_config

SRC_ISO="${1:?iso}"; STAGE="${2:?stage}"; BUILD_ID="${3:?build-id}"; shift 3
TIMEOUT=10; EXTRA_ARGS=""
while [ $# -gt 0 ]; do
  case "$1" in
    --timeout) TIMEOUT="$2"; shift 2 ;;
    --extra-args) EXTRA_ARGS="$2"; shift 2 ;;
    *) die "argumento desconhecido: $1" ;;
  esac
done

ORIG="$(dirname "$STAGE")/orig"
rm -rf "$ORIG"; mkdir -p "$ORIG" "$STAGE/boot/grub"

info "Inspecionando estrutura de boot da ISO original"
xorriso -indev "$SRC_ISO" -report_el_torito as_mkisofs -report_system_area plain \
  >"$(dirname "$STAGE")/iso-boot-report.txt" 2>/dev/null || die "xorriso não conseguiu ler a ISO."
REPORT="$(dirname "$STAGE")/iso-boot-report.txt"
grep -q -- "-b '/boot/grub/i386-pc/eltorito.img'" "$REPORT" && BIOS=1 || BIOS=0
grep -q -- "-e '" "$REPORT" && EFI=1 || EFI=0
[ "$EFI" = 1 ] || die "A ISO original não tem imagem de boot EFI; não suportada."
info "Boot original: UEFI=sim BIOS=$([ "$BIOS" = 1 ] && echo sim || echo não)"

xorriso -osirrox on -indev "$SRC_ISO" \
  -extract /boot/grub/grub.cfg "$ORIG/grub.cfg" \
  -extract /md5sum.txt "$ORIG/md5sum.txt" >/dev/null 2>&1 || die "Falha ao extrair grub.cfg/md5sum.txt"
xorriso -osirrox on -indev "$SRC_ISO" -extract /boot/grub/loopback.cfg "$ORIG/loopback.cfg" >/dev/null 2>&1 || true
chmod u+w "$ORIG"/* 2>/dev/null || true

python3 "$UAB_ROOT/scripts/lib/uab.py" grub --orig "$ORIG/grub.cfg" --out "$STAGE/boot/grub/grub.cfg" \
  --build-id "$BUILD_ID" --hostname "$HOSTNAME_" --timeout "$TIMEOUT" --extra-args "$EXTRA_ARGS" >&2
if [ -f "$ORIG/loopback.cfg" ]; then
  python3 "$UAB_ROOT/scripts/lib/uab.py" grub --loopback --orig "$ORIG/loopback.cfg" --out "$STAGE/boot/grub/loopback.cfg" \
    --build-id "$BUILD_ID" --hostname "$HOSTNAME_" --extra-args "$EXTRA_ARGS" >/dev/null
fi

# md5sum.txt: atualiza entradas alteradas e adiciona os arquivos novos
# (exceto segredos, cujo hash não deve ser publicado).
python3 - "$ORIG/md5sum.txt" "$STAGE" >"$STAGE/md5sum.txt" <<'EOF'
import hashlib, os, sys
orig, stage = sys.argv[1], sys.argv[2]
def md5(p):
    h = hashlib.md5()
    with open(p, "rb") as fh:
        for chunk in iter(lambda: fh.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()
new = {}
for root, _, files in os.walk(stage):
    for f in files:
        full = os.path.join(root, f)
        rel = "./" + os.path.relpath(full, stage)
        if rel == "./md5sum.txt" or "/secrets/" in rel:
            continue
        new[rel] = md5(full)
seen = set()
for line in open(orig, encoding="utf-8", errors="replace"):
    parts = line.rstrip("\n").split("  ", 1)
    if len(parts) == 2 and parts[1] in new:
        print(f"{new[parts[1]]}  {parts[1]}"); seen.add(parts[1])
    else:
        print(line.rstrip("\n"))
for rel in sorted(set(new) - seen):
    print(f"{new[rel]}  {rel}")
EOF
chmod 644 "$STAGE/md5sum.txt" "$STAGE/boot/grub/"*.cfg
ok "grub.cfg/loopback.cfg/md5sum.txt preparados."
