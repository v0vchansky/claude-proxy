#!/bin/bash
# Собирает ClaudeProxy.app: ядро (Go) + UI (SwiftPM) в один бандл, ad-hoc подпись.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
CORE_DIR="$ROOT/core"
APP_DIR="$ROOT/app"
DIST="$ROOT/dist"
APP="$DIST/ClaudeProxy.app"

export PATH="/opt/homebrew/bin:$PATH"

echo "==> Сборка ядра (Go)"
( cd "$CORE_DIR" && GOFLAGS=-mod=mod go build -o bin/claude-proxy-core . )

echo "==> Сборка UI (Swift, release)"
( cd "$APP_DIR" && swift build -c release )

APP_BIN="$APP_DIR/.build/release/ClaudeProxyApp"
CORE_BIN="$CORE_DIR/bin/claude-proxy-core"

echo "==> Сборка бандла $APP"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Helpers" "$APP/Contents/Resources"

cp "$APP_BIN" "$APP/Contents/MacOS/ClaudeProxyApp"
cp "$CORE_BIN" "$APP/Contents/Helpers/claude-proxy-core"
chmod +x "$APP/Contents/MacOS/ClaudeProxyApp" "$APP/Contents/Helpers/claude-proxy-core"

cat > "$APP/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>ClaudeProxy</string>
  <key>CFBundleDisplayName</key><string>Claude Proxy</string>
  <key>CFBundleIdentifier</key><string>com.claudeproxy.app</string>
  <key>CFBundleExecutable</key><string>ClaudeProxyApp</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0.0</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>LSMinimumSystemVersion</key><string>14.0</string>
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

echo "==> ad-hoc подпись"
codesign --force --deep --sign - "$APP" 2>/dev/null || codesign --force --sign - "$APP"

echo "==> Готово: $APP"
echo "Запуск:  open \"$APP\""
