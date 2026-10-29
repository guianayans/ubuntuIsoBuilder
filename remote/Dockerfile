# Remoto do ISO Builder: MeshCentral com o visual do painel, em /remoto/.
FROM ghcr.io/ylianst/meshcentral:latest-slim
# --- CVE-2026-111497 (Brotli OOB write / Node2Shell): sobe brotli-libs para a versão corrigida do Alpine.
RUN apk add --no-cache --upgrade brotli-libs
COPY config/config.json /defaults/config.json
COPY web/ /opt/meshcentral/meshcentral-web/
COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh
EXPOSE 4021
ENTRYPOINT ["/docker-entrypoint.sh"]
