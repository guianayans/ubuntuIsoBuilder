#!/usr/bin/env bash
# Verifica uma ISO gerada: estrutura de boot (UEFI/BIOS) comparada à original,
# GRUB, arquivos NoCloud, payload, validação do Autoinstall e checksum.
# Uso: scripts/verify-iso.sh <iso-gerada> [iso-original]
# Saída: linhas "OK|ERR<TAB>Seção<TAB>mensagem"; código 0 se tudo OK.
source "$(dirname "$0")/common.sh"

ISO="${1:?iso}"; SRC_ISO="${2:-}"
[ -f "$ISO" ] || { printf 'ERR\tISO\tarquivo não encontrado: %s\n' "$ISO"; exit 1; }

T="$(mktemp -d "$TMPDIR/uab-verify.XXXXXX")"
cleanup() { find "$T" -type f -exec sh -c 'command -v shred >/dev/null && shred -u "$@" || rm -f "$@"' _ {} + 2>/dev/null; rm -rf "$T"; }
trap cleanup EXIT
rc=0
res() { printf '%s\t%s\t%s\n' "$1" "$2" "$3"; [ "$1" = OK ] || rc=1; }

# ---- estrutura de boot
xorriso -indev "$ISO" -report_el_torito as_mkisofs -report_system_area plain >"$T/report.txt" 2>/dev/null \
  && res OK ISO "xorriso lê a ISO" || res ERR ISO "xorriso não lê a ISO"
grep -q -- "-e '" "$T/report.txt" && res OK Bootloader "imagem de boot UEFI (El Torito EFI) presente" || res ERR Bootloader "sem imagem de boot UEFI"
grep -q -- "-append_partition 2 28732ac11ff8d211ba4b00a0c93ec93b" "$T/report.txt" \
  && res OK Bootloader "partição ESP anexada (GPT) presente — boot UEFI via pendrive" || res ERR Bootloader "partição EFI anexada ausente"
if [ -n "$SRC_ISO" ] && [ -f "$SRC_ISO" ]; then
  xorriso -indev "$SRC_ISO" -report_el_torito as_mkisofs >"$T/orig.txt" 2>/dev/null
  if grep -q -- "-b '/boot/grub/i386-pc/eltorito.img'" "$T/orig.txt"; then
    grep -q -- "-b '/boot/grub/i386-pc/eltorito.img'" "$T/report.txt" \
      && res OK Bootloader "boot BIOS (El Torito eltorito.img) preservado" || res ERR Bootloader "boot BIOS da ISO original foi perdido"
  fi
  for opt in "--grub2-mbr" "--protective-msdos-label" "-appended_part_as_gpt" "--mbr-force-bootable"; do
    if grep -q -- "^$opt" "$T/orig.txt"; then
      grep -q -- "^$opt" "$T/report.txt" && res OK Bootloader "opção híbrida $opt preservada" || res ERR Bootloader "opção $opt perdida"
    fi
  done
  ov="$(grep '^-V ' "$T/orig.txt")"; nv="$(grep '^-V ' "$T/report.txt")"
  [ "$ov" = "$nv" ] && res OK ISO "volume id preservado ($nv)" || res ERR ISO "volume id mudou ($ov -> $nv)"
fi

# ---- arquivos
xorriso -osirrox on -indev "$ISO" \
  -extract /boot/grub/grub.cfg "$T/grub.cfg" \
  -extract /nocloud "$T/nocloud" \
  -extract /uab "$T/uab" \
  -extract /md5sum.txt "$T/md5sum.txt" >/dev/null 2>&1 || true
chmod -R u+rw "$T" 2>/dev/null || true

if [ -f "$T/grub.cfg" ]; then
  grep -q "autoinstall 'ds=nocloud;s=/cdrom/nocloud/'" "$T/grub.cfg" \
    && res OK Bootloader "GRUB: autoinstall + ds=nocloud;s=/cdrom/nocloud/ (';' protegido por aspas)" \
    || res ERR Bootloader "GRUB sem parâmetros autoinstall/NoCloud corretos"
  grep -q "set default=uab-autoinstall" "$T/grub.cfg" && res OK Bootloader "entrada autoinstall é o padrão" || res ERR Bootloader "autoinstall não é o padrão"
  grep -q "search --no-floppy --file --set=uab_disk" "$T/grub.cfg" && res OK Bootloader "proteção anti-reinstalação presente" || res ERR Bootloader "proteção anti-reinstalação ausente"
  for k in $(awk '$1=="linux"{print $2} $1=="initrd"{print $2}' "$T/grub.cfg" | sort -u); do
    xorriso -indev "$ISO" -find "$k" 2>/dev/null | grep -q . && res OK Bootloader "arquivo de boot existe: $k" || res ERR Bootloader "arquivo de boot ausente: $k"
  done
  want="$(awk '$2=="./boot/grub/grub.cfg"{print $1}' "$T/md5sum.txt" 2>/dev/null)"
  got="$(python3 -c 'import hashlib,sys;print(hashlib.md5(open(sys.argv[1],"rb").read()).hexdigest())' "$T/grub.cfg")"
  [ "$want" = "$got" ] && res OK ISO "md5sum.txt coerente com grub.cfg" || res ERR ISO "md5sum.txt desatualizado para grub.cfg"
else
  res ERR Bootloader "grub.cfg não encontrado"
fi

for f in user-data meta-data; do
  [ -f "$T/nocloud/$f" ] && res OK NoCloud "/nocloud/$f presente" || res ERR NoCloud "/nocloud/$f ausente"
done
for f in preflight.sh late-install.sh on-error.sh payload/uab-provision.sh payload/uab-report.sh payload/uab.conf payload/authorized_keys payload/systemd/uab-provision.service; do
  [ -f "$T/uab/$f" ] && res OK ISO "/uab/$f presente" || res ERR ISO "/uab/$f ausente"
done
if [ -d "$T/nocloud" ]; then
  tskey_arg=()
  [ -f "$T/uab/secrets/tailscale-authkey" ] && tskey_arg=(--ts-key "$T/uab/secrets/tailscale-authkey")
  python3 "$UAB_ROOT/scripts/lib/uab.py" check "$T/nocloud" --payload "$T/uab/payload" ${tskey_arg[@]+"${tskey_arg[@]}"} || rc=1
  if grep -q '^UAB_TAILSCALE=yes' "$T/uab/payload/uab.conf" 2>/dev/null && [ ! -f "$T/uab/secrets/tailscale-authkey" ]; then
    printf 'INFO\tTailscale\tISO sem auth key: o Tailscale será instalado, mas o login precisará ser manual\n'
  fi
fi
if grep -rqE -- '-----BEGIN ([A-Z]+ )?PRIVATE KEY-----' "$T/nocloud" "$T/uab" 2>/dev/null; then
  res ERR SSH "CHAVE PRIVADA encontrada dentro da ISO"
else
  res OK SSH "nenhuma chave privada dentro da ISO"
fi

# ---- checksum
sum="$(sha256_of "$ISO")"
if [ -f "$ISO.sha256" ]; then
  [ "$(awk '{print $1}' "$ISO.sha256")" = "$sum" ] && res OK Checksum "SHA256 confere com $(basename "$ISO").sha256" || res ERR Checksum "SHA256 NÃO confere com $(basename "$ISO").sha256"
else
  res OK Checksum "SHA256 $sum"
fi
exit $rc
