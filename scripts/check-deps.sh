#!/usr/bin/env bash
# Detecta dependências do builder e oferece instalá-las.
# Uso: scripts/check-deps.sh [--install] [--test]
source "$(dirname "$0")/common.sh"

WANT_INSTALL=0; WANT_TEST=0
for a in "$@"; do
  case "$a" in --install) WANT_INSTALL=1 ;; --test) WANT_TEST=1 ;; esac
done

missing_cmds=""
check_cmd() { command -v "$1" >/dev/null 2>&1 || missing_cmds="$missing_cmds $1"; }

check_cmd xorriso
check_cmd python3
check_cmd curl
check_cmd openssl
check_cmd ssh-keygen
check_cmd gpg
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then missing_cmds="$missing_cmds sha256sum"; fi
if [ "$WANT_TEST" = 1 ]; then check_cmd qemu-system-x86_64; check_cmd qemu-img; fi

missing_py=""
if command -v python3 >/dev/null 2>&1; then
  python3 -c 'import yaml' 2>/dev/null || missing_py="$missing_py pyyaml"
fi
jsonschema_ok=1
python3 -c 'import jsonschema' 2>/dev/null || jsonschema_ok=0

if [ -z "$missing_cmds$missing_py" ]; then
  ok "Dependências obrigatórias presentes."
  [ "$jsonschema_ok" = 1 ] || warn "Opcional: python3-jsonschema ausente (validação pelo schema oficial do Subiquity será pulada)."
  exit 0
fi

warn "Dependências ausentes:${missing_cmds}${missing_py}"

pkgs_for() {
  local kind="$1" out="" c
  for c in $missing_cmds $missing_py; do
    case "$kind:$c" in
      debian:ssh-keygen) out="$out openssh-client" ;;
      debian:sha256sum)  out="$out coreutils" ;;
      debian:qemu-system-x86_64) out="$out qemu-system-x86 ovmf" ;;
      debian:qemu-img)   out="$out qemu-utils" ;;
      debian:pyyaml)     out="$out python3-yaml" ;;
      debian:*)          out="$out $c" ;;
      fedora:ssh-keygen) out="$out openssh-clients" ;;
      fedora:qemu-system-x86_64) out="$out qemu-system-x86 edk2-ovmf" ;;
      fedora:qemu-img)   out="$out qemu-img" ;;
      fedora:pyyaml)     out="$out python3-pyyaml" ;;
      fedora:gpg)        out="$out gnupg2" ;;
      fedora:*)          out="$out $c" ;;
      macos:gpg)         out="$out gnupg" ;;
      macos:qemu-system-x86_64|macos:qemu-img) out="$out qemu" ;;
      macos:pyyaml)      out="$out pyyaml" ;;
      macos:ssh-keygen|macos:sha256sum|macos:curl) ;;
      macos:*)           out="$out $c" ;;
    esac
  done
  printf '%s' "$out" | tr ' ' '\n' | awk 'NF && !seen[$0]++' | tr '\n' ' '
}

kind="$(os_kind)"
pkgs="$(pkgs_for "$kind")"
case "$kind" in
  debian) cmd="sudo apt-get update && sudo apt-get install -y $pkgs" ;;
  fedora) cmd="sudo dnf install -y $pkgs" ;;
  macos)  cmd="brew install $pkgs" ;;
  *)      cmd="" ;;
esac

if [ -z "$cmd" ]; then
  die "Instale manualmente:${missing_cmds}${missing_py}"
fi
info "Comando sugerido: $cmd"
if [ "$WANT_INSTALL" = 1 ] && [ -t 0 ] && ask_yn "Instalar agora?" S; then
  if [ "$kind" = debian ] && [ "$(id -u)" = 0 ]; then cmd="$(printf '%s' "$cmd" | sed 's/sudo //g')"; fi
  if [ "$kind" = macos ] && [ -n "$missing_py" ]; then
    # PyYAML via pip do usuário no macOS
    cmd="$(printf '%s' "$cmd" | sed 's/ pyyaml//') && python3 -m pip install --user pyyaml"
  fi
  sh -c "$cmd" || die "Falha ao instalar dependências."
  exec "$0" "$@"
fi
die "Dependências ausentes. Execute o comando sugerido acima e tente novamente."
