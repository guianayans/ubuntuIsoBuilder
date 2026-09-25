# Remoto do ISO Builder: MeshCentral com o visual do painel, em /remoto/.
FROM ghcr.io/ylianst/meshcentral:latest-slim
COPY config/config.json /defaults/config.json
COPY web/ /opt/meshcentral/meshcentral-web/
COPY docker-entrypoint.sh /docker-entrypoint.sh
RUN chmod +x /docker-entrypoint.sh
EXPOSE 4021
ENTRYPOINT ["/docker-entrypoint.sh"]
