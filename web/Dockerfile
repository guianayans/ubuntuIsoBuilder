# Interface web do ubuntu-autoinstall-builder (iso.gvtserver.online).
# Contexto de build: raiz do projeto (veja docker-compose.yml).

# ---- 1. Gravador de Pendrive (Windows/macOS/Linux), compilado do código-fonte
FROM golang:1-bookworm AS helper
RUN apt-get update && apt-get install -y --no-install-recommends python3 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY helper/ ./helper/
RUN mkdir -p work && GO=go ./helper/build.sh

# ---- 2. Aplicação
FROM python:3.12-slim-bookworm
RUN apt-get update && apt-get install -y --no-install-recommends \
      xorriso gnupg curl openssl openssh-client ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/*
RUN pip install --no-cache-dir "fastapi>=0.115,<1" "starlette>=0.40" "uvicorn[standard]>=0.29,<1" \
      "pyyaml>=6,<7" "jsonschema>=4,<5" "asyncssh>=2.14,<3" "segno>=1.5,<2"

# Tailscale oficial (binários estáticos, SHA256 conferido) para o painel entrar
# na Tailnet em modo userspace e abrir o terminal SSH nas máquinas.
ARG TS_VERSION=1.102.4
RUN set -eu; arch="$(dpkg --print-architecture)"; f="tailscale_${TS_VERSION}_${arch}.tgz"; \
    curl -fsSL -o /tmp/ts.tgz "https://pkgs.tailscale.com/stable/$f"; \
    echo "$(curl -fsSL "https://pkgs.tailscale.com/stable/$f.sha256")  /tmp/ts.tgz" | sha256sum -c -; \
    tar -xzf /tmp/ts.tgz -C /tmp; install -m 0755 /tmp/tailscale_*/tailscale /tmp/tailscale_*/tailscaled /usr/local/bin/; \
    rm -rf /tmp/ts.tgz /tmp/tailscale_*

WORKDIR /app
COPY build.sh setup.sh validate.sh test-iso.sh ./
COPY scripts/ ./scripts/
COPY templates/ ./templates/
COPY payload/ ./payload/
COPY server-tools/ ./server-tools/
COPY schema/ ./schema/
COPY config/install.example.env ./config/
COPY web/ ./web/
COPY --from=helper /src/helper/dist/ ./helper/dist/

RUN chmod 755 build.sh scripts/*.sh payload/*.sh server-tools/*.sh web/entrypoint.sh \
    && useradd -u 1000 -m app && mkdir -p /data/wg && chown app:app /data /data/wg && chmod 700 /data

USER app
ENV DATA_DIR=/data UAB_ROOT=/app UAB_CACHE_DIR=/data/cache HELPER_DIST=/app/helper/dist \
    PYTHONUNBUFFERED=1 PORT=8080
EXPOSE 8080
VOLUME ["/data"]
HEALTHCHECK --interval=20s --timeout=5s --retries=6 --start-period=20s \
  CMD python3 -c "import urllib.request,sys; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:8080/health',timeout=4).status==200 else 1)"
CMD ["/app/web/entrypoint.sh"]
