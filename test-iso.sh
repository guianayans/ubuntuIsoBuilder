#!/usr/bin/env bash
# Teste ponta a ponta em VM QEMU/KVM com disco VIRTUAL descartável (qcow2 em
# work/test/). Nunca usa discos físicos.
#
# Gera uma variante de TESTE da ISO a partir da sua configuração:
#   - disco alvo = disco virtual da VM (serial UABTESTDISK0001);
#   - rede DHCP (rede NAT do QEMU);
#   - hostname <hostname>-vmtest;
#   - chave SSH descartável extra (work/test/id_test) para o teste logar;
#   - console serial para registrar o progresso (work/test/serial.log);
#   - SEM a auth key do Tailscale (para não gastar/poluir sua Tailnet),
#     a menos que use --with-tailscale.
# A ISO é conectada como PENDRIVE USB (padrão) ou CD-ROM (--cdrom).
#
# Verifica: ISO inicia -> autoinstall roda -> Ubuntu instala -> reboot ->
# sistema inicia -> SSH por chave funciona -> senha recusada no SSH ->
# provisionamento concluído -> Docker/Tailscale -> reboot com o pendrive
# ainda conectado NÃO reinstala (proteção do GRUB).
#
# Uso: ./test-iso.sh [--bios] [--cdrom] [--with-tailscale] [--abort-test]
#                    [--ssh-password] [--ssh-port N] [--keep] [--yes] [--timeout MIN]
#   --stick-helper BIN  grava a ISO num "pendrive" (imagem de 8 GB) com o isoBuilder
#                 BIN (Linux), incluindo a partição INSTALAR, e dá boot por ele
#   --ventoy DIR  copia a ISO para um pendrive (imagem) com Ventoy (DIR = pasta do
#                 Ventoy para Linux, com Ventoy2Disk.sh) e dá boot pelo menu do Ventoy
#   --ssh-port N  instala com o SSH na porta N (padrão: SSH_PORT da config) e
#                 confere que a 22 fecha depois do provisionamento
#   --ssh-password  testa o modo "usuário e senha" (gera uma senha de teste
#                 aleatória só para a VM e faz login real por senha)
#   --abort-test  verifica que um critério de disco sem correspondência
#                 ABORTA a instalação sem escrever no disco.
source "$(dirname "$0")/scripts/common.sh"

FIRMWARE=uefi; MEDIA=usb; WITH_TS=0; ABORT_TEST=0; KEEP=0; YES=0; TIMEOUT_MIN=60; SSH_PW=0
while [ $# -gt 0 ]; do
  case "$1" in
    --bios) FIRMWARE=bios ;;
    --cdrom) MEDIA=cdrom ;;
    --with-tailscale) WITH_TS=1 ;;
    --ssh-password) SSH_PW=1 ;;
    --ssh-port) OVR_SSH_PORT="$2"; shift ;;
    --stick-helper) STICK_HELPER="$(readlink -f "$2")"; shift ;;
    --ventoy) VENTOY_DIR="$(readlink -f "$2")"; shift ;;
    --abort-test) ABORT_TEST=1 ;;
    --keep) KEEP=1 ;;
    --yes|-y) YES=1 ;;
    --timeout) TIMEOUT_MIN="$2"; shift ;;
    -h|--help) sed -n '2,26p' "$0"; exit 0 ;;
    *) die "argumento desconhecido: $1" ;;
  esac
  shift
done

"$UAB_ROOT/scripts/check-deps.sh" --test
load_config

TDIR="$WORK_DIR/test"
DISK="$TDIR/disk.qcow2"
TEST_ISO="$TDIR/test-autoinstall.iso"
SERIAL="$TDIR/serial.log"
MON="$TDIR/monitor.sock"
PIDF="$TDIR/qemu.pid"
KEY="$TDIR/id_test"
PORT="${UAB_TEST_SSH_PORT:-2222}"           # no host (127.0.0.1)
GUEST_PORT="${OVR_SSH_PORT:-${SSH_PORT:-22}}"  # porta do SSH dentro da VM
valid_ssh_port "$GUEST_PORT" || die "porta SSH inválida: $GUEST_PORT"
PORT22=$((PORT+1))                           # 22 da VM (deve fechar se GUEST_PORT != 22)
TEST_HOST="${HOSTNAME_:0:56}-vmtest"

cat >&2 <<EOF

Teste em máquina virtual
  firmware: $FIRMWARE   mídia: $MEDIA   modo: $([ "$ABORT_TEST" = 1 ] && echo "abort-test" || echo "instalação completa")
  disco virtual descartável: ${DISK#$UAB_ROOT/} (25 GB, qcow2; nenhum disco físico é usado)
  SSH da VM: 127.0.0.1:$PORT -> porta $GUEST_PORT da VM   Tailscale login: $([ "$WITH_TS" = 1 ] && echo "SIM (usa sua auth key!)" || echo "não")
  Duração típica com KVM: 5–15 min.
EOF
if [ "$WITH_TS" = 1 ]; then
  warn "--with-tailscale usa a auth key real: se ela for de uso único, será CONSUMIDA pela VM."
fi
if [ "$YES" != 1 ]; then ask_yn "Iniciar o teste?" S || exit 1; fi

if [ -f "$PIDF" ] && kill -0 "$(cat "$PIDF")" 2>/dev/null; then die "Já existe uma VM de teste rodando (pid $(cat "$PIDF"))."; fi
rm -rf "$TDIR"; mkdir -p "$TDIR"; chmod 700 "$TDIR"

stop_vm() {
  if [ -f "$PIDF" ] && kill -0 "$(cat "$PIDF")" 2>/dev/null; then
    printf 'quit\n' | monitor_cmd >/dev/null 2>&1 || true
    sleep 2; kill "$(cat "$PIDF")" 2>/dev/null || true
  fi
}
finish() {
  if [ "${UAB_TEST_HOLD:-0}" = 1 ] && alive; then  # depuração: deixa a VM ligada
    warn "VM mantida ligada (UAB_TEST_HOLD=1): ssh -p $PORT -i $KEY $ADMIN_USER@127.0.0.1; pare com: kill \$(cat $PIDF)"
    return
  fi
  stop_vm
  if [ "$KEEP" = 1 ]; then warn "Artefatos mantidos em ${TDIR#$UAB_ROOT/}"; else
    find "$TDIR" -maxdepth 1 -type f \( -name '*.iso' -o -name '*.img' -o -name '*.qcow2' -o -name '*.fd' -o -name 'id_test*' -o -name 'test-password*' -o -name askpass \) -delete 2>/dev/null
    rm -rf "$TDIR/tools"
    rm -rf "$TDIR/build"
  fi
}
trap finish EXIT

# Envia comandos (um por linha, pela entrada padrão) ao monitor do QEMU.
# Os comandos vão como ARGUMENTO: a entrada padrão do python3 é o próprio script.
monitor_cmd() {
  local cmds; cmds="$(cat)"
  python3 -c '
import socket, sys, time
s = socket.socket(socket.AF_UNIX); s.connect(sys.argv[1]); s.settimeout(2)
try: s.recv(4096)
except Exception: pass
for line in sys.argv[2].splitlines():
    s.sendall((line + "\n").encode()); time.sleep(0.5)
    try: print(s.recv(65536).decode(errors="replace"))
    except Exception: pass
' "$MON" "$cmds"
}

# ------------------------------------------------------------ ISO de teste
header "Gerando ISO de teste"
SRC_ISO="$("$UAB_ROOT/scripts/download-ubuntu.sh" "$UBUNTU_SERIES" | tail -n1)"
ssh-keygen -q -t ed25519 -N '' -C "uab-vmtest" -f "$KEY"
BUILD_ID="vmtest-$(date -u +%Y%m%d%H%M%S)"
STAGE="$TDIR/build/stage"
if [ "$ABORT_TEST" = 1 ]; then
  DISK_OVR=(--override STORAGE_MATCH_TYPE=serial --override STORAGE_MATCH_VALUE=NAO-EXISTE-UABTEST)
else
  DISK_OVR=(--override STORAGE_MATCH_TYPE=serial --override STORAGE_MATCH_VALUE=UABTESTDISK0001 --override STORAGE_MIN_GB=20)
fi
NO_TS=1; [ "$WITH_TS" = 1 ] && NO_TS=0
PW_OVR=(); PW_HASH_FILE="$PASSWORD_HASH_FILE"
if [ "$SSH_PW" = 1 ]; then
  # Senha de teste só desta VM (a ISO real carrega apenas o hash da sua senha).
  TEST_PW="Teste-$(od -An -N9 -tx1 /dev/urandom | tr -d ' \n')"
  printf '%s\n' "$TEST_PW" >"$TDIR/test-password"; chmod 600 "$TDIR/test-password"
  PWFILE_FOR_SUDO="$TDIR/test-password"
  printf '%s\n' "$TEST_PW" | openssl passwd -6 -stdin >"$TDIR/test-password.hash"
  PW_HASH_FILE="$TDIR/test-password.hash"
  PW_OVR=(--override SSH_AUTH=password)
  printf '#!/bin/sh\ncat "%s"\n' "$TDIR/test-password" >"$TDIR/askpass"; chmod 700 "$TDIR/askpass"
fi
UAB_PASSWORD_HASH_FILE="$PW_HASH_FILE" UAB_NO_TS_KEY=$NO_TS "$UAB_ROOT/scripts/generate-autoinstall.sh" "$STAGE" "$BUILD_ID" \
  --extra-keys "$KEY.pub" \
  --override "HOSTNAME_=$TEST_HOST" --override "TAILSCALE_HOSTNAME=$TEST_HOST" \
  --override NET_MODE=dhcp --override "SSH_PORT=$GUEST_PORT" "${DISK_OVR[@]}" ${PW_OVR[@]+"${PW_OVR[@]}"}
python3 "$UAB_ROOT/scripts/lib/uab.py" check "$STAGE/nocloud" --payload "$STAGE/uab/payload" >"$TDIR/check.txt" \
  || { cat "$TDIR/check.txt"; die "ISO de teste inválida."; }
HOSTNAME_="$TEST_HOST" "$UAB_ROOT/scripts/customize-iso.sh" "$SRC_ISO" "$STAGE" "$BUILD_ID" \
  --timeout 3 --extra-args "console=tty0 console=ttyS0,115200n8"
"$UAB_ROOT/scripts/build-iso.sh" "$SRC_ISO" "$STAGE" "$TEST_ISO"
"$UAB_ROOT/scripts/verify-iso.sh" "$TEST_ISO" "$SRC_ISO" >"$TDIR/verify.txt" || { grep -v '^OK' "$TDIR/verify.txt"; die "Verificação da ISO de teste falhou."; }
find "$STAGE" -path '*secrets*' -type f -exec sh -c 'command -v shred >/dev/null && shred -u "$@" || rm -f "$@"' _ {} +
ok "ISO de teste pronta."
MEDIA_FILE="$TEST_ISO"
if [ -n "${VENTOY_DIR:-}" ]; then
  header "Pendrive (imagem) com Ventoy"
  STICK="$TDIR/ventoy.img"; rm -f "$STICK"; truncate -s 8G "$STICK"
  LOOP="$(losetup -fP --show "$STICK")" || die "losetup falhou"
  VMNT="$(mktemp -d)"
  vt_cleanup() { umount "$VMNT" 2>/dev/null; rmdir "$VMNT" 2>/dev/null; losetup -d "$LOOP" 2>/dev/null; }
  (cd "$VENTOY_DIR" && printf 'y\ny\n' | sh Ventoy2Disk.sh -I "$LOOP" >/dev/null 2>&1) || { vt_cleanup; die "Ventoy2Disk falhou"; }
  udevadm settle 2>/dev/null || sleep 2
  mount "${LOOP}p1" "$VMNT" || { vt_cleanup; die "não montou a partição do Ventoy"; }
  mkdir -p "$VMNT/ISOs" "$VMNT/ventoy"
  cp "$TEST_ISO" "$VMNT/ISOs/test-autoinstall.iso"
  # escolhe a nossa ISO sozinho após 5 s (no uso real, a pessoa escolhe no menu)
  printf '{ "control": [ { "VTOY_DEFAULT_IMAGE": "/ISOs/test-autoinstall.iso" }, { "VTOY_MENU_TIMEOUT": "5" }, { "VTOY_SECONDARY_BOOT_MENU": "0" } ] }\n' >"$VMNT/ventoy/ventoy.json"
  sync; vt_cleanup
  ok "Ventoy $(cat "$VENTOY_DIR/ventoy/version" 2>/dev/null) instalado; ISO copiada para /ISOs"
  MEDIA_FILE="$STICK"
fi
if [ -n "${STICK_HELPER:-}" ]; then
  header "Gravando o pendrive (imagem) com o isoBuilder"
  STICK="$TDIR/stick.img"; rm -f "$STICK"; truncate -s 8G "$STICK"
  TOOLS="$TDIR/tools"; rm -rf "$TOOLS"; mkdir -p "$TOOLS"
  cp "$STICK_HELPER" "$TOOLS/instalar-linux"; echo teste >"$TOOLS/LEIA-ME.txt"
  printf '{"hostname":"%s","iso_name":"test-autoinstall.iso","created":"teste"}' "$TEST_HOST" >"$TOOLS/uab-stick.json"
  UAB_FAKE_DISK="$STICK" UAB_FAKE_DISK_SIZE=$((8*1024*1024*1024)) "$STICK_HELPER" --write --iso "$TEST_ISO" --disk fake0 --tools "$TOOLS" \
    || die "isoBuilder não gravou o pendrive"
  sgdisk -v "$STICK" 2>&1 | grep -q "No problems found" && ok "tabela GPT do pendrive íntegra (sgdisk -v)" || { sgdisk -v "$STICK"; die "GPT inválida"; }
  sgdisk -p "$STICK" | tail -n 5 >&2
  pn="$(sgdisk -p "$STICK" | awk '$NF=="INSTALAR"{print $1}')"
  [ -n "$pn" ] && ok "partição $pn: INSTALAR" || die "partição INSTALAR ausente"
  sgdisk -i 1 "$STICK" | grep -q "Linux filesystem" && ok "partição da ISO marcada como Linux (Windows/macOS não oferecem formatar)" || die "tipo da partição 1 inesperado"
  start="$(sgdisk -i "$pn" "$STICK" | awk '/First sector/{print $3}')"
  dd if="$STICK" of="$TDIR/p4.img" bs=512 skip="$start" count=$((64*2048)) status=none
  fsck.fat -n "$TDIR/p4.img" >/dev/null && ok "FAT32 da INSTALAR íntegro (fsck.fat)" || die "FAT32 inválido"
  7z l "$TDIR/p4.img" | grep -q "instalar-linux" && 7z l "$TDIR/p4.img" | grep -q "uab-stick.json" && ok "arquivos na INSTALAR" || die "arquivos ausentes na INSTALAR"
  cmp -n "$(stat -c %s "$TEST_ISO")" "$TEST_ISO" "$STICK" >/dev/null 2>&1 && warn "ISO idêntica byte a byte (esperado: só a tabela GPT muda)"
  rm -f "$TDIR/p4.img"; MEDIA_FILE="$STICK"
fi

# ------------------------------------------------------------ VM
qemu-img create -q -f qcow2 "$DISK" 25G
case "$DISK" in "$TDIR"/*) ;; *) die "Disco de teste fora de work/test — abortando por segurança." ;; esac

ACCEL=(-accel tcg)
if [ "$(uname -s)" = Darwin ]; then ACCEL=(-accel hvf)
elif [ -r /dev/kvm ] && [ -w /dev/kvm ]; then ACCEL=(-accel kvm -cpu host)
else warn "KVM indisponível: a emulação será MUITO lenta."; TIMEOUT_MIN=$((TIMEOUT_MIN*4)); fi

FW=()
if [ "$FIRMWARE" = uefi ]; then
  CODE=""; VARS=""
  for pair in /usr/share/OVMF/OVMF_CODE_4M.fd:/usr/share/OVMF/OVMF_VARS_4M.fd \
              /usr/share/OVMF/OVMF_CODE.fd:/usr/share/OVMF/OVMF_VARS.fd \
              /usr/share/edk2/ovmf/OVMF_CODE.fd:/usr/share/edk2/ovmf/OVMF_VARS.fd \
              /usr/share/edk2/x64/OVMF_CODE.4m.fd:/usr/share/edk2/x64/OVMF_VARS.4m.fd \
              /opt/homebrew/share/qemu/edk2-x86_64-code.fd:/opt/homebrew/share/qemu/edk2-i386-vars.fd \
              /usr/local/share/qemu/edk2-x86_64-code.fd:/usr/local/share/qemu/edk2-i386-vars.fd; do
    c="${pair%%:*}"; v="${pair##*:}"
    if [ -f "$c" ] && [ -f "$v" ]; then CODE="$c"; VARS="$v"; break; fi
  done
  [ -n "$CODE" ] || die "Firmware OVMF (UEFI) não encontrado. Instale 'ovmf' ou use --bios."
  cp "$VARS" "$TDIR/vars.fd"
  FW=(-drive "if=pflash,format=raw,readonly=on,file=$CODE" -drive "if=pflash,format=raw,file=$TDIR/vars.fd")
fi

if [ "$MEDIA" = usb ]; then
  RO=",readonly=on"; [ -n "${VENTOY_DIR:-}" ] && RO=""  # o Ventoy precisa do pendrive gravável (como um real)
  MEDIA_ARGS=(-device qemu-xhci,id=xhci -drive "if=none,id=stick,format=raw$RO,file=$MEDIA_FILE"
              -device usb-storage,bus=xhci.0,drive=stick,removable=on,bootindex=0)
else
  MEDIA_ARGS=(-drive "if=none,id=cd0,media=cdrom,readonly=on,file=$TEST_ISO" -device ide-cd,drive=cd0,bootindex=0)
fi

header "Iniciando VM"
qemu-system-x86_64 -machine q35 "${ACCEL[@]}" -m 4096 -smp 2 -name uab-vmtest \
  "${FW[@]}" "${MEDIA_ARGS[@]}" \
  -drive "if=none,id=d0,format=qcow2,file=$DISK" -device "virtio-blk-pci,drive=d0,serial=UABTESTDISK0001,bootindex=1" \
  -netdev "user,id=n0,hostfwd=tcp:127.0.0.1:$PORT-:$GUEST_PORT,hostfwd=tcp:127.0.0.1:$PORT22-:22" -device virtio-net-pci,netdev=n0 \
  -display none -serial "file:$SERIAL" -monitor "unix:$MON,server,nowait" \
  -pidfile "$PIDF" -daemonize
ok "VM iniciada (pid $(cat "$PIDF")). Log serial: ${SERIAL#$UAB_ROOT/}"

deadline=$(( $(date +%s) + TIMEOUT_MIN*60 ))
elapsed() { printf '%dm%02ds' $(( ($(date +%s)-START)/60 )) $(( ($(date +%s)-START)%60 )); }
START=$(date +%s)
alive() { [ -f "$PIDF" ] && kill -0 "$(cat "$PIDF")" 2>/dev/null; }
seen() { grep -a -q -- "$1" "$SERIAL" 2>/dev/null; }
SSH=(ssh -p "$PORT" -i "$KEY" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=5
     -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR "$ADMIN_USER@127.0.0.1")

PASS=0; FAILS=0
t_ok()   { ok "$*"; PASS=$((PASS+1)); }
t_fail() { fail "$*"; FAILS=$((FAILS+1)); }

wait_for() {  # descrição comando...
  local d="$1"; shift
  info "Aguardando: $d"
  while [ "$(date +%s)" -lt "$deadline" ]; do
    if "$@"; then t_ok "$d ($(elapsed))"; return 0; fi
    alive || { t_fail "VM encerrou inesperadamente"; return 1; }
    if [ "$ABORT_TEST" != 1 ] && seen "UAB: a instalacao automatica FALHOU"; then
      t_fail "$d — o INSTALADOR FALHOU (motivo em ${SERIAL#$UAB_ROOT/}):"
      sed 's/\x1b\[[0-9;?]*[a-zA-Z]//g' "$SERIAL" | tr -s '\r' '\n' | grep -a -E "Error|error:|CalledProcessError|UAB-PREFLIGHT: ERRO" | tail -n 4 | cut -c1-240 >&2
      deadline=0; return 1
    fi
    sleep 10
  done
  t_fail "$d — tempo esgotado"; return 1
}

if [ "$ABORT_TEST" = 1 ]; then
  wait_for "preflight aborta com critério inexistente" seen "UAB-PREFLIGHT: NENHUM DISCO FOI MODIFICADO" || true
  sleep 20
  stop_vm
  alloc="$(qemu-img info --output=json "$DISK" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("actual-size",0))')"
  used="$(qemu-img map --output=json "$DISK" | python3 -c 'import json,sys;print(sum(e["length"] for e in json.load(sys.stdin) if e.get("data")))')"
  [ "$used" = 0 ] && t_ok "disco virtual intacto (0 bytes escritos)" || t_fail "disco virtual recebeu $used bytes (alocado $alloc)"
else
  wait_for "ISO iniciou (kernel do instalador)" seen "Linux version" || true
  wait_for "autoinstall iniciou (preflight do disco)" seen "UAB-PREFLIGHT: DISCO DE DESTINO" || true
  wait_for "instalação concluída (late-commands)" seen "UAB-LATE: provisionador instalado" || true
  wait_for "sistema instalado iniciou e SSH aceita a chave" "${SSH[@]}" true || true
  wait_for "provisionamento concluído" sh -c "\"\$@\" 'grep -q ^provisioned=ok /var/lib/uab/status' 2>/dev/null" _ "${SSH[@]}" || true

  if "${SSH[@]}" true 2>/dev/null; then
    [ "$("${SSH[@]}" hostname)" = "$TEST_HOST" ] && t_ok "hostname = $TEST_HOST" || t_fail "hostname incorreto"
    "${SSH[@]}" 'systemctl is-active --quiet ssh.socket || systemctl is-active --quiet ssh.service' && t_ok "SSH ativo" || t_fail "SSH inativo"
    "${SSH[@]}" 'systemctl is-enabled --quiet ssh.socket || systemctl is-enabled --quiet ssh.service' && t_ok "SSH habilitado no boot" || t_fail "SSH não habilitado no boot"
    if [ "$GUEST_PORT" != 22 ]; then
      t_ok "SSH responde na porta escolhida ($GUEST_PORT)"
      ssh -p "$PORT22" -o BatchMode=yes -o ConnectTimeout=5 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
        "$ADMIN_USER@127.0.0.1" true >"$TDIR/port22.txt" 2>&1 && t_fail "porta 22 ainda aceita SSH" \
        || { grep -qiE "refused|closed|reset|timed out|kex_exchange" "$TDIR/port22.txt" && t_ok "porta 22 fechada" || t_fail "porta 22: $(head -c 200 "$TDIR/port22.txt")"; }
      "${SSH[@]}" "ss -Hltn | awk '{print \$4}' | grep -Eq '[:.]$GUEST_PORT\$' && ! ss -Hltn | awk '{print \$4}' | grep -Eq '[:.]22\$'" \
        && t_ok "sshd escuta só na $GUEST_PORT" || t_fail "portas de escuta inesperadas: $("${SSH[@]}" "ss -Hltn | awk '{print \$4}' | tr '\n' ' '")"
      "${SSH[@]}" "grep -qx 'port = $GUEST_PORT' /etc/fail2ban/jail.d/uab-sshd-port.conf" && t_ok "fail2ban vigia a porta $GUEST_PORT" || t_fail "fail2ban sem a porta $GUEST_PORT"
    fi
    methods="$(ssh -p "$PORT" -o PubkeyAuthentication=no -o PreferredAuthentications=password,keyboard-interactive \
      -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 \
      "$ADMIN_USER@127.0.0.1" true 2>&1 || true)"
    if [ "$SSH_PW" = 1 ]; then
      printf '%s\n' "$methods" | grep -q 'Permission denied (publickey,password)' && t_ok "SSH aceita senha da rede local" || t_fail "SSH não oferece senha: $methods"
      # login REAL por senha (sem chave), via SSH_ASKPASS
      PWSSH=(env SSH_ASKPASS="$TDIR/askpass" SSH_ASKPASS_REQUIRE=force DISPLAY=none setsid -w ssh -p "$PORT"
             -o PubkeyAuthentication=no -o PreferredAuthentications=password -o NumberOfPasswordPrompts=1
             -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10 -o LogLevel=ERROR "$ADMIN_USER@127.0.0.1")
      [ "$("${PWSSH[@]}" whoami </dev/null 2>/dev/null)" = "$ADMIN_USER" ] && t_ok "login por usuário e senha funciona" || t_fail "login por senha falhou"
      "${PWSSH[@]}" "sudo -S -p '' true <<< \"\$(cat)\"" <"$TDIR/test-password" >/dev/null 2>&1 \
        && t_ok "sudo com a mesma senha funciona (permissão de root)" || t_fail "sudo com senha falhou"
      "${SSH[@]}" "sudo -S -p '' sshd -T -C user=$ADMIN_USER,host=x,addr=203.0.113.10" <"$TDIR/test-password" 2>/dev/null | grep -qi '^passwordauthentication no' \
        && t_ok "senha recusada para IP público (só rede local/Tailscale)" || t_fail "senha liberada para IP público"
      "${SSH[@]}" "sudo -S -p '' sshd -T -C user=root,host=x,addr=192.168.1.9" <"$TDIR/test-password" 2>/dev/null | grep -qi '^permitrootlogin no' \
        && t_ok "login direto como root bloqueado" || t_fail "root pode logar"
      "${SSH[@]}" 'systemctl is-active --quiet fail2ban && systemctl is-enabled --quiet fail2ban' && t_ok "fail2ban ativo e habilitado" || t_fail "fail2ban inativo"
    else
      printf '%s\n' "$methods" | grep -q 'Permission denied (publickey)' && t_ok "SSH recusa senha (somente publickey)" || t_fail "SSH ainda anuncia autenticação por senha: $methods"
    fi
    "${SSH[@]}" "id -nG | grep -qw sudo" && t_ok "usuário no grupo sudo" || t_fail "usuário sem sudo"
    "${SSH[@]}" "test \"\$(cat /etc/timezone 2>/dev/null || timedatectl show -p Timezone --value)\" = '$TIMEZONE'" && t_ok "timezone $TIMEZONE" || t_fail "timezone incorreta"
    "${SSH[@]}" "grep -q 'LANG=$LOCALE' /etc/default/locale" && t_ok "locale $LOCALE" || t_fail "locale incorreto"
    "${SSH[@]}" "grep -q 'XKBLAYOUT=\"$KEYBOARD_LAYOUT\"' /etc/default/keyboard" && t_ok "teclado $KEYBOARD_LAYOUT" || t_fail "teclado incorreto"
    missing=""
    for p in curl wget git ca-certificates openssh-server htop unzip rsync ${EXTRA_PACKAGES:-}; do
      "${SSH[@]}" "dpkg -s $p >/dev/null 2>&1" || missing="$missing $p"
    done
    [ -z "$missing" ] && t_ok "pacotes instalados (base + extras)" || t_fail "pacotes ausentes:$missing"
    "${SSH[@]}" 'grep -qs "Acquire::Retries" /etc/apt/apt.conf.d/94curtin-config' && t_ok "apt com novas tentativas (falhas momentâneas do espelho)" || t_fail "configuração de novas tentativas do apt ausente"
    if [ "${DOCKER_ENABLED:-no}" = yes ]; then
      "${SSH[@]}" 'systemctl is-active --quiet docker && systemctl is-enabled --quiet docker' && t_ok "Docker ativo e habilitado" || t_fail "Docker inativo"
      "${SSH[@]}" 'docker compose version' >/dev/null 2>&1 && t_ok "Docker Compose + usuário no grupo docker" || t_fail "docker compose indisponível para o usuário"
    fi
    if [ "${COCKPIT_ENABLED:-no}" = yes ]; then
      "${SSH[@]}" 'systemctl is-active --quiet cockpit.socket && systemctl is-enabled --quiet cockpit.socket' && t_ok "Cockpit ativo e habilitado" || t_fail "Cockpit inativo"
      n="$("${SSH[@]}" 'dpkg -l "cockpit*" 2>/dev/null | grep -c "^ii"')"
      [ "${n:-0}" -ge 9 ] && t_ok "Cockpit completo: $n pacotes cockpit-*" || t_fail "Cockpit incompleto ($n pacotes)"
      "${SSH[@]}" 'curl -ks -o /dev/null -w "%{http_code}" https://127.0.0.1:9090/' | grep -q 200 && t_ok "Cockpit responde em :9090" || t_fail "Cockpit não responde em :9090"
    fi
    if [ "${COOLIFY_ENABLED:-no}" = yes ]; then
      "${SSH[@]}" 'curl -fsS --max-time 10 http://127.0.0.1:8000/api/health' >/dev/null 2>&1 && t_ok "Coolify responde em :8000" || t_fail "Coolify não responde em :8000"
      "${SSH[@]}" 'test ! -e /etc/uab/coolify-password' && t_ok "senha do Coolify apagada do servidor" || t_fail "senha do Coolify ainda no disco"
      "${SSH[@]}" "curl -s -o /dev/null -w '%{redirect_url}' http://127.0.0.1:8000/register" | grep -q /login \
        && t_ok "Coolify: admin criado na instalação e cadastro público fechado" || t_fail "Coolify com cadastro público aberto (admin não criado)"
      "${SSH[@]}" 'docker network inspect coolify -f "{{(index .IPAM.Config 0).Subnet}}"' 2>/dev/null | grep -q '^172\.30\.' \
        && t_ok "rede do Coolify na faixa isolada 172.30.0.0/16" || t_fail "rede do Coolify fora da faixa esperada"
      if [ "$GUEST_PORT" != 22 ]; then
        "${SSH[@]}" "docker exec coolify-db psql -U coolify -d coolify -tAc 'select port from servers where id=0'" 2>/dev/null | grep -qx "$GUEST_PORT" \
          && t_ok "Coolify gerencia o servidor pela porta $GUEST_PORT" || t_fail "Coolify ainda usa outra porta SSH"
      fi
      if [ -n "${PWFILE_FOR_SUDO:-}" ]; then
        "${SSH[@]}" "sudo -S -p '' sshd -T -C user=root,host=x,addr=192.168.1.9" <"$PWFILE_FOR_SUDO" 2>/dev/null | grep -qi '^permitrootlogin no' \
          && t_ok "root bloqueado vindo da rede local" || t_fail "root liberado na rede local"
      fi
    fi
    if [ "${TAILSCALE_ENABLED:-no}" = yes ]; then
      "${SSH[@]}" 'systemctl is-active --quiet tailscaled && systemctl is-enabled --quiet tailscaled' && t_ok "tailscaled ativo e habilitado" || t_fail "tailscaled inativo"
      if [ "$WITH_TS" = 1 ]; then
        "${SSH[@]}" 'tailscale ip -4' >/dev/null 2>&1 && t_ok "Tailscale conectado: $("${SSH[@]}" 'tailscale ip -4' | head -n1)" || t_fail "Tailscale não conectou"
        "${SSH[@]}" 'test ! -e /etc/uab/tailscale-authkey' && t_ok "auth key apagada do servidor" || t_fail "auth key ainda no disco"
      fi
    fi
    # ---- extras
    SUDO="sudo -S -p ''"; [ -n "${PWFILE_FOR_SUDO:-}" ] || SUDO="sudo -n"
    rsh() { "${SSH[@]}" "$SUDO sh -c $(printf '%q' "$1")" <"${PWFILE_FOR_SUDO:-/dev/null}"; }
    extras_diag() {
      rsh 'cat /proc/cmdline; grep -E "uab-provision: .*(ajustes|update-grub|pool|ponte|DDNS|Cloudflare|GPU)" /var/log/uab-provision.log | tail -n 20
        echo "--- grub.d"; for f in /etc/default/grub.d/*.cfg; do echo "# $f"; cat "$f"; done; grep -E "^GRUB_CMDLINE" /etc/default/grub
        echo "--- grub.cfg"; ls -l /boot/grub/grub.cfg /boot/efi/EFI/ubuntu/grub.cfg 2>&1; cat /boot/efi/EFI/ubuntu/grub.cfg 2>/dev/null; grep -m3 -E "^\s*linux\s" /boot/grub/grub.cfg
        findmnt /boot; ls -la /boot/.uab-installed* / 2>/dev/null | grep uab; echo "--- pools"; virsh pool-list --all --autostart' >"$TDIR/extras-diag.txt" 2>&1 || true
    }
    extras_diag
    if [ "${DDNS_ENABLED:-no}" = yes ]; then
      "${SSH[@]}" 'systemctl is-active --quiet ddclient && systemctl is-enabled --quiet ddclient' && t_ok "DDNS: ddclient ativo e habilitado" || t_fail "DDNS: ddclient inativo"
      [ "$("${SSH[@]}" 'stat -c %a:%U /etc/ddclient.conf')" = "600:root" ] && t_ok "DDNS: config protegida (600 root)" || t_fail "DDNS: permissão errada em /etc/ddclient.conf"
      "${SSH[@]}" 'test ! -e /etc/uab/ddns-password' && t_ok "DDNS: cópia da senha apagada de /etc/uab" || t_fail "DDNS: senha ainda em /etc/uab"
    fi
    if [ "${CLOUDFLARE_ENABLED:-no}" = yes ]; then
      "${SSH[@]}" 'command -v cloudflared >/dev/null && systemctl is-enabled --quiet cloudflared' && t_ok "Cloudflare: cloudflared instalado e habilitado" || t_fail "Cloudflare: cloudflared ausente"
      [ "$(rsh 'stat -c %a:%U /etc/cloudflared/token.env')" = "600:root" ] && t_ok "Cloudflare: token protegido (600 root)" || t_fail "Cloudflare: permissão errada no token"
      "${SSH[@]}" 'ps -eo args | grep -v grep | grep -q -- "--token"' && t_fail "Cloudflare: token visível na linha de comando" || t_ok "Cloudflare: token fora da linha de comando"
      "${SSH[@]}" 'test ! -e /etc/uab/cloudflare-token' && t_ok "Cloudflare: cópia do token apagada de /etc/uab" || t_fail "Cloudflare: token ainda em /etc/uab"
    fi
    if [ "${VIRT_ENABLED:-no}" = yes ]; then
      "${SSH[@]}" 'systemctl is-active --quiet libvirtd && virsh -c qemu:///system list --all >/dev/null' && t_ok "Virtualização: libvirt ativo (usuário no grupo libvirt)" || t_fail "Virtualização: libvirt indisponível"
      [ "$(rsh 'virsh pool-list --autostart --name' | tr -d ' \r' | grep -cxE 'default|isos')" = 2 ] && t_ok "Virtualização: pools default e isos (autostart)" || t_fail "Virtualização: pools ausentes: $(rsh 'virsh pool-list --all --autostart' | tr '\n' ' ')"
      if [ "${VIRT_BRIDGE:-no}" = yes ]; then
        "${SSH[@]}" 'ip route show default | grep -q " dev br0" && ip -4 addr show br0 | grep -q inet' && t_ok "Ponte br0 com IP e rota padrão" || t_fail "Ponte br0 não ficou ativa"
        "${SSH[@]}" 'virsh -c qemu:///system net-list --name | grep -qx br0' && t_ok "Rede br0 no libvirt (VMs na rede local)" || t_fail "rede br0 ausente no libvirt"
      fi
    fi
    if [ "${GPU_NVIDIA:-no}" = yes ]; then
      "${SSH[@]}" 'grep -q "^gpu_info=" /var/lib/uab/status && grep "^gpu_info=" /var/lib/uab/status' >"$TDIR/gpu.txt" 2>&1 \
        && t_ok "GPU: $(cut -d= -f2- "$TDIR/gpu.txt")" || t_fail "GPU: etapa não registrou resultado"
    fi
    if [ "${REMOTE_AGENT:-no}" = yes ]; then
      "${SSH[@]}" 'systemctl is-active --quiet meshagent && systemctl is-enabled --quiet meshagent' && t_ok "Remoto: agente MeshCentral ativo e habilitado" || t_fail "Remoto: agente inativo"
      "${SSH[@]}" 'test ! -e /etc/uab/remote-meshid' && t_ok "Remoto: id do grupo apagado de /etc/uab" || t_fail "Remoto: id do grupo ainda em /etc/uab"
    fi
    if [ "${SERVER_TWEAKS:-no}" = yes ]; then
      rsh 'grep -q consoleblank=0 /boot/grub/grub.cfg' && t_ok "Ajustes de servidor no GRUB" || { t_fail "ajustes de servidor ausentes do GRUB"; cat "$TDIR/extras-diag.txt" >&2; }
    fi
    "${SSH[@]}" 'uab-status' >"$TDIR/uab-status.txt" 2>&1 && t_ok "uab-status executa" || t_fail "uab-status falhou"

    runs_before="$(grep -a -c 'UAB-PREFLIGHT: critério do disco' "$SERIAL")"
    header "Reboot com o pendrive ainda conectado"
    boot_before="$("${SSH[@]}" cat /proc/sys/kernel/random/boot_id 2>/dev/null)"
    # reboot NORMAL (como o usuário faria); se não houver como, reset forçado
    rsh 'systemctl reboot' >/dev/null 2>&1 || printf 'system_reset\n' | monitor_cmd >/dev/null
    sleep 30
    deadline=$(( $(date +%s) + 15*60 ))
    wait_for "após reboot: sistema instalado volta (sem reinstalar) e SSH responde" "${SSH[@]}" 'test -f /var/lib/uab/ssh_keys.done' || true
    if "${SSH[@]}" true 2>/dev/null; then
      boot_after="$("${SSH[@]}" cat /proc/sys/kernel/random/boot_id 2>/dev/null)"
      if [ -n "$boot_before" ] && [ -n "$boot_after" ] && [ "$boot_before" != "$boot_after" ]; then t_ok "a VM reiniciou de fato (boot_id mudou)"
      else t_fail "a VM NÃO reiniciou (boot_id igual): as verificações abaixo não valem"; fi
      "${SSH[@]}" 'systemctl is-active --quiet ssh.socket || systemctl is-active --quiet ssh.service' && t_ok "SSH ativo após reboot" || t_fail "SSH inativo após reboot"
      [ "${DOCKER_ENABLED:-no}" = yes ] && { "${SSH[@]}" 'systemctl is-active --quiet docker' && t_ok "Docker ativo após reboot" || t_fail "Docker inativo após reboot"; }
      [ "${TAILSCALE_ENABLED:-no}" = yes ] && { "${SSH[@]}" 'systemctl is-active --quiet tailscaled' && t_ok "tailscaled ativo após reboot" || t_fail "tailscaled inativo após reboot"; }
      "${SSH[@]}" 'getent hosts archive.ubuntu.com >/dev/null && ip route show default | grep -q .' && t_ok "rede e DNS ok após reboot" || t_fail "rede quebrada após reboot"
      if [ "${VIRT_BRIDGE:-no}" = yes ] && [ "${VIRT_ENABLED:-no}" = yes ]; then
        "${SSH[@]}" 'ip route show default | grep -q " dev br0"' && t_ok "ponte br0 volta após reboot" || t_fail "ponte br0 não voltou após reboot"
      fi
      [ "${SERVER_TWEAKS:-no}" = yes ] && { "${SSH[@]}" 'grep -q consoleblank=0 /proc/cmdline' && t_ok "ajustes de boot ativos após reboot" || { t_fail "ajustes de boot não ativos"; SUDO="sudo -S -p ''"; extras_diag; cat "$TDIR/extras-diag.txt" >&2; }; }
      [ "${DDNS_ENABLED:-no}" = yes ] && { "${SSH[@]}" 'systemctl is-active --quiet ddclient' && t_ok "ddclient ativo após reboot" || t_fail "ddclient inativo após reboot"; }
      [ "${COCKPIT_ENABLED:-no}" = yes ] && { "${SSH[@]}" 'systemctl is-active --quiet cockpit.socket' && t_ok "Cockpit ativo após reboot" || t_fail "Cockpit inativo após reboot"; }
      if [ "${COOLIFY_ENABLED:-no}" = yes ]; then
        ok_c=0; for _ in $(seq 1 36); do "${SSH[@]}" 'curl -fsS --max-time 5 http://127.0.0.1:8000/api/health' >/dev/null 2>&1 && { ok_c=1; break; }; sleep 10; done
        if [ "$ok_c" = 1 ]; then t_ok "Coolify volta sozinho após reboot"; else
          t_fail "Coolify não voltou após reboot (6 min)"
          rsh 'uptime; systemctl is-active docker; docker ps -a --format "{{.Names}} {{.Status}}"; for c in coolify coolify-db coolify-redis coolify-proxy; do echo "--- $c"; docker inspect -f "restart={{.HostConfig.RestartPolicy.Name}} exit={{.State.ExitCode}} err={{.State.Error}}" $c; docker logs --tail 15 $c 2>&1; done; echo "--- docker journal"; journalctl -b -u docker --no-pager | tail -n 25' >&2 || true
        fi
      fi
      runs_after="$(grep -a -c 'UAB-PREFLIGHT: critério do disco' "$SERIAL")"
      [ "$runs_after" = "$runs_before" ] && t_ok "instalador NÃO rodou de novo (proteção anti-reinstalação)" || t_fail "instalador rodou novamente após o reboot"
    fi
  fi
fi

echo
if [ "$FAILS" = 0 ]; then ok "TESTE APROVADO: $PASS verificações."; else fail "TESTE COM FALHAS: $FAILS falha(s), $PASS ok. Veja ${SERIAL#$UAB_ROOT/}"; fi
[ "$FAILS" = 0 ]
