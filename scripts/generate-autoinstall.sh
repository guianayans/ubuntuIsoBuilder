#!/usr/bin/env bash
# Monta o diretório "overlay" que será adicionado à ISO:
#   <stage>/nocloud/{user-data,meta-data,vendor-data}
#   <stage>/uab/{preflight.sh,late-install.sh,BUILD_INFO}
#   <stage>/uab/payload/{uab-provision.sh,systemd/,server-tools/,uab.conf,authorized_keys}
#   <stage>/uab/secrets/tailscale-authkey   (somente se Tailscale ativo)
# Uso: scripts/generate-autoinstall.sh <stage> <build-id> [args extras para uab.py render]
#      UAB_NO_TS_KEY=1 omite a auth key (usado pelo test-iso.sh)
source "$(dirname "$0")/common.sh"
load_config

STAGE="${1:?stage}"; BUILD_ID="${2:?build-id}"; shift 2

rm -rf "$STAGE"
mkdir -p "$STAGE/nocloud" "$STAGE/uab/payload" "$STAGE/uab/secrets"
chmod 755 "$STAGE" "$STAGE/nocloud" "$STAGE/uab" "$STAGE/uab/payload"
chmod 700 "$STAGE/uab/secrets"

cp "$UAB_ROOT/payload/preflight.sh" "$UAB_ROOT/payload/late-install.sh" "$UAB_ROOT/payload/on-error.sh" "$STAGE/uab/"
cp -R "$UAB_ROOT/payload/systemd" "$STAGE/uab/payload/"
cp "$UAB_ROOT/payload/uab-provision.sh" "$UAB_ROOT/payload/uab-report.sh" "$UAB_ROOT/payload/uab-wg-apply.py" \
   "$UAB_ROOT/payload/README-SERVER.txt" "$STAGE/uab/payload/"
mkdir -p "$STAGE/uab/payload/server-tools"
cp "$UAB_ROOT"/server-tools/*.sh "$STAGE/uab/payload/server-tools/"
# No modo senha as chaves são opcionais: arquivo pode não existir.
if [ -f "$AUTHKEYS_FILE" ]; then cp "$AUTHKEYS_FILE" "$STAGE/uab/payload/authorized_keys"; else : >"$STAGE/uab/payload/authorized_keys"; fi
find "$STAGE/uab" -type d -exec chmod 755 {} + ; chmod 700 "$STAGE/uab/secrets"
find "$STAGE/uab" -type f -name '*.sh' -exec chmod 755 {} +
find "$STAGE/uab" -type f ! -name '*.sh' -exec chmod 644 {} +

# Painel de máquinas (ISOs geradas pela interface web): relatório + chave do painel.
PANEL_OVR=()
if [ -n "${UAB_PANEL_URL:-}" ] && [ -s "${UAB_PANEL_TOKEN_FILE:-}" ]; then
  install -m 600 "$UAB_PANEL_TOKEN_FILE" "$STAGE/uab/secrets/panel-token"
  PANEL_OVR=(--override "PANEL_URL=$UAB_PANEL_URL")
fi
if [ -s "${UAB_PANEL_KEY_FILE:-}" ]; then
  install -m 644 "$UAB_PANEL_KEY_FILE" "$STAGE/uab/payload/panel_key"
fi

python3 "$UAB_ROOT/scripts/lib/uab.py" render \
  --config "$CONFIG_FILE" \
  --password-hash "${UAB_PASSWORD_HASH_FILE:-$PASSWORD_HASH_FILE}" \
  --authorized-keys "$STAGE/uab/payload/authorized_keys" \
  --out-dir "$STAGE/nocloud" \
  --payload-conf "$STAGE/uab/payload/uab.conf" \
  --build-id "$BUILD_ID" ${PANEL_OVR[@]+"${PANEL_OVR[@]}"} "$@" >/dev/null
# user-data contém o hash da senha; o instalador lê como root.
chmod 600 "$STAGE/nocloud/user-data"

if [ "${COOLIFY_ENABLED:-no}" = yes ]; then
  [ -s "$COOLIFY_PW_FILE" ] || die "Senha do admin do Coolify ausente (secrets/coolify-password)."
  install -m 600 "$COOLIFY_PW_FILE" "$STAGE/uab/secrets/coolify-password"
fi

if [ "${TAILSCALE_ENABLED:-no}" = yes ] && [ "${UAB_NO_TS_KEY:-0}" != 1 ]; then
  [ -s "$TS_AUTHKEY_FILE" ] || die "Auth key do Tailscale ausente."
  install -m 600 "$TS_AUTHKEY_FILE" "$STAGE/uab/secrets/tailscale-authkey"
fi

{
  echo "build_id=$BUILD_ID"
  echo "hostname=$HOSTNAME_"
  echo "created=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "generator=ubuntu-autoinstall-builder"
} >"$STAGE/uab/BUILD_INFO"
chmod 644 "$STAGE/uab/BUILD_INFO"
ok "Autoinstall/NoCloud e payload gerados em ${STAGE#$UAB_ROOT/}"
