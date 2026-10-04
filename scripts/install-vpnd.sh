#!/bin/sh
# Устанавливает root-демон vpnd (full-tunnel VPN) как LaunchDaemon.
# Запускается под root (через osascript "with administrator privileges").
# Аргументы:
#   $1 — путь к бинарю ядра-источнику (из бандла: <App>/Contents/Helpers/claude-proxy-core)
#   $2 — uid пользователя-установщика (для peercred-забора сокета, docs/full-vpn-design.md §5); опционален
# Детали: §6 (установка при ad-hoc подписи), §5 (сокет и права).
set -eu

SRC="${1:?нужен путь к бинарю ядра}"
UID_ALLOWED="${2:-}"

LABEL="com.claudeproxy.vpnd"
BIN="/Library/PrivilegedHelperTools/claude-proxy-core"
PLIST="/Library/LaunchDaemons/${LABEL}.plist"
SOCK="/var/run/claude-proxy-vpnd.sock"
STATE_DIR="/var/lib/claude-proxy"
STATE="${STATE_DIR}/vpnd-state.json"

if [ ! -f "$SRC" ]; then
  echo "install-vpnd: бинарь-источник не найден: $SRC" >&2
  exit 1
fi

# Каталоги: хелперы и рабочее состояние демона.
mkdir -p /Library/PrivilegedHelperTools "$STATE_DIR"
chown root:wheel "$STATE_DIR"
chmod 755 "$STATE_DIR"

# Бинарь: root:wheel 0755 — пользователю не перезаписать (анти-эскалация, §6).
install -o root -g wheel -m 755 "$SRC" "$BIN"

# vpnd.conf с allowedUid — демон сверяет peercred на accept (§5).
if [ -n "$UID_ALLOWED" ]; then
  printf 'allowedUid=%s\n' "$UID_ALLOWED" > "${STATE_DIR}/vpnd.conf"
  chown root:wheel "${STATE_DIR}/vpnd.conf"
  chmod 600 "${STATE_DIR}/vpnd.conf"
fi

# LaunchDaemon-plist: RunAtLoad + KeepAlive (boot-recovery и перезапуск после краха, §9).
cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>${LABEL}</string>
  <key>ProgramArguments</key><array>
    <string>${BIN}</string>
    <string>-mode</string><string>vpnd</string>
    <string>-sock</string><string>${SOCK}</string>
    <string>-state</string><string>${STATE}</string>
    <string>-sock-uid</string><string>${UID_ALLOWED}</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
EOF
chown root:wheel "$PLIST"
chmod 644 "$PLIST"

# Перезагрузка: снять старый инстанс (идемпотентно), затем поднять.
launchctl bootout "system/${LABEL}" 2>/dev/null || true
if ! launchctl bootstrap system "$PLIST" 2>/dev/null; then
  # Fallback для старых launchctl.
  launchctl load -w "$PLIST"
fi

echo "install-vpnd: ${LABEL} установлен и запущен"
