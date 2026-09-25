#!/usr/bin/env bash
# Valida config/install.env, chaves e segredos ANTES de gerar qualquer coisa.
# Uso: scripts/validate-config.sh [--quiet]
source "$(dirname "$0")/common.sh"
load_config

errs=0
e() { fail "$*"; errs=$((errs+1)); }
q=0; [ "${1:-}" = --quiet ] && q=1
k() { [ "$q" = 1 ] || ok "$*"; }

case "${UBUNTU_SERIES:-}" in 24.04|26.04) k "Ubuntu $UBUNTU_SERIES LTS" ;; *) e "UBUNTU_SERIES inválida: ${UBUNTU_SERIES:-}" ;; esac
is_valid_hostname "${HOSTNAME_:-}" && k "hostname ${HOSTNAME_}" || e "hostname inválido: ${HOSTNAME_:-}"
is_valid_username "${ADMIN_USER:-}" && k "usuário ${ADMIN_USER}" || e "usuário inválido: ${ADMIN_USER:-}"
printf '%s\n' "${TIMEZONE:-}" | grep -Eq '^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$' && k "timezone $TIMEZONE" || e "timezone inválida"
printf '%s\n' "${LOCALE:-}" | grep -Eq '^[a-z]{2,3}(_[A-Z]{2})?\.UTF-8$|^C\.UTF-8$' && k "locale $LOCALE" || e "locale inválido"
printf '%s\n' "${KEYBOARD_LAYOUT:-}" | grep -Eq '^[a-z]{2,8}$' && k "teclado $KEYBOARD_LAYOUT" || e "layout de teclado inválido"

# Senha
if [ -s "$PASSWORD_HASH_FILE" ]; then
  grep -Eq '^\$6\$' "$PASSWORD_HASH_FILE" && k "hash de senha SHA-512 presente" || e "secrets/password.hash não é SHA-512"
  perm="$(ls -l "$PASSWORD_HASH_FILE" | cut -c1-10)"
  [ "$perm" = "-rw-------" ] || e "secrets/password.hash deve ter permissão 600 (atual $perm)"
else
  e "secrets/password.hash ausente (rode ./setup.sh)"
fi

# Rede
case "${NET_MODE:-}" in
  dhcp) k "rede DHCP" ;;
  static)
    python3 - "$NET_ADDRESS" "$NET_GATEWAY" "$NET_DNS" <<'EOF' && k "rede estática $NET_ADDRESS gw $NET_GATEWAY" || e "configuração de IP estático inválida"
import ipaddress, sys
i = ipaddress.ip_interface(sys.argv[1]); g = ipaddress.ip_address(sys.argv[2])
assert g in i.network and i.ip != g
[ipaddress.ip_address(x) for x in sys.argv[3].split(",") if x]
EOF
    case "${NET_MATCH_TYPE:-}" in mac|name|glob) ;; *) e "NET_MATCH_TYPE inválido" ;; esac ;;
  *) e "NET_MODE inválido: ${NET_MODE:-}" ;;
esac

# Storage
case "${STORAGE_MATCH_TYPE:-}" in
  serial|model|path|single) k "disco: $STORAGE_MATCH_TYPE=${STORAGE_MATCH_VALUE:-} layout ${STORAGE_LAYOUT:-}" ;;
  *) e "STORAGE_MATCH_TYPE inválido: ${STORAGE_MATCH_TYPE:-}" ;;
esac
case "${STORAGE_LAYOUT:-}" in lvm|direct) ;; *) e "STORAGE_LAYOUT inválido" ;; esac
if [ "${STORAGE_CONFIRMED:-}" = "$(storage_confirm_token)" ]; then
  k "apagamento do disco confirmado com APAGAR"
else
  e "apagamento do disco NÃO confirmado para o critério atual (rode ./setup.sh e digite APAGAR)"
fi

# SSH
nkeys=0
[ -f "$AUTHKEYS_FILE" ] && nkeys="$(grep -cEv '^[[:space:]]*(#|$)' "$AUTHKEYS_FILE" || true)"
case "${SSH_AUTH:-key}" in
  password)
    k "SSH por usuário e senha (senha só da rede local/Tailscale; fail2ban)"
    if [ "$nkeys" -gt 0 ]; then
      validate_pubkey_file "$AUTHKEYS_FILE" 2>/dev/null && k "$nkeys chave(s) pública(s) SSH adicional(is)" \
        || { validate_pubkey_file "$AUTHKEYS_FILE" || true; e "config/authorized_keys inválido"; }
    fi ;;
  key)
    if validate_pubkey_file "$AUTHKEYS_FILE" 2>/dev/null; then
      k "$nkeys chave(s) pública(s) SSH válida(s)"
    else
      validate_pubkey_file "$AUTHKEYS_FILE" || true
      e "config/authorized_keys inválido ou ausente"
    fi ;;
  *) e "SSH_AUTH inválido: ${SSH_AUTH:-} (use key ou password)" ;;
esac

# Tailscale
if [ "${TAILSCALE_ENABLED:-no}" = yes ]; then
  is_valid_hostname "${TAILSCALE_HOSTNAME:-}" && k "Tailscale como ${TAILSCALE_HOSTNAME}" || e "TAILSCALE_HOSTNAME inválido"
  if [ -s "$TS_AUTHKEY_FILE" ]; then
    grep -Eq '^tskey-(auth-)?[A-Za-z0-9_-]{10,}$' "$TS_AUTHKEY_FILE" && k "auth key Tailscale $(mask_secret "$(cat "$TS_AUTHKEY_FILE")")" || e "auth key com formato inválido"
    perm="$(ls -l "$TS_AUTHKEY_FILE" | cut -c1-10)"
    [ "$perm" = "-rw-------" ] || e "secrets/tailscale-authkey deve ter permissão 600 (atual $perm)"
  else
    [ "$q" = 1 ] || warn "auth key do Tailscale ausente (será solicitada no build)"
  fi
fi

if [ "${COOLIFY_ENABLED:-no}" = yes ]; then
  if [ -s "$COOLIFY_PW_FILE" ]; then
    perm="$(ls -l "$COOLIFY_PW_FILE" | cut -c1-10)"
    [ "$perm" = "-rw-------" ] || e "secrets/coolify-password deve ter permissão 600 (atual $perm)"
    k "Coolify$( [ "${COCKPIT_ENABLED:-no}" = yes ] && echo " + Cockpit completo") (admin ${COOLIFY_ADMIN_EMAIL:-?})"
  else
    e "Coolify ativado, mas secrets/coolify-password ausente (rode ./setup.sh)"
  fi
fi

for p in ${EXTRA_PACKAGES:-}; do printf '%s\n' "$p" | grep -Eq '^[a-z0-9][a-z0-9+.-]+$' || e "pacote inválido: $p"; done

# Nenhuma chave privada dentro do projeto (exceto a chave descartável de teste em work/).
priv="$(grep -rlE -- '-----BEGIN ([A-Z]+ )?PRIVATE KEY-----' "$UAB_STATE/config" "$SECRETS_DIR" "$UAB_ROOT/templates" "$UAB_ROOT/payload" "$UAB_ROOT/server-tools" 2>/dev/null || true)"
[ -z "$priv" ] && k "nenhuma chave privada no projeto" || e "chave PRIVADA encontrada em: $priv"

[ "$errs" = 0 ] || die "$errs problema(s) na configuração."
exit 0
