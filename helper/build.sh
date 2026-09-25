#!/usr/bin/env bash
# Compila o isoBuilder (gravador de pendrive) para Windows e macOS (e Linux) em helper/dist/.
#   dist/isoBuilder-Windows.exe         (x64)
#   dist/isoBuilder-Windows-ARM64.exe
#   dist/isoBuilder-macOS.zip           (app universal: Intel + Apple Silicon)
#   dist/isobuilder-linux-amd64
# Ícone: assets/make-icon.py gera assets/icon.{png,icns,ico} (versionados).
# Uso: helper/build.sh [versão]
set -euo pipefail
cd "$(dirname "$0")"
GO="${GO:-go}"
command -v "$GO" >/dev/null 2>&1 || GO="$(cd .. && pwd)/work/tools/go/bin/go"
VERSION="${1:-$(date -u +%Y.%m.%d)}"
export CGO_ENABLED=0 GOFLAGS=-trimpath
# Caches do Go dentro do projeto (nada em ~/go ou ~/.cache)
ROOT="$(cd .. && pwd)"
export GOCACHE="${GOCACHE:-$ROOT/work/tools/gocache}" GOPATH="${GOPATH:-$ROOT/work/tools/gopath}"
export GOTELEMETRY=off XDG_CONFIG_HOME="${UAB_GO_CONFIG:-$ROOT/work/tools/config}"
LD="-s -w -X main.version=$VERSION"
rm -rf dist; mkdir -p dist

# Windows: ícone + nome/versão do arquivo embutidos no .exe (rsrc_windows_*.syso)
python3 assets/winres.py assets/icon.ico . "$VERSION"
trap 'rm -f rsrc_windows_*.syso' EXIT

"$GO" vet ./...
"$GO" test ./...

GOOS=windows GOARCH=amd64 "$GO" build -ldflags "$LD -H windowsgui" -o dist/isoBuilder-Windows.exe .
GOOS=windows GOARCH=arm64 "$GO" build -ldflags "$LD -H windowsgui" -o dist/isoBuilder-Windows-ARM64.exe .
GOOS=linux   GOARCH=amd64 "$GO" build -ldflags "$LD" -o dist/isobuilder-linux-amd64 .

# macOS: .app com os dois binários e um lançador que escolhe a arquitetura.
APP="dist/isoBuilder.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
GOOS=darwin GOARCH=arm64 "$GO" build -ldflags "$LD" -o "$APP/Contents/MacOS/isoBuilder-arm64" .
GOOS=darwin GOARCH=amd64 "$GO" build -ldflags "$LD" -o "$APP/Contents/MacOS/isoBuilder-x86_64" .
cp assets/icon.icns "$APP/Contents/Resources/isoBuilder.icns"
cat >"$APP/Contents/MacOS/isoBuilder" <<'EOF'
#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
ARCH="$(uname -m)"
[ "$ARCH" = arm64 ] || ARCH=x86_64
# Roda em segundo plano e encerra o lançador: assim, abrir o app de novo (ex.: depois
# de fechar a aba durante a gravação) inicia outro lançador, que reabre a página do
# isoBuilder que continua rodando. (Com "exec", o macOS só "ativaria" o app e nada aparecia.)
"$DIR/isoBuilder-$ARCH" "$@" </dev/null >/dev/null 2>&1 &
EOF
chmod 755 "$APP/Contents/MacOS/"*
cat >"$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>isoBuilder</string>
  <key>CFBundleDisplayName</key><string>isoBuilder</string>
  <key>CFBundleIconFile</key><string>isoBuilder</string>
  <key>CFBundleIdentifier</key><string>local.autoinstall-builder.gravador</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleExecutable</key><string>isoBuilder</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>LSUIElement</key><true/>
</dict></plist>
EOF
( cd dist && python3 - <<'EOF'
# zip preservando permissões de execução (o zip do Python mantém o modo Unix)
import os, zipfile
with zipfile.ZipFile("isoBuilder-macOS.zip", "w", zipfile.ZIP_DEFLATED) as z:
    for root, dirs, files in os.walk("isoBuilder.app"):
        for d in dirs:
            p = os.path.join(root, d); zi = zipfile.ZipInfo(p + "/"); zi.external_attr = (0o40755 << 16) | 0x10; z.writestr(zi, "")
        for f in files:
            p = os.path.join(root, f); zi = zipfile.ZipInfo(p); zi.external_attr = (os.stat(p).st_mode & 0xFFFF) << 16
            zi.compress_type = zipfile.ZIP_DEFLATED
            z.writestr(zi, open(p, "rb").read())
EOF
)
rm -rf "$APP"
( cd dist && sha256sum ./* > SHA256SUMS )
ls -la dist
