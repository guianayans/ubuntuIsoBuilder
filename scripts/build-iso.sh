#!/usr/bin/env bash
# Reconstrói a ISO: parte da ISO oficial, adiciona/substitui os arquivos do
# stage e REPRODUZ exatamente a configuração de boot original
# (-boot_image any replay): El Torito BIOS, imagem EFI em partição GPT anexada,
# MBR híbrido/protetivo. Resultado: ISO híbrida bootável em UEFI e BIOS,
# gravável em pendrive (dd) ou DVD.
# Uso: scripts/build-iso.sh <iso-original> <stage> <iso-saida>
source "$(dirname "$0")/common.sh"

SRC_ISO="${1:?iso}"; STAGE="${2:?stage}"; OUT="${3:?saida}"
mkdir -p "$(dirname "$OUT")"
rm -f "$OUT" "$OUT.part"

args=(-indev "$SRC_ISO" -outdev "$OUT.part"
      -map "$STAGE/nocloud" /nocloud
      -map "$STAGE/uab" /uab
      -map "$STAGE/boot/grub/grub.cfg" /boot/grub/grub.cfg
      -map "$STAGE/md5sum.txt" /md5sum.txt)
[ -f "$STAGE/boot/grub/loopback.cfg" ] && args+=(-map "$STAGE/boot/grub/loopback.cfg" /boot/grub/loopback.cfg)
args+=(-boot_image any replay)

info "Gerando ISO com xorriso (preservando boot UEFI/BIOS original)…"
if ! xorriso -report_about WARNING "${args[@]}" >"$(dirname "$STAGE")/xorriso.log" 2>&1; then
  tail -n 30 "$(dirname "$STAGE")/xorriso.log" >&2
  rm -f "$OUT.part"
  die "xorriso falhou (log em $(dirname "$STAGE")/xorriso.log)"
fi
mv -f "$OUT.part" "$OUT"
chmod 600 "$OUT"
ok "ISO gerada: ${OUT#$UAB_ROOT/} ($(du -h "$OUT" | awk '{print $1}'))"
