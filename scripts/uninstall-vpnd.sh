#!/bin/sh
# Снимает root-демон vpnd: выгружает LaunchDaemon и удаляет его файлы.
# Запускается под root (через osascript "with administrator privileges").
# Каждый шаг идемпотентен — отсутствие файла/сервиса не считается ошибкой.
set -u

LABEL="com.claudeproxy.vpnd"
BIN="/Library/PrivilegedHelperTools/claude-proxy-core"
PLIST="/Library/LaunchDaemons/${LABEL}.plist"
SOCK="/var/run/claude-proxy-vpnd.sock"
STATE_DIR="/var/lib/claude-proxy"

# Выгрузить сервис (bootout, с fallback на unload).
launchctl bootout "system/${LABEL}" 2>/dev/null || launchctl unload -w "$PLIST" 2>/dev/null || true

# Удалить файлы.
rm -f "$PLIST"
rm -f "$BIN"
rm -f "$SOCK"
rm -rf "$STATE_DIR"

echo "uninstall-vpnd: ${LABEL} удалён"
