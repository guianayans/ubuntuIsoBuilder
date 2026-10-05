#!/bin/sh
# uab-progress.sh — reporta o andamento da instalação SEM PENDRIVE (kexec) ao painel, para o
# card de "Formatação à distância" mostrar os passos (checkboxes) durante o instalador (subiquity),
# quando a tela da máquina pode estar sem vídeo (headless). Roda no ambiente do instalador.
#
# Uso: uab-progress.sh begin|end|fail
#   begin  -> estado "instalando" + heartbeat em segundo plano (mantém o card vivo).
#   end    -> estado "reiniciando" (instalação terminou, vai reiniciar).
#   fail   -> estado "erro" (a instalação falhou).
#
# A URL de report é derivada do /proc/cmdline (url=.../f/<token>/image.iso) — só existe no modo
# SEM PENDRIVE. TUDO é best-effort: NUNCA falha nem aborta a instalação (sempre sai 0). Os hooks
# no user-data chamam este script DESTACADO (setsid &) para não bloquear nem derrubar o subiquity.
set +e
PIDF=/run/uab-progress.pid

py() {  # acha um python3 utilizável (inclusive o do snap do subiquity)
  for c in python3 /usr/bin/python3 /snap/subiquity/current/usr/bin/python3; do
    command -v "$c" >/dev/null 2>&1 && { echo "$c"; return 0; }
    [ -x "$c" ] && { echo "$c"; return 0; }
  done
  return 1
}

# report <estado> <mensagem> [percent]
report() {
  P="$(py)" || return 0
  "$P" - "$1" "$2" "${3:--1}" <<'PY' >/dev/null 2>&1 || true
import sys, re, json, urllib.request
try:
    cmd = open('/proc/cmdline').read()
except Exception:
    sys.exit(0)
m = re.search(r'url=(\S+?)/image\.iso', cmd)
if not m:
    sys.exit(0)
rep = m.group(1) + '/report'
state = sys.argv[1] if len(sys.argv) > 1 else 'instalando'
msg = sys.argv[2] if len(sys.argv) > 2 else ''
try:
    pct = int(sys.argv[3])
except Exception:
    pct = -1
data = json.dumps({'state': state, 'message': msg, 'percent': pct}).encode()
req = urllib.request.Request(rep, data=data, headers={'Content-Type': 'application/json'})
try:
    urllib.request.urlopen(req, timeout=10).read()
except Exception:
    pass
PY
}

case "${1:-}" in
  begin)
    report instalando "Instalando o sistema no disco..." -1
    # heartbeat: reafirma "instalando" a cada 20s (mantém o card vivo até o end/fail).
    ( while true; do sleep 20; report instalando "Instalando o sistema no disco..." -1; done ) >/dev/null 2>&1 &
    echo $! >"$PIDF" 2>/dev/null
    ;;
  end)
    if [ -f "$PIDF" ]; then kill "$(cat "$PIDF" 2>/dev/null)" >/dev/null 2>&1; rm -f "$PIDF" 2>/dev/null; fi
    report reiniciando "Instalacao concluida no disco; reiniciando. A maquina volta ao painel em instantes." 100
    ;;
  fail)
    if [ -f "$PIDF" ]; then kill "$(cat "$PIDF" 2>/dev/null)" >/dev/null 2>&1; rm -f "$PIDF" 2>/dev/null; fi
    report erro "A instalacao falhou no disco. Veja a tela/logs da maquina." -1
    ;;
esac
exit 0
