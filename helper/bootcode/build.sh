#!/usr/bin/env bash
# Monta o código de boot BIOS (mbr.bin, fat32.bin) com o nasm num container descartável
# (nada é instalado no computador). Os .bin são versionados; mbr.bin e fat32.bin são embutidos
# no gravador (go:embed); testbootmgr.bin é só para o teste no QEMU (qemu-test.sh).
# Uso: helper/bootcode/build.sh
set -euo pipefail
cd "$(dirname "$0")"
docker run --rm --name uabtest-nasm -v "$PWD":/src -w /src golang:1-bookworm sh -c \
  'apt-get update -qq >/dev/null && apt-get install -y -qq nasm >/dev/null &&
   nasm -f bin -O9 -o mbr.bin mbr.asm && nasm -f bin -O9 -o fat32.bin fat32.asm &&
   nasm -f bin -O9 -o testbootmgr.bin testbootmgr.asm &&
   chown '"$(id -u):$(id -g)"' mbr.bin fat32.bin testbootmgr.bin'
ls -l mbr.bin fat32.bin
sha256sum mbr.bin fat32.bin testbootmgr.bin
