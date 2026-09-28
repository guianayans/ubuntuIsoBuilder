#!/bin/bash
# Teste de instalação do Windows 11 numa VM QEMU/KVM (sem o gravador):
#   - ISO do Windows como CD-ROM (SATA);
#   - autounattend.xml numa imagem FAT como PENDRIVE USB (removível);
#   - dois discos "internos" SATA: 30 GB (disco 0) e 64 GB (disco 1) -> tem que instalar no de 64 GB;
#   - rede "user" do QEMU: o host é 10.0.2.2 (painel de teste), 10.0.2.100:443 é encaminhado
#     para um proxy TLS local do MeshCentral de teste; SSH da VM em 127.0.0.1:$SSH_PORT.
# Uso: vm_test.sh start <autounattend.xml> | boot (discos existentes) | shot <arquivo.ppm> | key <tecla> | stop | clean
# Pasta: $VMDIR (padrão work/win/vm). Precisa de ~25 GB livres (discos qcow2 crescem sob demanda).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
VMDIR="${VMDIR:-$ROOT/work/win/vm}"
ISO="${ISO:-$ROOT/work/win/iso/$(cat "$ROOT/work/win/iso/READY" 2>/dev/null)}"
SSH_PORT="${SSH_PORT:-12222}"
MESH_TLS="${MESH_TLS:-127.0.0.1:18443}"
VNC="${VNC:-127.0.0.1:47}"
MON="$VMDIR/mon.sock"

mon() {  # comando no monitor do QEMU
  python3 - "$MON" "$1" <<'PY'
import socket, sys, time
s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); s.settimeout(2)
try: s.recv(4096)
except Exception: pass
s.sendall((sys.argv[2] + "\n").encode()); time.sleep(0.5)
try: print(s.recv(65536).decode(errors="replace").split("\n", 1)[-1].strip())
except Exception: pass
PY
}

# Ordem de boot: discos internos antes do CD (o OVMF descarta as entradas de boot que não
# estão na lista do QEMU, inclusive o "Windows Boot Manager" criado pelo bcdboot). Com os
# discos vazios, cai no CD; depois de instalado, inicia o Windows.
run_qemu() {
  qemu-system-x86_64 -name uabtest-win11 -rtc base=localtime -enable-kvm -machine q35 -cpu host -smp 4 -m 4096 \
    -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
    -drive if=pflash,format=raw,file="$VMDIR/vars.fd" \
    -drive id=cd,if=none,media=cdrom,readonly=on,file="$ISO" -device ide-cd,drive=cd,bus=ide.0,bootindex=${CD_BOOTINDEX:-3} \
    -drive id=d0,if=none,format=qcow2,file="$VMDIR/small.qcow2",discard=unmap,detect-zeroes=unmap \
    -device ide-hd,drive=d0,bus=ide.1,rotation_rate=1,serial=PEQUENO30,bootindex=2 \
    -drive id=d1,if=none,format=qcow2,file="$VMDIR/big.qcow2",discard=unmap,detect-zeroes=unmap \
    -device ide-hd,drive=d1,bus=ide.2,rotation_rate=1,serial=GRANDE64,bootindex=1 \
    -device qemu-xhci,id=xhci -drive id=usb,if=none,format=raw,file="$VMDIR/usb.img" \
    -device usb-storage,bus=xhci.0,drive=usb,removable=on -device usb-tablet,bus=xhci.0 \
    -netdev "user,id=n0,guestfwd=tcp:10.0.2.100:443-cmd:nc ${MESH_TLS/:/ },hostfwd=tcp:127.0.0.1:$SSH_PORT-:22" \
    -device e1000e,netdev=n0 \
    -display none -vnc "$VNC" -monitor "unix:$MON,server,nowait" -pidfile "$VMDIR/qemu.pid" -daemonize
  # "Pressione qualquer tecla para iniciar do CD/DVD..."
  for _ in $(seq 1 6); do sleep 1; mon "sendkey ret" >/dev/null 2>&1 || true; done
}

case "${1:-}" in
start)
  XML="$2"
  avail=$(df -BG --output=avail "$ROOT/work" | tail -1 | tr -dc 0-9)
  [ "$avail" -ge 30 ] || { echo "pouco espaço livre (${avail} GB; precisa de 30 para manter 10 de folga)"; exit 1; }
  mkdir -p "$VMDIR"
  cp /usr/share/OVMF/OVMF_VARS_4M.fd "$VMDIR/vars.fd"
  qemu-img create -q -f qcow2 "$VMDIR/small.qcow2" 30G
  qemu-img create -q -f qcow2 "$VMDIR/big.qcow2" 64G
  # "pendrive": FAT32 de 64 MB com o autounattend.xml na raiz
  rm -f "$VMDIR/usb.img"; truncate -s 64M "$VMDIR/usb.img"
  mkfs.fat -F 32 -n UABTEST "$VMDIR/usb.img" >/dev/null
  mkdir -p "$VMDIR/mnt"; mount -o loop "$VMDIR/usb.img" "$VMDIR/mnt"
  cp "$XML" "$VMDIR/mnt/autounattend.xml"; sync; umount "$VMDIR/mnt"; rmdir "$VMDIR/mnt"
  sha256sum "$VMDIR/usb.img" | cut -d' ' -f1 >"$VMDIR/usb.sha256"
  run_qemu
  echo "VM iniciada (pid $(cat "$VMDIR/qemu.pid"))"
  ;;
boot) run_qemu;  # CD_BOOTINDEX=0 vm_test.sh boot: inicia pelo CD de novo (teste do laço)
  [ "${CD_BOOTINDEX:-3}" = 0 ] && for _ in $(seq 1 6); do sleep 1; mon "sendkey ret" >/dev/null 2>&1 || true; done; echo "VM iniciada (pid $(cat "$VMDIR/qemu.pid"))" ;;
shot) mon "screendump $2" ;;
key) mon "sendkey $2" ;;
stop) [ -f "$VMDIR/qemu.pid" ] && kill "$(cat "$VMDIR/qemu.pid")" 2>/dev/null || true; rm -f "$VMDIR/qemu.pid" ;;
clean) "$0" stop; sleep 2; rm -rf "$VMDIR" ;;
*) echo "uso: $0 start <xml> | shot <ppm> | key <tecla> | stop | clean"; exit 2 ;;
esac
