#!/usr/bin/env bash
# Ponto de entrada: assistente interativo + geração da ISO.
# Uso: ./setup.sh
source "$(dirname "$0")/scripts/common.sh"

cat >&2 <<'EOF'

  ubuntu-autoinstall-builder
  Gera uma ISO do Ubuntu Server LTS que instala sozinha e deixa o servidor
  acessível por SSH + Tailscale após o primeiro boot.

  Nada é gravado em discos desta máquina: o resultado é um arquivo em output/.

EOF

"$UAB_ROOT/scripts/check-deps.sh" --install
"$UAB_ROOT/scripts/collect-config.sh"

load_config
if [ "${STORAGE_CONFIRMED:-}" != "$(storage_confirm_token)" ]; then
  warn "Apagamento do disco não confirmado: a ISO NÃO será gerada."
  warn "Rode ./setup.sh novamente e digite APAGAR quando solicitado."
  exit 1
fi

if ask_yn "Gerar a ISO agora?" S; then
  exec "$UAB_ROOT/build.sh"
fi
info "Quando quiser gerar: ./build.sh"
