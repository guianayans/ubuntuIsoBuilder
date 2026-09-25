#!/bin/bash
# Resumo do estado do servidor: provisionamento, rede, SSH, Tailscale, Docker.
# Uso: uab-status   (use sudo para ver todos os detalhes)
set -uo pipefail

G=$'\033[32m'; R=$'\033[31m'; Y=$'\033[33m'; N=$'\033[0m'
[ -t 1 ] || { G=""; R=""; Y=""; N=""; }
okf()  { printf '%s[OK]%s    %s\n' "$G" "$N" "$*"; }
bad()  { printf '%s[FALHA]%s %s\n' "$R" "$N" "$*"; }
note() { printf '%s[INFO]%s  %s\n' "$Y" "$N" "$*"; }
svc()  { systemctl is-active --quiet "$1"; }

[ -r /etc/uab/uab.conf ] && . /etc/uab/uab.conf
echo "Host: $(hostname)   $(. /etc/os-release; echo "$PRETTY_NAME")   uptime: $(uptime -p)"
[ -r /etc/uab/install-info ] && sed 's/^/  /' /etc/uab/install-info

echo; echo "== Provisionamento"
if [ -r /var/lib/uab/status ]; then sed 's/^/  /' /var/lib/uab/status; else note "sem status em /var/lib/uab"; fi
svc uab-provision && note "uab-provision ainda em execução (journalctl -u uab-provision -f)"

echo; echo "== Rede"
ip -brief address show scope global 2>/dev/null | sed 's/^/  /'
gw="$(ip route show default 2>/dev/null | awk '{print $3; exit}')"
[ -n "$gw" ] && okf "gateway padrão $gw" || bad "sem rota padrão"
getent hosts archive.ubuntu.com >/dev/null 2>&1 && okf "DNS funcionando" || bad "DNS não resolve"

echo; echo "== SSH"
if svc ssh.socket || svc ssh.service; then okf "SSH ativo"; else bad "SSH inativo"; fi
if systemctl is-enabled --quiet ssh.socket 2>/dev/null || systemctl is-enabled --quiet ssh.service 2>/dev/null; then okf "SSH habilitado no boot"; else bad "SSH não habilitado no boot"; fi
if [ "$(id -u)" = 0 ]; then
  install -d -m 0755 /run/sshd
  sshd -T 2>/dev/null | grep -Ei '^(permitrootlogin|passwordauthentication|pubkeyauthentication) ' | sed 's/^/  /'
fi
if [ -n "${UAB_ADMIN_USER:-}" ]; then
  h="$(getent passwd "$UAB_ADMIN_USER" | cut -d: -f6)"
  if [ -r "$h/.ssh/authorized_keys" ]; then okf "$(grep -cEv '^\s*(#|$)' "$h/.ssh/authorized_keys") chave(s) autorizada(s) para $UAB_ADMIN_USER"; fi
fi

echo; echo "== Tailscale"
if command -v tailscale >/dev/null 2>&1; then
  svc tailscaled && okf "tailscaled ativo" || bad "tailscaled inativo"
  systemctl is-enabled --quiet tailscaled && okf "tailscaled habilitado no boot" || bad "tailscaled não habilitado"
  ts_ip="$(tailscale ip -4 2>/dev/null | head -n1)"
  [ -n "$ts_ip" ] && okf "IP Tailscale: $ts_ip   nome: ${UAB_TAILSCALE_HOSTNAME:-$(hostname)}" || bad "sem IP Tailscale (tailscale status)"
  tailscale status 2>/dev/null | head -n 5 | sed 's/^/  /'
else
  note "Tailscale não instalado"
fi

if [ -n "${UAB_PANEL_URL:-}" ]; then
  echo; echo "== Painel"
  if [ -r /var/lib/uab/last-report ]; then okf "último relatório ao painel: $(cat /var/lib/uab/last-report) ($UAB_PANEL_URL)"
  else note "ainda sem relatório ao painel ($UAB_PANEL_URL); teste: sudo uab-report -v"; fi
fi

if systemctl list-unit-files cockpit.socket >/dev/null 2>&1 && command -v cockpit-bridge >/dev/null 2>&1; then
  echo; echo "== Cockpit"
  svc cockpit.socket && okf "Cockpit ativo: https://<ip>:9090" || bad "cockpit.socket inativo"
fi
if command -v docker >/dev/null 2>&1 && [ -d /data/coolify ]; then
  echo; echo "== Coolify"
  curl -fsS -o /dev/null --max-time 5 http://127.0.0.1:8000/api/health && okf "Coolify responde: http://<ip>:8000" || bad "Coolify não responde em :8000"
fi

echo; echo "== Docker"
if command -v docker >/dev/null 2>&1; then
  svc docker && okf "docker ativo" || bad "docker inativo"
  systemctl is-enabled --quiet docker && okf "docker habilitado no boot" || bad "docker não habilitado"
  docker compose version 2>/dev/null | sed 's/^/  /'
else
  note "Docker não instalado"
fi

if [ "$(id -u)" = 0 ] && [ -r /var/log/uab-provision.log ]; then
  echo; echo "== Últimas linhas de /var/log/uab-provision.log"
  tail -n 8 /var/log/uab-provision.log | sed 's/^/  /'
fi
