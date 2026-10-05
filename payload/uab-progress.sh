#!/bin/sh
# uab-progress.sh — reporta o andamento da instalação SEM PENDRIVE (kexec) ao painel, para o
# card de "Formatação à distância" mostrar o progresso durante o instalador (subiquity), quando
# a tela da máquina pode estar sem vídeo. Roda no ambiente do instalador (early/late-commands).
#
# Uso: uab-progress.sh begin|end
#   begin  -> avisa que a instalação começou e sobe um heartbeat em segundo plano (mantém o card
#             "vivo" e mostrando "instalando" até o late-commands).
#   end    -> encerra o heartbeat e avisa que está finalizando (vai reiniciar).
#
# A URL de report é derivada do /proc/cmdline (url=.../f/<token>/image.iso) — só existe no modo
# SEM PENDRIVE. TUDO é best-effort: este script NUNCA falha nem aborta a instalação (sempre sai 0).
set +e
PIDF=/run/uab-progress.pid

# acha um python3 utilizável (inclusive o do snap do subiquity), como o preflight.sh faz
py() {
  for c in python3 /usr/bin/python3 /snap/subiquity/current/usr/bin/python3; do
    command -v "$c" >/dev/null 2>&1 && { echo "$c"; return 0; }
    [ -x "$c" ] && { echo "$c"; return 0; }
  done
  return 1
}

# report <mensagem> <percent> [state]: POST {state, message, percent} para .../f/<token>/report
report() {
  P="$(py)" || return 0
  "$P" - "$1" "$2" "${3:-instalando}" <<'PY' >/dev/null 2>&1 || true
import sys, re, json, urllib.request
try:
    cmd = open('/proc/cmdline').read()
except Exception:
    sys.exit(0)
m = re.search(r'url=(\S+?)/image\.iso', cmd)
if not m:
    sys.exit(0)
rep = m.group(1) + '/report'
msg = sys.argv[1] if len(sys.argv) > 1 else 'Instalando...'
try:
    pct = int(sys.argv[2])
except Exception:
    pct = -1
state = sys.argv[3] if len(sys.argv) > 3 and sys.argv[3] else 'instalando'
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
    report "Instalando o Ubuntu no disco..." -1
    # heartbeat: a cada 20 s reafirma "instalando" (mantém o card vivo até o late-commands).
    # Se o ambiente do instalador matar este processo, não tem problema: ainda restam as
    # mensagens de begin/end e o "concluído" quando a máquina voltar.
    ( while true; do sleep 20; report "Instalando o Ubuntu no disco..." -1; done ) >/dev/null 2>&1 &
    echo $! >"$PIDF" 2>/dev/null
    ;;
  end)
    if [ -f "$PIDF" ]; then kill "$(cat "$PIDF" 2>/dev/null)" >/dev/null 2>&1; rm -f "$PIDF" 2>/dev/null; fi
    report "Finalizando e preparando o primeiro boot; a maquina vai reiniciar..." 99
    ;;
  fail)
    if [ -f "$PIDF" ]; then kill "$(cat "$PIDF" 2>/dev/null)" >/dev/null 2>&1; rm -f "$PIDF" 2>/dev/null; fi
    report "A instalacao falhou. Veja o motivo na tela da maquina (se houver video)." -1 erro
    ;;
esac
exit 0
