# Hub WireGuard do ISO Builder (ponto de encontro dos servidores instalados).
# Usa o WireGuard do kernel do host (módulo "wireguard" carregado) e precisa só
# de NET_ADMIN. Não tem interface web: é controlado pelo painel via volume.
FROM alpine:3
RUN apk add --no-cache wireguard-tools-wg iptables python3 iproute2
COPY hub.py /usr/local/bin/uab-hub
RUN chmod 755 /usr/local/bin/uab-hub
ENV HUB_STATE=/state HUB_KEYDIR=/hubkeys HUB_PORT=51822 PYTHONUNBUFFERED=1
HEALTHCHECK --interval=30s --timeout=5s --retries=3 --start-period=15s \
  CMD test -n "$(find /state/hub-status.json -mmin -1 2>/dev/null)"
CMD ["/usr/local/bin/uab-hub"]
