#!/bin/sh
# Entrada do container do ISO Builder.
# 1. Se houver Tailscale no container, sobe o tailscaled em modo userspace
#    (sem root/TUN) com proxy SOCKS5 local; o painel usa esse proxy para abrir
#    o terminal SSH nas máquinas da Tailnet.
# 2. Na primeira vez, faz login com TS_AUTHKEY (o estado fica em /data/tailscale).
# 3. Inicia a aplicação web.
set -eu

TS_DIR="${DATA_DIR:-/data}/tailscale"
TS_SOCKET="${TS_SOCKET:-$TS_DIR/tailscaled.sock}"
export TS_SOCKET

if command -v tailscaled >/dev/null 2>&1 && [ "${PANEL_TAILSCALE:-on}" != off ]; then
  mkdir -p "$TS_DIR"; chmod 700 "$TS_DIR"
  tailscaled --tun=userspace-networking --statedir="$TS_DIR" --socket="$TS_SOCKET" \
    --socks5-server="${PANEL_SOCKS:-127.0.0.1:1055}" >"$TS_DIR/tailscaled.log" 2>&1 &
  i=0; while [ ! -S "$TS_SOCKET" ] && [ $i -lt 50 ]; do sleep 0.2; i=$((i+1)); done
  state="$(tailscale --socket="$TS_SOCKET" status --json 2>/dev/null | python3 -c 'import json,sys; print(json.load(sys.stdin).get("BackendState",""))' 2>/dev/null || true)"
  if [ "$state" != Running ] && [ -n "${TS_AUTHKEY:-}" ]; then
    echo "painel: entrando na Tailnet como ${TS_HOSTNAME:-iso-painel}"
    # a chave é lida de arquivo temporário (não aparece em ps/logs)
    kf="$TS_DIR/.authkey"; umask 077; printf '%s' "$TS_AUTHKEY" >"$kf"
    tailscale --socket="$TS_SOCKET" up --auth-key="file:$kf" --hostname="${TS_HOSTNAME:-iso-painel}" \
      ${TS_EXTRA_ARGS:-} --timeout=60s || echo "painel: AVISO: login no Tailscale falhou (confira TS_AUTHKEY)"
    rm -f "$kf"
  fi
  unset TS_AUTHKEY
fi

exec uvicorn web.app.main:app --host 0.0.0.0 --port "${PORT:-8080}" --proxy-headers --forwarded-allow-ips='*' --no-server-header
