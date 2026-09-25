#!/usr/bin/env bash
# Compila o Gravador de Pendrive para Windows e macOS (e Linux) em helper/dist/.
#   dist/Gravador-Pendrive-Windows.exe         (x64)
#   dist/Gravador-Pendrive-Windows-ARM64.exe
#   dist/Gravador-Pendrive-macOS.zip           (app universal: Intel + Apple Silicon)
#   dist/gravador-linux-amd64
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

"$GO" vet ./...
"$GO" test ./...

GOOS=windows GOARCH=amd64 "$GO" build -ldflags "$LD -H windowsgui" -o dist/Gravador-Pendrive-Windows.exe .
GOOS=windows GOARCH=arm64 "$GO" build -ldflags "$LD -H windowsgui" -o dist/Gravador-Pendrive-Windows-ARM64.exe .
GOOS=linux   GOARCH=amd64 "$GO" build -ldflags "$LD" -o dist/gravador-linux-amd64 .

# macOS: .app com os dois binários e um lançador que escolhe a arquitetura.
APP="dist/Gravador de Pendrive.app"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
GOOS=darwin GOARCH=arm64 "$GO" build -ldflags "$LD" -o "$APP/Contents/MacOS/gravador-arm64" .
GOOS=darwin GOARCH=amd64 "$GO" build -ldflags "$LD" -o "$APP/Contents/MacOS/gravador-x86_64" .
cat >"$APP/Contents/MacOS/Gravador" <<'EOF'
#!/bin/sh
DIR="$(cd "$(dirname "$0")" && pwd)"
ARCH="$(uname -m)"
[ "$ARCH" = arm64 ] || ARCH=x86_64
exec "$DIR/gravador-$ARCH" "$@"
EOF
chmod 755 "$APP/Contents/MacOS/"*
cat >"$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleName</key><string>Gravador de Pendrive</string>
  <key>CFBundleDisplayName</key><string>Gravador de Pendrive</string>
  <key>CFBundleIdentifier</key><string>local.autoinstall-builder.gravador</string>
  <key>CFBundleVersion</key><string>$VERSION</string>
  <key>CFBundleShortVersionString</key><string>$VERSION</string>
  <key>CFBundleExecutable</key><string>Gravador</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>LSUIElement</key><true/>
</dict></plist>
EOF
( cd dist && python3 - <<'EOF'
# zip preservando permissões de execução (o zip do Python mantém o modo Unix)
import os, zipfile
with zipfile.ZipFile("Gravador-Pendrive-macOS.zip", "w", zipfile.ZIP_DEFLATED) as z:
    for root, dirs, files in os.walk("Gravador de Pendrive.app"):
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
