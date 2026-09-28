#!/usr/bin/env bash
# Teste REAL do código de boot BIOS no QEMU/KVM com SeaBIOS (sem UEFI): grava pendrives de
# vários tamanhos (clusters de 4, 8, 16 e 32 KB; até 200 GB, arquivos esparsos) com o mesmo
# código do gravador e um BOOTMGR falso (testbootmgr.asm), contíguo ou fragmentado no fim
# da partição, inicia cada um como disco USB e confere pela serial que o BOOTMGR inteiro foi
# carregado em 2000:0000 com os registradores certos.
# Uso: helper/bootcode/qemu-test.sh   (precisa de docker, qemu-system-x86_64 e /dev/kvm)
set -euo pipefail
HELPER="$(cd "$(dirname "$0")/.." && pwd)"
ROOT="$(cd "$HELPER/.." && pwd)"
OUT="${OUT:-$ROOT/work/win/boottest}"
mkdir -p "$OUT"
# trava das VMs (compartilhada com os outros testes: o disco é o da produção)
exec 9>"$ROOT/work/win/VM.lock"
flock 9
fails=0
for cfg in 6:0:pc 6:1:q35 12:1:pc 24:1:pc 40:0:pc 40:1:q35 200:1:pc; do
  IFS=: read -r gb frag machine <<<"$cfg"
  img="$OUT/boot-${gb}g-frag${frag}.img"
  rm -f "$img"
  docker run --rm --name "uabtest-bootimg-$$" -v "$ROOT:$ROOT" -w "$HELPER" \
    -e GOCACHE="$ROOT/work/tools/gocache" -e GOPATH="$ROOT/work/tools/gopath" -e GOTELEMETRY=off -e CGO_ENABLED=0 \
    -e UAB_BOOTIMG="$img" -e UAB_BOOTIMG_SIZE="$gb" -e UAB_BOOTIMG_FRAG="$frag" \
    golang:1-bookworm go test -count=1 -run TestMakeBootImage -v . | grep -E "bootcode_test|FAIL|ok " || true
  [ -f "$img" ] || { echo "imagem não criada"; exit 1; }
  serial="$OUT/serial-${gb}g-frag${frag}.txt"
  rm -f "$serial"
  set +e
  timeout 90 qemu-system-x86_64 -name uabtest-bootcode -enable-kvm -machine "$machine" -m 1024 \
    -drive id=usb,if=none,format=raw,file="$img" -device qemu-xhci,id=xhci \
    -device usb-storage,bus=xhci.0,drive=usb,bootindex=1 \
    -device isa-debug-exit,iobase=0xf4,iosize=1 -serial "file:$serial" \
    -display none -monitor none -no-reboot
  code=$?
  set -e
  line="$(tr -d "\r" <"$serial" 2>/dev/null | grep UABTEST | tail -1 || true)"
  if [ "$code" = 1 ] && grep -q "RESULTADO=OK" <<<"$line"; then r=OK; else r=FALHOU; fails=$((fails+1)); fi
  echo "== ${gb} GB, fragmentado=$frag, máquina=$machine: $r (saída do QEMU $code) | $line"
  rm -f "$img"
done
echo "falhas: $fails"
[ "$fails" = 0 ]
