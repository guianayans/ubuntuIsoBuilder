#!/bin/sh
# ubuntu-autoinstall-builder — late-commands.
# Executa no ambiente do instalador com o sistema novo montado em /target.
# Instala o provisionador de primeiro boot, as ferramentas de servidor, a
# auth key do Tailscale (0600, apagada após o uso) e o marcador que impede a
# ISO de reinstalar a máquina caso ela dê boot pelo pendrive novamente.
set -eu

SRC="${UAB_DIR:-/cdrom/uab}"
T="${TARGET_MOUNT:-/target}"
log() { echo "UAB-LATE: $*"; [ -w /dev/console ] && echo "UAB-LATE: $*" >/dev/console 2>/dev/null; return 0; }

# shellcheck disable=SC1091
. "$SRC/payload/uab.conf"

install -d -m 0755 "$T/opt/uab" "$T/etc/uab" "$T/var/lib/uab" "$T/usr/local/sbin"
cp -R "$SRC/payload/." "$T/opt/uab/"
chmod -R go-w "$T/opt/uab"
install -m 0644 "$SRC/payload/uab.conf" "$T/etc/uab/uab.conf"
install -m 0644 "$SRC/payload/authorized_keys" "$T/etc/uab/authorized_keys"
install -m 0755 "$SRC/payload/uab-provision.sh" "$T/usr/local/sbin/uab-provision"
install -m 0644 "$SRC/payload/systemd/uab-provision.service" "$T/etc/systemd/system/uab-provision.service"
install -m 0755 "$SRC/payload/uab-report.sh" "$T/usr/local/sbin/uab-report"
install -m 0755 "$SRC/payload/uab-wg-apply.py" "$T/usr/local/sbin/uab-wg-apply"
install -m 0644 "$SRC/payload/systemd/uab-report.service" "$T/etc/systemd/system/uab-report.service"
install -m 0644 "$SRC/payload/systemd/uab-report.timer" "$T/etc/systemd/system/uab-report.timer"
install -m 0644 "$SRC/payload/systemd/uab-report-shutdown.service" "$T/etc/systemd/system/uab-report-shutdown.service"
[ -f "$SRC/payload/panel_key" ] && install -m 0644 "$SRC/payload/panel_key" "$T/etc/uab/panel_key"
for f in "$SRC"/payload/server-tools/*.sh; do
  [ -f "$f" ] || continue
  n="$(basename "$f" .sh)"
  install -m 0755 "$f" "$T/usr/local/sbin/uab-$n"
done

if [ -f "$SRC/secrets/panel-token" ]; then
  (umask 077; install -m 0600 -o root -g root "$SRC/secrets/panel-token" "$T/etc/uab/panel-token")
  log "token de relatório ao painel instalado"
fi
if [ -f "$SRC/secrets/coolify-password" ]; then
  (umask 077; install -m 0600 -o root -g root "$SRC/secrets/coolify-password" "$T/etc/uab/coolify-password")
  log "senha do admin do Coolify instalada em /etc/uab (0600, apagada após a instalação do Coolify)"
fi
for sf in ddns-password cloudflare-token remote-meshid tsec-token; do
  if [ -f "$SRC/secrets/$sf" ]; then
    (umask 077; install -m 0600 -o root -g root "$SRC/secrets/$sf" "$T/etc/uab/$sf")
    log "$sf instalado em /etc/uab (0600, apagado após o uso)"
  fi
done
if [ -f "$SRC/secrets/tailscale-authkey" ]; then
  umask 077
  install -m 0600 -o root -g root "$SRC/secrets/tailscale-authkey" "$T/etc/uab/tailscale-authkey"
  log "auth key do Tailscale instalada em /etc/uab (0600, será apagada após o login)"
fi

# Habilita o provisionador (symlink direto; funciona sem systemd em execução).
install -d "$T/etc/systemd/system/multi-user.target.wants"
ln -sf /etc/systemd/system/uab-provision.service "$T/etc/systemd/system/multi-user.target.wants/uab-provision.service"
install -d "$T/etc/systemd/system/timers.target.wants"
ln -sf /etc/systemd/system/uab-report.timer "$T/etc/systemd/system/timers.target.wants/uab-report.timer"
ln -sf /etc/systemd/system/uab-report-shutdown.service "$T/etc/systemd/system/multi-user.target.wants/uab-report-shutdown.service"

# SSH precisa subir sozinho após o reboot (socket ou serviço, conforme a versão).
if command -v curtin >/dev/null 2>&1; then
  curtin in-target --target="$T" -- sh -c 'systemctl enable ssh.socket 2>/dev/null || systemctl enable ssh.service' || log "aviso: não foi possível habilitar ssh via curtin"
else
  chroot "$T" sh -c 'systemctl enable ssh.socket 2>/dev/null || systemctl enable ssh.service' || log "aviso: não foi possível habilitar ssh"
fi

# Informações da instalação (sem segredos)
{
  echo "UAB_BUILD_ID=$UAB_BUILD_ID"
  echo "UAB_INSTALLED_AT=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo "UAB_TARGET_DISK=$(cat /run/uab-target-disk 2>/dev/null || echo desconhecido)"
} >"$T/etc/uab/install-info"

# Marcador anti-reinstalação lido pelo GRUB da ISO (fica em /boot).
touch "$T/boot/.uab-installed-$UAB_BUILD_ID"
log "provisionador instalado; marcador /boot/.uab-installed-$UAB_BUILD_ID criado"
