#!/usr/bin/env bash
# Pergunta a senha administrativa, gera hash SHA-512 crypt ($6$) e grava
# SOMENTE o hash em secrets/password.hash (chmod 600). A senha em texto puro
# nunca é gravada em disco nem passada como argumento de linha de comando.
source "$(dirname "$0")/common.sh"

hash_password() {
  # Lê a senha do stdin. Tenta métodos compatíveis com crypt(3) SHA-512.
  local pw h=""
  IFS= read -r pw
  if command -v openssl >/dev/null 2>&1; then
    h="$(printf '%s\n' "$pw" | openssl passwd -6 -stdin 2>/dev/null || true)"
  fi
  if [ -z "$h" ] && command -v mkpasswd >/dev/null 2>&1; then
    h="$(printf '%s\n' "$pw" | mkpasswd -m sha-512 -s 2>/dev/null || true)"
  fi
  if [ -z "$h" ]; then
    h="$(printf '%s' "$pw" | python3 -c 'import sys
try:
    from passlib.hash import sha512_crypt
    print(sha512_crypt.using(rounds=5000).hash(sys.stdin.read()))
except Exception:
    import crypt
    print(crypt.crypt(sys.stdin.read(), crypt.mksalt(crypt.METHOD_SHA512)))' 2>/dev/null || true)"
  fi
  pw=""
  printf '%s' "$h"
}

mkdir -p "$SECRETS_DIR"; chmod 700 "$SECRETS_DIR"

if [ "${UAB_MIN_PW:-10}" -ge 12 ]; then
  echo "A senha é usada para: login SSH (rede local/Tailscale), sudo e console físico."
else
  echo "A senha é usada para: sudo, login no console físico (recuperação) e"
  echo "login SSH por senha APENAS até a chave SSH ser confirmada no servidor."
fi
echo "Ela não será salva; somente o hash SHA-512."
while true; do
  MIN="${UAB_MIN_PW:-10}"
  ask_secret PW1 "Senha do usuário administrativo (mín. $MIN caracteres)"
  if [ "${#PW1}" -lt "$MIN" ]; then warn "Senha muito curta."; PW1=""; continue; fi
  ask_secret PW2 "Confirme a senha"
  if [ "$PW1" != "$PW2" ]; then warn "As senhas não conferem."; PW1=""; PW2=""; continue; fi
  break
done
PW2=""
HASH="$(printf '%s\n' "$PW1" | hash_password)"
PW1=""
unset PW1 PW2

case "$HASH" in
  '$6$'*) ;;
  *) die "Não foi possível gerar hash SHA-512 (instale openssl >= 1.1.1 ou 'mkpasswd' do pacote whois)." ;;
esac
printf '%s\n' "$HASH" | write_private_file "$PASSWORD_HASH_FILE"
HASH=""
ok "Hash da senha gravado em secrets/password.hash (600)."
