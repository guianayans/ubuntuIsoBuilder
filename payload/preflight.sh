#!/bin/sh
# ubuntu-autoinstall-builder — preflight do disco (autoinstall early-commands).
#
# Executa no ambiente do instalador ANTES de qualquer escrita em disco.
# Resolve o critério configurado para EXATAMENTE UM disco, que não pode ser o
# pendrive de instalação e precisa ter o tamanho mínimo. Em qualquer dúvida,
# sai com erro: o Subiquity aborta a instalação sem tocar em nenhum disco.
# Em caso de sucesso, fixa o disco resolvido em /autoinstall.yaml
# (match: {path: /dev/...}), que o Subiquity relê após os early-commands.
set -u

UAB_DIR="${UAB_DIR:-/cdrom/uab}"
CONF="$UAB_DIR/payload/uab.conf"
AUTOINSTALL_YAML="${AUTOINSTALL_YAML:-/autoinstall.yaml}"
LOG="${UAB_PREFLIGHT_LOG:-/var/log/uab-preflight.log}"
RESULT="${UAB_RESULT_FILE:-/run/uab-target-disk}"

log() {
  echo "UAB-PREFLIGHT: $*" >>"$LOG"
  echo "UAB-PREFLIGHT: $*" >&2
  [ -z "${UAB_NO_CONSOLE:-}" ] && [ -w /dev/console ] && echo "UAB-PREFLIGHT: $*" >/dev/console 2>/dev/null
  return 0
}

abort() {
  log "ERRO: $*"
  log "NENHUM DISCO FOI MODIFICADO. A instalação foi interrompida por segurança."
  log "Discos detectados:"
  lsblk -d -e 7,11 -o NAME,SIZE,TYPE,TRAN,RM,MODEL,SERIAL 2>&1 | while IFS= read -r l; do log "  $l"; done
  log "Corrija a configuração (./setup.sh) e gere a ISO novamente."
  exit 1
}

[ -r "$CONF" ] || abort "configuração $CONF não encontrada na mídia"
# shellcheck disable=SC1090
. "$CONF"

command -v udevadm >/dev/null 2>&1 && udevadm settle --timeout=30 2>/dev/null

MTYPE="${UAB_STORAGE_MATCH_TYPE:-}"
MVAL="${UAB_STORAGE_MATCH_VALUE:-}"
MIN_GB="${UAB_STORAGE_MIN_GB:-8}"
log "critério do disco: $MTYPE=$MVAL (mínimo ${MIN_GB} GB)"

# ---- discos que pertencem à mídia de instalação (nunca podem ser alvo)
disk_of() {
  # Recebe /dev/X (partição ou disco) e imprime o disco inteiro /dev/Y
  d="$(readlink -f "$1" 2>/dev/null || echo "$1")"
  p="$(lsblk -dno PKNAME "$d" 2>/dev/null | head -n1)"
  if [ -n "$p" ]; then echo "/dev/$p"; else echo "$d"; fi
}
PROTECTED=""
for mp in /cdrom /isodevice /run/live/medium /media/cdrom; do
  # --mountpoint exige correspondência exata (--target subiria até "/").
  src="$(findmnt -n -o SOURCE --mountpoint "$mp" 2>/dev/null | head -n1)"
  case "$src" in
    /dev/loop*)
      back="$(losetup -n -O BACK-FILE "$src" 2>/dev/null | head -n1)"
      if [ -n "$back" ]; then
        bsrc="$(findmnt -n -o SOURCE --target "$back" 2>/dev/null | head -n1)"
        case "$bsrc" in /dev/*) PROTECTED="$PROTECTED $(disk_of "$bsrc")" ;; esac
      fi ;;
    /dev/*) PROTECTED="$PROTECTED $(disk_of "$src")" ;;
  esac
done
PROTECTED="$(printf '%s\n' $PROTECTED | awk 'NF && !seen[$0]++' | tr '\n' ' ')"
log "mídia de instalação (protegida): ${PROTECTED:-não identificada}"
is_protected() { for p in $PROTECTED; do [ "$p" = "$1" ] && return 0; done; return 1; }

prop() { udevadm info --query=property --name="$1" 2>/dev/null | sed -n "s/^$2=//p" | head -n1; }
# USB = transporte usb no lsblk, ID_BUS=usb ou caminho do dispositivo sob um
# controlador USB (pontes USB-SATA reportam ID_BUS=ata).
is_usb() {
  [ "$(lsblk -dno TRAN "$1" 2>/dev/null | tr -d ' ')" = usb ] && return 0
  [ "$(prop "$1" ID_BUS)" = usb ] && return 0
  case "$(prop "$1" DEVPATH)$(prop "$1" ID_PATH)" in *usb*) return 0 ;; esac
  return 1
}

# ---- candidatos: discos reais (sem loop, cdrom, ram, zram, md)
CANDIDATES=""
for d in $(lsblk -dpno NAME,TYPE 2>/dev/null | awk '$2=="disk"{print $1}'); do
  case "$d" in /dev/loop*|/dev/sr*|/dev/ram*|/dev/zram*|/dev/md*|/dev/fd*) continue ;; esac
  is_protected "$d" && continue
  CANDIDATES="$CANDIDATES $d"
done
[ -n "$CANDIDATES" ] || abort "nenhum disco elegível encontrado"

MATCHES=""
case "$MTYPE" in
  path)
    want="$(readlink -f "$MVAL" 2>/dev/null || echo "$MVAL")"
    [ -b "$want" ] || abort "o dispositivo $MVAL não existe nesta máquina"
    is_protected "$want" && abort "$MVAL é o PENDRIVE de instalação, não o disco interno"
    for d in $CANDIDATES; do [ "$d" = "$want" ] && MATCHES="$d"; done ;;
  serial)
    pat="$MVAL"; case "$pat" in *'*'*) ;; *) pat="*$pat*" ;; esac
    for d in $CANDIDATES; do
      s1="$(prop "$d" ID_SERIAL)"; s2="$(prop "$d" ID_SERIAL_SHORT)"
      # shellcheck disable=SC2254
      case "$s1" in $pat) MATCHES="$MATCHES $d"; continue ;; esac
      # shellcheck disable=SC2254
      case "$s2" in $pat) MATCHES="$MATCHES $d" ;; esac
    done ;;
  model)
    # ID_MODEL usa espaços em NVMe e '_' em SATA: compara normalizado.
    pat="$(printf '%s' "$MVAL" | tr ' ' '_')"
    for d in $CANDIDATES; do
      m="$(prop "$d" ID_MODEL | tr ' ' '_')"
      # shellcheck disable=SC2254
      case "$m" in $pat) MATCHES="$MATCHES $d" ;; esac
    done ;;
  single|largest)
    for d in $CANDIDATES; do
      name="$(basename "$d")"
      rm_flag="$(cat "/sys/block/$name/removable" 2>/dev/null || echo 0)"
      [ "$rm_flag" = 1 ] && continue
      is_usb "$d" && continue
      MATCHES="$MATCHES $d"
    done ;;
  *) abort "tipo de critério desconhecido: $MTYPE" ;;
esac

set -- $MATCHES
[ "$#" -eq 0 ] && abort "nenhum disco corresponde a $MTYPE=$MVAL"
[ "$#" -gt 1 ] && abort "mais de um disco corresponde a $MTYPE=$MVAL ($*); critério ambíguo"
TARGET="$1"

# Discos USB (HD externo de backup, outro pendrive) são recusados por padrão.
if is_usb "$TARGET" && [ "${UAB_STORAGE_ALLOW_USB:-no}" != yes ]; then
  abort "$TARGET é um disco USB (externo). Por segurança, destino USB exige STORAGE_ALLOW_USB='yes'"
fi

bytes="$(lsblk -dbno SIZE "$TARGET" 2>/dev/null | head -n1)"
gb=$(( ${bytes:-0} / 1000000000 ))
[ "$gb" -ge "$MIN_GB" ] || abort "$TARGET tem ${gb} GB, abaixo do mínimo configurado de ${MIN_GB} GB"

log "DISCO DE DESTINO: $TARGET  ${gb} GB  modelo=$(prop "$TARGET" ID_MODEL)  serial=$(prop "$TARGET" ID_SERIAL)"
printf '%s\n' "$TARGET" >"$RESULT"

# ---- fixa o disco resolvido na configuração do autoinstall
# Sem conseguir reescrever, só é seguro manter o match original quando ele é
# equivalente ao que foi verificado aqui (path/serial).
keep_ok() { case "$MTYPE" in path|serial) return 0 ;; esac; return 1; }
if [ -f "$AUTOINSTALL_YAML" ]; then
  PY=""
  for c in python3 /usr/bin/python3 /snap/subiquity/current/usr/bin/python3; do
    if command -v "$c" >/dev/null 2>&1 && "$c" -c 'import yaml' 2>/dev/null; then PY="$c"; break; fi
  done
  if [ -n "$PY" ]; then
    if "$PY" - "$AUTOINSTALL_YAML" "$TARGET" <<'EOF'
import sys, yaml
path, target = sys.argv[1], sys.argv[2]
with open(path) as fh:
    doc = yaml.safe_load(fh)
ai = doc["autoinstall"] if isinstance(doc, dict) and "autoinstall" in doc else doc
ai["storage"]["layout"]["match"] = {"path": target}
with open(path + ".uab", "w") as fh:
    yaml.safe_dump(doc, fh, sort_keys=False)
EOF
    then
      mv -f "$AUTOINSTALL_YAML.uab" "$AUTOINSTALL_YAML"
      log "autoinstall.yaml atualizado: storage.layout.match = {path: $TARGET}"
    else
      rm -f "$AUTOINSTALL_YAML.uab"
      keep_ok || abort "não foi possível fixar o disco em $AUTOINSTALL_YAML"
      log "aviso: não foi possível reescrever $AUTOINSTALL_YAML; mantendo o match original (equivalente)"
    fi
  else
    keep_ok || abort "python3/yaml indisponível para fixar o disco em $AUTOINSTALL_YAML"
    log "aviso: python3/yaml indisponível; mantendo o match original (já verificado como único)"
  fi
fi
exit 0
