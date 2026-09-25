#!/bin/sh
# Remoto do ISO Builder (MeshCentral) em https://<painel>/remoto/.
# Independente do remote antigo: dados próprios, começa vazio.
#  - 1ª subida: cria o config.json (host público + chave de sessão) e a conta admin
#    (ADMIN_EMAIL / ADMIN_PASSWORD, as mesmas do painel);
#  - publica em /shared (volume só entre o painel e este container) a chave de login
#    (o painel abre o remoto já logado) e o id do grupo "Servidores ISO Builder"
#    (as ISOs instalam o agente nele).
set -eu

MC=/opt/meshcentral
DATA="$MC/meshcentral-data"
CONFIG="$DATA/config.json"
SHARED=/shared
PUBLIC_URL="${PUBLIC_URL:-https://iso.gvtserver.online}"
PUBLIC_HOST="$(printf '%s' "$PUBLIC_URL" | sed -E 's#^https?://##; s#/.*$##; s#:.*$##')"

mkdir -p "$DATA" "$SHARED"
if [ ! -f "$CONFIG" ]; then
  SK="${REMOTE_SESSION_KEY:-$(head -c 48 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 64)}"
  jq --arg sk "$SK" --arg host "$PUBLIC_HOST" --arg url "$PUBLIC_URL" \
     '.settings.SessionKey=$sk | .settings.Cert=$host | .domains.remoto.CertUrl=$url' /defaults/config.json >"$CONFIG"
  echo "[remoto] config.json criado para $PUBLIC_URL/remoto/"
fi

cd "$MC"
if [ ! -f "$DATA/.uab-admin" ] && [ -n "${ADMIN_PASSWORD:-}" ]; then
  node meshcentral/meshcentral --createaccount admin --pass "$ADMIN_PASSWORD" --email "${ADMIN_EMAIL:-admin@localhost}" --domain remoto >/dev/null 2>&1 || true
  node meshcentral/meshcentral --adminaccount admin --domain remoto >/dev/null 2>&1 || true
  touch "$DATA/.uab-admin"
  echo "[remoto] conta admin criada (usuário admin, senha = ADMIN_PASSWORD do painel)"
fi
# chave de login (a mesma a cada subida; gerada na 1ª)
KEY="$(node meshcentral/meshcentral --logintokenkey 2>/dev/null | tail -n1 | tr -dc '0-9a-fA-F')"
if [ ${#KEY} -ge 64 ]; then
  (umask 077; printf '%s\n' "$KEY" >"$SHARED/loginkey.tmp") && chmod 0640 "$SHARED/loginkey.tmp" && chgrp 1000 "$SHARED/loginkey.tmp" 2>/dev/null
  mv -f "$SHARED/loginkey.tmp" "$SHARED/loginkey"
fi
KEY=""

# depois que o servidor subir: garante o grupo de dispositivos das ISOs
(
  for i in $(seq 1 60); do
    sleep 5
    out="$(node meshcentral/meshctrl.js ListDeviceGroups --json --url ws://127.0.0.1:4021/remoto/ \
            --loginuser admin --loginkeyfile "$SHARED/loginkey" --logindomain remoto 2>/dev/null)" || continue
    id="$(printf '%s' "$out" | jq -r '.[] | select(.name=="Servidores ISO Builder") | ._id' 2>/dev/null | head -n1)"
    if [ -z "$id" ] || [ "$id" = null ]; then
      node meshcentral/meshctrl.js AddDeviceGroup --name "Servidores ISO Builder" --url ws://127.0.0.1:4021/remoto/ \
        --loginuser admin --loginkeyfile "$SHARED/loginkey" --logindomain remoto >/dev/null 2>&1 || continue
      continue
    fi
    printf '%s\n' "${id##*/}" >"$SHARED/meshid"  # como a tela do MeshCentral: só a última parte
    chmod 0644 "$SHARED/meshid"
    echo "[remoto] grupo \"Servidores ISO Builder\" pronto"
    exit 0
  done
) &

exec /bin/bash "$MC/entrypoint.sh"
