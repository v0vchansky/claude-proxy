package provision

import "fmt"

// buildScript собирает bash-скрипт установки/усыновления. Выполняется на сервере под
// root. Числовые/строковые параметры подставляются только в ЗАГОЛОВОК (через fmt), а
// тело — raw-строка: в нём есть bash-раскрытия вида ${VAR%%/*}, и fmt к телу не
// применяется, иначе '%' в них ломается. Скрипт идемпотентен.
//
// Соглашения вывода:
//
//	LOG:<текст>              — шаг для UI
//	MODE=fresh|adopt         — режим
//	NEEDS_REBOOT=0|1         — нужен ли ребут (kernel-модуль не под текущее ядро)
//	SERVER_PUBLIC_KEY=...     — публичный ключ сервера
//	AWG_PORT=, SERVER_VPN=, JC=, JMIN=, JMAX=, S1=, S2=, S3=, S4=, H1..H4=
//	PROVISION_OK             — успешное завершение
func buildScript(p Params, clientPub string) string {
	header := fmt.Sprintf(`set -euo pipefail

AWG_PORT=%d
SERVER_VPN=%q
CLIENT_VPN=%q
CLIENT_PUB=%q
JC=%d; JMIN=%d; JMAX=%d
S1=%d; S2=%d
H1=%d; H2=%d; H3=%d; H4=%d
`,
		p.AWGPort, p.ServerVpnAddress, p.ClientVpnAddress, clientPub,
		p.Jc, p.Jmin, p.Jmax, p.S1, p.S2, p.H1, p.H2, p.H3, p.H4)

	return header + scriptBody
}

// scriptBody — тело без подстановок. Использует переменные из заголовка.
const scriptBody = `
DIR=/etc/amnezia/amneziawg
CONF="$DIR/awg0.conf"
CLIENT_IP="${CLIENT_VPN%%/*}"

log(){ echo "LOG:$*"; }

# 0. root
if [ "$(id -u)" != "0" ]; then echo "нужен root (или sudo)"; exit 10; fi

# 1. дистрибутив
if [ -r /etc/os-release ]; then . /etc/os-release; else echo "не удалось определить ОС"; exit 11; fi
case "${ID:-}" in
  ubuntu|debian) : ;;
  *) echo "поддерживаются только Ubuntu/Debian (обнаружено: ${ID:-unknown})"; exit 12 ;;
esac

# 2. режим: усыновление, если уже есть конфиг или интерфейс
MODE=fresh
if [ -f "$CONF" ] || ip link show awg0 >/dev/null 2>&1; then MODE=adopt; fi
echo "MODE=$MODE"
log "Режим: $MODE"

# 3. установка AmneziaWG, если нет awg
if ! command -v awg >/dev/null 2>&1; then
  log "Установка AmneziaWG (может занять пару минут)…"
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -y >/dev/null
  apt-get install -y software-properties-common >/dev/null
  if [ "${ID}" = "ubuntu" ]; then
    add-apt-repository -y ppa:amnezia/ppa >/dev/null
  else
    echo "deb https://deb.amnezia.org/ stable main" > /etc/apt/sources.list.d/amnezia.list || true
  fi
  apt-get update -y >/dev/null
  apt-get install -y linux-headers-"$(uname -r)" >/dev/null 2>&1 || log "заголовки под текущее ядро недоступны — потребуется перезагрузка"
  apt-get install -y amneziawg amneziawg-tools >/dev/null 2>&1 || apt-get install -y amneziawg >/dev/null
  log "Пакет установлен"
fi
command -v awg >/dev/null 2>&1 || { echo "awg не найден после установки"; exit 13; }

mkdir -p "$DIR"; chmod 700 "$DIR"; cd "$DIR"

# 4. серверные ключи (userspace, не требуют kernel-модуля)
if [ ! -f server_private.key ]; then
  log "Генерация серверных ключей"
  umask 077
  awg genkey > server_private.key
  awg pubkey < server_private.key > server_public.key
fi
SRV_PRIV="$(cat server_private.key)"

# 5. конфиг
if [ "$MODE" = "fresh" ] || [ ! -f "$CONF" ]; then
  log "Создание конфига awg0"
  cat > "$CONF" <<EOF
[Interface]
Address = ${SERVER_VPN}/24
ListenPort = ${AWG_PORT}
PrivateKey = ${SRV_PRIV}

Jc = ${JC}
Jmin = ${JMIN}
Jmax = ${JMAX}
S1 = ${S1}
S2 = ${S2}
S3 = 0
S4 = 0

H1 = ${H1}
H2 = ${H2}
H3 = ${H3}
H4 = ${H4}
EOF
  chmod 600 "$CONF"
else
  log "Приведение S3/S4 к 0"
  sed -i 's/^S3 = .*/S3 = 0/; s/^S4 = .*/S4 = 0/' "$CONF" || true
fi

# 6. клиентский peer В КОНФИГ (до bring-up): так awg0 поднимется с peer даже после ребута
if ! grep -q "$CLIENT_PUB" "$CONF"; then
  printf '\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32\n' "$CLIENT_PUB" "$CLIENT_IP" >> "$CONF"
fi

# 7. ip_forward (персистентно)
echo 'net.ipv4.ip_forward=1' > /etc/sysctl.d/99-amneziawg.conf
sysctl -p /etc/sysctl.d/99-amneziawg.conf >/dev/null 2>&1 || sysctl -w net.ipv4.ip_forward=1 >/dev/null
log "ip_forward включён"

# 8. внешний интерфейс и NAT (своя таблица nftables)
EXT_IF="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
[ -n "$EXT_IF" ] || EXT_IF="$(ip -o -4 route show to default | awk '{print $5; exit}')"
log "Внешний интерфейс: ${EXT_IF:-?}"
SUBNET="${SERVER_VPN%.*}.0/24"
if command -v nft >/dev/null 2>&1; then
  mkdir -p /etc/nftables.d
  cat > /etc/nftables.d/claudeproxy.conf <<EOF
table ip claudeproxy {
  chain forward {
    type filter hook forward priority -10; policy accept;
    iifname "awg0" oifname "${EXT_IF}" accept
    iifname "${EXT_IF}" oifname "awg0" ct state established,related accept
  }
  chain postrouting {
    type nat hook postrouting priority 100; policy accept;
    oifname "${EXT_IF}" ip saddr ${SUBNET} masquerade
  }
}
EOF
  nft delete table ip claudeproxy >/dev/null 2>&1 || true
  nft -f /etc/nftables.d/claudeproxy.conf
  grep -q 'nftables.d/\*.conf' /etc/nftables.conf 2>/dev/null || \
    echo 'include "/etc/nftables.d/*.conf"' >> /etc/nftables.conf
  systemctl enable nftables >/dev/null 2>&1 || true
  log "NAT/forwarding настроены (nftables)"
else
  log "nft не найден — пропускаю firewall (проверьте NAT вручную)"
fi

# 9. автозапуск + попытка поднять интерфейс сейчас
systemctl enable awg-quick@awg0 >/dev/null 2>&1 || true
NEEDS_REBOOT=1
if modprobe amneziawg >/dev/null 2>&1; then
  NEEDS_REBOOT=0
  log "Поднятие awg0"
  ip link del awg0 >/dev/null 2>&1 || true
  systemctl restart awg-quick@awg0 2>/dev/null || awg-quick up awg0 || true
  awg set awg0 peer "$CLIENT_PUB" allowed-ips "${CLIENT_IP}/32" 2>/dev/null || true
  log "Клиентский peer добавлен"
else
  log "Kernel-модуль не собран под текущее ядро — перезагрузка сервера для активации"
fi

# 10. валидность конфига
awg-quick strip awg0 >/dev/null 2>&1 && log "Конфиг валиден" || log "ВНИМАНИЕ: awg-quick strip не прошёл"

# 11. результат (читаем из файлов — не зависит от того, поднят ли интерфейс)
echo "NEEDS_REBOOT=$NEEDS_REBOOT"
echo "SERVER_PUBLIC_KEY=$(awg pubkey < "$DIR/server_private.key")"
echo "AWG_PORT=$(awk -F' *= *' '/^ListenPort/{print $2; exit}' "$CONF")"
echo "SERVER_VPN=${SERVER_VPN%%/*}"
awk -F' *= *' '
  /^Jc /   {print "JC="$2}
  /^Jmin / {print "JMIN="$2}
  /^Jmax / {print "JMAX="$2}
  /^S1 /   {print "S1="$2}
  /^S2 /   {print "S2="$2}
  /^S3 /   {print "S3="$2}
  /^S4 /   {print "S4="$2}
  /^H1 /   {print "H1="$2}
  /^H2 /   {print "H2="$2}
  /^H3 /   {print "H3="$2}
  /^H4 /   {print "H4="$2}
' "$CONF"
echo "PROVISION_OK"

# 12. ребут, если модуль не под текущее ядро — но ТОЛЬКО после того, как вывод выше
# ушёл клиенту. systemd-run регистрирует отдельный таймер и мгновенно возвращает
# управление, не держа SSH-канал; fallback — setsid, полностью отвязанный от сессии.
# awg-quick@awg0 в автозапуске поднимет awg0 с нужным ядром и уже с peer.
if [ "$NEEDS_REBOOT" = 1 ]; then
  systemd-run --on-active=5 systemctl reboot >/dev/null 2>&1 \
    || (setsid sh -c 'sleep 5; systemctl reboot' </dev/null >/dev/null 2>&1 &)
fi
`
