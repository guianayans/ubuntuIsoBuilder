#!/bin/sh
# ubuntu-autoinstall-builder — error-commands: exibe o motivo da falha no console.
{
  echo
  echo "=================================================================="
  echo " UAB: a instalacao automatica FALHOU. Nada mais sera feito."
  echo " Logs: /var/log/installer/  e  /var/log/uab-preflight.log"
  echo " Console de diagnostico: Ctrl+Alt+F2 (ou menu Help > Enter shell)."
  echo "=================================================================="
  [ -f /var/log/uab-preflight.log ] && tail -n 20 /var/log/uab-preflight.log
  tail -n 30 /var/log/installer/subiquity-server-debug.log 2>/dev/null
} >/dev/console 2>&1
exit 0
