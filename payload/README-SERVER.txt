ubuntu-autoinstall-builder — referência rápida no servidor
==========================================================

Status geral ............ sudo uab-status
Log do provisionamento .. sudo tail -f /var/log/uab-provision.log
                          journalctl -u uab-provision -f
Refazer provisionamento . sudo systemctl restart uab-provision
Chaves SSH .............. uab-list-ssh-keys
                          uab-add-ssh-key 'ssh-ed25519 AAAA... nome@pc'
                          uab-remove-ssh-key <numero|SHA256:fingerprint>
Tailscale ............... tailscale status ; tailscale ip -4
                          sudo tailscale up --hostname=<nome>   (login manual)

Arquivos
  /etc/uab/uab.conf               configuração (sem segredos)
  /etc/uab/install-info           build, data e disco da instalação
  /var/lib/uab/status             estado de cada etapa
  /etc/ssh/sshd_config.d/10-uab-hardening.conf   endurecimento do SSH
  /boot/.uab-installed-<build>    marcador que impede a ISO de reinstalar
