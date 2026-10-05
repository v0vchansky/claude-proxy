package provision

import "fmt"

// buildScript собирает bash-скрипт установки/усыновления. Выполняется на сервере под
// root. Числовые/строковые параметры подставляются только в ЗАГОЛОВОК (через fmt), а
// тело — raw-строки: в них есть bash-раскрытия вида ${VAR%%/*}, и fmt к ним не
// применяется, иначе '%' в них ломается. Скрипт идемпотентен: повторный запуск с теми
// же ключами ничего не меняет на сервере.
//
// Соглашения вывода:
//
//	LOG:<текст>              — шаг для UI
//	MODE=fresh|adopt         — режим
//	NEEDS_REBOOT=0|1         — нужен ли ребут (kernel-модуль не под текущее ядро)
//	SERVER_PUBLIC_KEY=...     — публичный ключ сервера
//	AWG_PORT=, SERVER_VPN=, JC=, JMIN=, JMAX=, S1=, S2=, S3=, S4=, H1..H4=
//	CLIENT_VPN=              — фактический адрес peer'а прокси (выделен сервером)
//	CLIENT_VPN_FULL=         — фактический адрес peer'а Полного VPN (только если был ключ)
//	PROVISION_OK             — успешное завершение
func buildScript(p Params, clientPub, clientPubFull string) string {
	header := fmt.Sprintf(`set -euo pipefail

AWG_PORT=%d
SERVER_VPN=%q
CLIENT_VPN=%q
CLIENT_PUB=%q
CLIENT_VPN_FULL=%q
CLIENT_PUB_FULL=%q
JC=%d; JMIN=%d; JMAX=%d
S1=%d; S2=%d
H1=%d; H2=%d; H3=%d; H4=%d
`,
		p.AWGPort, p.ServerVpnAddress, p.ClientVpnAddress, clientPub,
		p.ClientVpnAddressFull, clientPubFull,
		p.Jc, p.Jmin, p.Jmax, p.S1, p.S2, p.H1, p.H2, p.H3, p.H4)

	return header + scriptLib + scriptBody
}

// scriptLib — функции работы с awg0.conf: выделение адресов клиенту, дописывание
// peer'ов, приведение S3/S4. Вынесены отдельно, чтобы go test прогонял ровно этот код
// через bash на временном конфиге (без awg/root/сети). Только POSIX awk (на сервере это
// mawk) и bash 3.2+; без sed -i и без printf %d на больших числах.
const scriptLib = `
# --- библиотека awg0.conf ---------------------------------------------------------

# Программа выделения адресов. Вход — awg0.conf (файл-аргумент) и окружение:
#   AL_PUB / AL_PUB_FULL   — публичные ключи прокси / Полного VPN (второй может быть пуст);
#   AL_PREF / AL_PREF_FULL — запрошенные адреса (лишь предпочтение, если свободны);
#   AL_LIVE                — вывод "awg show awg0 allowed-ips" (пусто, если интерфейс не поднят).
# Выход — строки KEY=VALUE; при ошибке — ALLOC_ERR=<текст> и код 1.
ALLOC_AWK='
function trim(s) { sub(/^[ \t\r]+/, "", s); sub(/[ \t\r]+$/, "", s); return s }
function val(line,   i) { i = index(line, "="); return trim(substr(line, i + 1)) }
function lkey(line,   i) { i = index(line, "="); return tolower(trim(substr(line, 1, i - 1))) }
function ip2n(s,   a, n, i) {
  n = split(s, a, ".")
  if (n != 4) return -1
  for (i = 1; i <= 4; i++) if (a[i] !~ /^[0-9]+$/ || a[i] + 0 > 255) return -1
  return ((a[1] * 256 + a[2]) * 256 + a[3]) * 256 + a[4]
}
function n2ip(n,   d, c, b) {
  d = n % 256; n = (n - d) / 256
  c = n % 256; n = (n - c) / 256
  b = n % 256; n = (n - b) / 256
  return n "." b "." c "." d
}
function pow2(k,   r) { r = 1; while (k-- > 0) r *= 2; return r }
# Разбор "a.b.c.d[/m]" в CIDR_IP/CIDR_N/CIDR_M; 0 — не IPv4.
function cidr(s,   p, ip, m) {
  s = trim(s); if (s == "" || index(s, ":")) return 0
  p = index(s, "/")
  if (p) { ip = substr(s, 1, p - 1); m = substr(s, p + 1) } else { ip = s; m = 32 }
  if (m !~ /^[0-9]+$/ || m + 0 > 32 || ip2n(ip) < 0) return 0
  CIDR_IP = ip; CIDR_N = ip2n(ip); CIDR_M = m + 0
  return 1
}
# Занесение AllowedIPs peer-а: все диапазоны — занятые; /32 — ещё и адрес этого ключа.
function addallowed(key, list, src,   parts, n, i, sz, st) {
  n = split(list, parts, /[, \t]+/)
  for (i = 1; i <= n; i++) {
    if (!cidr(parts[i])) continue
    sz = pow2(32 - CIDR_M); st = int(CIDR_N / sz) * sz
    NR_R++; RGS[NR_R] = st; RGE[NR_R] = st + sz - 1
    if (key == "" || CIDR_M != 32) continue
    if (src == "conf" && !(key in CONFIP)) CONFIP[key] = CIDR_IP
    if (src == "live") { if (!(key in LIVEIP)) LIVEIP[key] = CIDR_IP; LIVEHAS[key, CIDR_IP] = 1 }
  }
}
function flush() { if (sect == "peer") addallowed(pkey, pallowed, "conf"); pkey = ""; pallowed = "" }
function used(x,   i) {
  if (x <= NET || x >= BC || x == SRV || (n2ip(x) in TAKEN)) return 1
  for (i = 1; i <= NR_R; i++) if (x >= RGS[i] && x <= RGE[i]) return 1
  return 0
}
# Адрес для ключа: свой из конфига → свой из живого интерфейса → запрошенный, если
# свободен → наименьший свободный. -1 — подсеть заполнена.
function pick(key, pref,   x) {
  if (key in CONFIP) return ip2n(CONFIP[key])
  if (key in LIVEIP) return ip2n(LIVEIP[key])
  if (cidr(pref)) { x = CIDR_N; if (x > NET && x < BC && !used(x)) return x }
  for (x = NET + 1; x < BC; x++) if (!used(x)) return x
  return -1
}
function fail(msg) { print "ALLOC_ERR=" msg; exit 1 }
/^[ \t]*\[/ {
  flush(); s = tolower(trim($0))
  sect = (s == "[interface]") ? "iface" : (s == "[peer]") ? "peer" : "other"
  next
}
/^[ \t]*[#;]/ || index($0, "=") == 0 { next }
{
  k = lkey($0)
  if (sect == "iface" && k == "address" && SRVIP == "") {
    n = split(val($0), parts, /[, \t]+/)
    for (i = 1; i <= n; i++) {
      if (!cidr(parts[i])) continue
      SRVIP = CIDR_IP
      SRVM = (index(parts[i], "/") ? CIDR_M : 24)
      break
    }
  }
  if (sect == "peer" && k == "publickey") pkey = val($0)
  if (sect == "peer" && k == "allowedips") pallowed = pallowed "," val($0)
}
END {
  flush()
  nl = split(ENVIRON["AL_LIVE"], lines, "\n")
  for (j = 1; j <= nl; j++) {
    m = split(trim(lines[j]), f, /[ \t]+/)
    if (m < 2) continue
    rest = ""; for (i = 2; i <= m; i++) rest = rest "," f[i]
    addallowed(f[1], rest, "live")
  }
  pub = ENVIRON["AL_PUB"]; pubf = ENVIRON["AL_PUB_FULL"]
  if (pub == "") fail("пустой публичный ключ клиента")
  if (pub == pubf) fail("ключи прокси и Полного VPN совпадают")
  if (SRVIP == "") fail("в awg0.conf нет IPv4 Address в секции [Interface]")
  if (SRVM > 30) fail("подсеть сервера /" SRVM " слишком мала для клиентов")
  SRV = ip2n(SRVIP); SZ = pow2(32 - SRVM); NET = int(SRV / SZ) * SZ; BC = NET + SZ - 1
  a = pick(pub, ENVIRON["AL_PREF"])
  if (a < 0) fail("подсеть " n2ip(NET) "/" SRVM " заполнена — нет свободного адреса для прокси")
  TAKEN[n2ip(a)] = 1  # ключ — строка: mawk превращает большие числа-ключи в %.6g
  if (pubf != "") {
    b = pick(pubf, ENVIRON["AL_PREF_FULL"])
    if (b < 0) fail("подсеть " n2ip(NET) "/" SRVM " заполнена — нет свободного адреса для Полного VPN")
    if (b == a) fail("адрес " n2ip(a) " закреплён за обоими ключами клиента")
  }
  print "ALLOC_SERVER=" SRVIP
  print "ALLOC_PREFIX=" SRVM
  print "ALLOC_NET=" n2ip(NET)
  print "ALLOC_IP=" n2ip(a)
  print "ALLOC_REUSED=" ((pub in CONFIP || pub in LIVEIP) ? 1 : 0)
  print "ALLOC_INLIVE=" (((pub, n2ip(a)) in LIVEHAS) ? 1 : 0)
  if (pubf != "") {
    print "ALLOC_IP_FULL=" n2ip(b)
    print "ALLOC_REUSED_FULL=" ((pubf in CONFIP || pubf in LIVEIP) ? 1 : 0)
    print "ALLOC_INLIVE_FULL=" (((pubf, n2ip(b)) in LIVEHAS) ? 1 : 0)
  }
}'

# alloc_addrs CONF — выделяет адреса клиенту по конфигу и живому интерфейсу.
# Вход: CLIENT_PUB, CLIENT_PUB_FULL, CLIENT_VPN, CLIENT_VPN_FULL, LIVE_ALLOWED.
# Выход (переменные): SRV_IP, SRV_PREFIX, SRV_NET, CLIENT_IP, CLIENT_IP_FULL,
# REUSED, REUSED_FULL, INLIVE, INLIVE_FULL. Конфиг не меняет. Ошибка → текст в stderr, код 1.
alloc_addrs(){
  local out k v
  if ! out="$(AL_PUB="$CLIENT_PUB" AL_PUB_FULL="$CLIENT_PUB_FULL" \
      AL_PREF="$CLIENT_VPN" AL_PREF_FULL="$CLIENT_VPN_FULL" AL_LIVE="${LIVE_ALLOWED:-}" \
      awk "$ALLOC_AWK" "$1")"; then
    out="$(printf '%s\n' "$out" | awk -F'ALLOC_ERR=' 'NF > 1 {print $2}')"
    echo "${out:-ошибка выделения адресов}" >&2
    return 1
  fi
  SRV_IP=""; SRV_PREFIX=""; SRV_NET=""; CLIENT_IP=""; CLIENT_IP_FULL=""
  REUSED=0; REUSED_FULL=0; INLIVE=0; INLIVE_FULL=0
  while IFS='=' read -r k v; do
    case "$k" in
      ALLOC_SERVER) SRV_IP="$v" ;;
      ALLOC_PREFIX) SRV_PREFIX="$v" ;;
      ALLOC_NET) SRV_NET="$v" ;;
      ALLOC_IP) CLIENT_IP="$v" ;;
      ALLOC_IP_FULL) CLIENT_IP_FULL="$v" ;;
      ALLOC_REUSED) REUSED="$v" ;;
      ALLOC_REUSED_FULL) REUSED_FULL="$v" ;;
      ALLOC_INLIVE) INLIVE="$v" ;;
      ALLOC_INLIVE_FULL) INLIVE_FULL="$v" ;;
    esac
  done <<EOF
$out
EOF
  [ -n "$CLIENT_IP" ]
}

# conf_has_key CONF KEY — есть ли в конфиге peer с таким PublicKey.
conf_has_key(){
  AL_KEY="$2" awk '
    function trim(s) { sub(/^[ \t\r]+/, "", s); sub(/[ \t\r]+$/, "", s); return s }
    index($0, "=") && tolower(trim(substr($0, 1, index($0, "=") - 1))) == "publickey" &&
      trim(substr($0, index($0, "=") + 1)) == ENVIRON["AL_KEY"] { f = 1 }
    END { exit(f ? 0 : 1) }' "$1"
}

# conf_has_peer CONF KEY IP — есть ли в конфиге peer KEY с адресом IP (/32 или без маски).
conf_has_peer(){
  AL_KEY="$2" AL_IP="$3" awk '
    function trim(s) { sub(/^[ \t\r]+/, "", s); sub(/[ \t\r]+$/, "", s); return s }
    function val(line,   i) { i = index(line, "="); return trim(substr(line, i + 1)) }
    function lkey(line,   i) { i = index(line, "="); return tolower(trim(substr(line, 1, i - 1))) }
    function flush(   n, p, i) {
      if (sect && key == ENVIRON["AL_KEY"]) {
        n = split(allowed, p, /[, \t]+/)
        for (i = 1; i <= n; i++) if (p[i] == ENVIRON["AL_IP"] "/32" || p[i] == ENVIRON["AL_IP"]) found = 1
      }
      key = ""; allowed = ""
    }
    /^[ \t]*\[/ { flush(); sect = (tolower(trim($0)) == "[peer]"); next }
    index($0, "=") == 0 { next }
    sect && lkey($0) == "publickey" { key = val($0) }
    sect && lkey($0) == "allowedips" { allowed = allowed "," val($0) }
    END { flush(); exit(found ? 0 : 1) }
  ' "$1"
}

# conf_upsert_peer CONF KEY IP — гарантирует в конфиге peer KEY с AllowedIPs = IP/32.
# Уже есть — файл не трогается (код 1, «без изменений»). Новый ключ — дописывается в
# конец, остальной файл байт в байт прежний. Ключ есть, но с другим адресом/без адреса —
# его секция заменяется. Код 0 — конфиг изменён. Права/владелец файла сохраняются.
conf_upsert_peer(){
  local conf="$1" key="$2" ip="$3" tmp
  if conf_has_peer "$conf" "$key" "$ip"; then return 1; fi
  if conf_has_key "$conf" "$key"; then
    tmp="$(mktemp)"
    AL_KEY="$key" awk '
      function trim(s) { sub(/^[ \t\r]+/, "", s); sub(/[ \t\r]+$/, "", s); return s }
      function flush() { if (!drop) printf "%s", buf; buf = ""; drop = 0 }
      /^[ \t]*\[/ { flush() }
      index($0, "=") && tolower(trim(substr($0, 1, index($0, "=") - 1))) == "publickey" &&
        trim(substr($0, index($0, "=") + 1)) == ENVIRON["AL_KEY"] { drop = 1 }
      { buf = buf $0 "\n" }
      END { flush() }' "$conf" > "$tmp"
    cat "$tmp" > "$conf"; rm -f "$tmp"
  fi
  printf '\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32\n' "$key" "$ip" >> "$conf"
  return 0
}

# conf_normalize_s34 CONF — приводит S3/S4 в [Interface] к 0 (userspace-клиент их не
# умеет). Код 0 — секция [Interface] изменена, 1 — уже были нули (файл не тронут).
conf_normalize_s34(){
  local conf="$1" tmp
  tmp="$(mktemp)"
  if awk '
      function trim(s) { sub(/^[ \t\r]+/, "", s); sub(/[ \t\r]+$/, "", s); return s }
      /^[ \t]*\[/ { iface = (tolower(trim($0)) == "[interface]") }
      iface && index($0, "=") {
        k = tolower(trim(substr($0, 1, index($0, "=") - 1)))
        v = trim(substr($0, index($0, "=") + 1))
        if ((k == "s3" || k == "s4") && v != "0") { print toupper(k) " = 0"; ch = 1; next }
      }
      { print }
      END { exit(ch ? 0 : 1) }' "$conf" > "$tmp"; then
    cat "$tmp" > "$conf"; rm -f "$tmp"; return 0
  fi
  rm -f "$tmp"; return 1
}
# --- конец библиотеки -------------------------------------------------------------
`

// scriptBody — тело без подстановок. Использует переменные из заголовка и scriptLib.
const scriptBody = `
DIR=/etc/amnezia/amneziawg
CONF="$DIR/awg0.conf"

log(){ echo "LOG:$*"; }

# 0. root
if [ "$(id -u)" != "0" ]; then echo "нужен root (или sudo)"; exit 10; fi

# 1. дистрибутив
if [ -r /etc/os-release ]; then . /etc/os-release; else echo "не удалось определить ОС"; exit 11; fi
case "${ID:-}" in
  ubuntu|debian) : ;;
  *) echo "поддерживаются только Ubuntu/Debian (обнаружено: ${ID:-unknown})"; exit 12 ;;
esac

# 2. режим: донастройка (adopt), если уже есть конфиг или интерфейс
LIVE_UP=0
if ip link show awg0 >/dev/null 2>&1; then LIVE_UP=1; fi
MODE=fresh
if [ -f "$CONF" ] || [ "$LIVE_UP" = 1 ]; then MODE=adopt; fi
echo "MODE=$MODE"
if [ "$MODE" = adopt ]; then
  log "Режим: донастройка уже настроенного сервера (awg0 поднят: $([ "$LIVE_UP" = 1 ] && echo да || echo нет))"
else
  log "Режим: установка с нуля"
fi

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

# 5. конфиг: создаётся, только если его нет. Существующий не переписывается.
IFACE_CHANGED=0
if [ ! -f "$CONF" ]; then
  log "Создание конфига awg0"
  case "$SERVER_VPN" in */*) SRV_ADDR="$SERVER_VPN" ;; *) SRV_ADDR="${SERVER_VPN}/24" ;; esac
  cat > "$CONF" <<EOF
[Interface]
Address = ${SRV_ADDR}
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
  IFACE_CHANGED=1
fi

# 6. выделение адресов клиенту — ДО любых правок существующего конфига: при ошибке
# (подсеть заполнена) сервер остаётся как был. Занятыми считаются адрес сервера,
# сеть/broadcast, все AllowedIPs из конфига и из живого awg0. Свой ключ уже есть —
# его адрес переиспользуется; запрошенный адрес — лишь предпочтение.
LIVE_ALLOWED=""
if [ "$LIVE_UP" = 1 ]; then LIVE_ALLOWED="$(awg show awg0 allowed-ips 2>/dev/null || true)"; fi
ALLOC_ERR_FILE="$(mktemp)"
if ! alloc_addrs "$CONF" 2>"$ALLOC_ERR_FILE"; then
  echo "выделение адреса клиенту: $(cat "$ALLOC_ERR_FILE")"
  rm -f "$ALLOC_ERR_FILE"
  exit 20
fi
rm -f "$ALLOC_ERR_FILE"
ADDRS="прокси ${CLIENT_IP}$([ "$REUSED" = 1 ] && echo ' (уже был)' || true)"
if [ -n "$CLIENT_PUB_FULL" ]; then
  ADDRS="$ADDRS, Полный VPN ${CLIENT_IP_FULL}$([ "$REUSED_FULL" = 1 ] && echo ' (уже был)' || true)"
fi
log "Подсеть ${SRV_NET}/${SRV_PREFIX}, сервер ${SRV_IP}; выделены адреса: ${ADDRS}"

# 7. S3/S4 → 0 (только если не нули: иначе [Interface] не трогаем)
if [ "$MODE" = adopt ] && conf_normalize_s34 "$CONF"; then
  IFACE_CHANGED=1
  log "S3/S4 приведены к 0 — секция [Interface] изменена"
fi

# 8. клиентские peer'ы В КОНФИГ (для ребута): прокси — всегда, Полный VPN — если есть ключ.
# Уже записанный peer с тем же адресом не трогается — конфиг байт в байт прежний.
if conf_upsert_peer "$CONF" "$CLIENT_PUB" "$CLIENT_IP"; then log "Peer прокси записан в конфиг"; fi
if [ -n "$CLIENT_PUB_FULL" ]; then
  if conf_upsert_peer "$CONF" "$CLIENT_PUB_FULL" "$CLIENT_IP_FULL"; then log "Peer Полного VPN записан в конфиг"; fi
fi

# 9. ip_forward (персистентно; файл переписывается, только если отличается)
SYSCTL_FILE=/etc/sysctl.d/99-amneziawg.conf
if [ "$(cat "$SYSCTL_FILE" 2>/dev/null || true)" != "net.ipv4.ip_forward=1" ]; then
  echo 'net.ipv4.ip_forward=1' > "$SYSCTL_FILE"
fi
if [ "$(sysctl -n net.ipv4.ip_forward 2>/dev/null || echo 0)" != 1 ]; then
  sysctl -p "$SYSCTL_FILE" >/dev/null 2>&1 || sysctl -w net.ipv4.ip_forward=1 >/dev/null
fi
log "ip_forward включён"

# 10. внешний интерфейс и NAT (своя таблица nftables). Таблица пересоздаётся, только если
# содержимое изменилось или её нет в ядре — иначе живые правила не трогаем.
EXT_IF="$(ip route get 1.1.1.1 2>/dev/null | awk '{for(i=1;i<=NF;i++) if($i=="dev"){print $(i+1); exit}}')"
[ -n "$EXT_IF" ] || EXT_IF="$(ip -o -4 route show to default | awk '{print $5; exit}')"
log "Внешний интерфейс: ${EXT_IF:-?}"
SUBNET="${SRV_NET}/${SRV_PREFIX}"
if command -v nft >/dev/null 2>&1; then
  mkdir -p /etc/nftables.d
  NFT_FILE=/etc/nftables.d/claudeproxy.conf
  NFT_NEW="$(cat <<EOF
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
)"
  if [ "$(cat "$NFT_FILE" 2>/dev/null || true)" = "$NFT_NEW" ] && nft list table ip claudeproxy >/dev/null 2>&1; then
    log "NAT/forwarding уже настроены (nftables) — без изменений"
  else
    printf '%s\n' "$NFT_NEW" > "$NFT_FILE"
    nft delete table ip claudeproxy >/dev/null 2>&1 || true
    nft -f "$NFT_FILE"
    log "NAT/forwarding настроены (nftables)"
  fi
  grep -q 'nftables.d/\*.conf' /etc/nftables.conf 2>/dev/null || \
    echo 'include "/etc/nftables.d/*.conf"' >> /etc/nftables.conf
  systemctl enable nftables >/dev/null 2>&1 || true
else
  log "nft не найден — пропускаю firewall (проверьте NAT вручную)"
fi

# 11. автозапуск + применение. Перезапуск awg0 рвёт сессии ВСЕХ клиентов, поэтому:
#  - awg0 поднят и [Interface] не менялся → peer'ы добавляются вживую, без перезапуска;
#  - awg0 поднят, но [Interface] изменён → перезапуск (клиенты переподключатся сами);
#  - awg0 не поднят → поднимаем из конфига, ничего не удаляя (нужен kernel-модуль; нет — ребут).
systemctl enable awg-quick@awg0 >/dev/null 2>&1 || true
NEEDS_REBOOT=0
if [ "$LIVE_UP" = 1 ] && [ "$IFACE_CHANGED" = 0 ]; then
  if [ "$INLIVE" != 1 ]; then
    awg set awg0 peer "$CLIENT_PUB" allowed-ips "${CLIENT_IP}/32" \
      || { echo "awg set (peer прокси) не прошёл"; exit 21; }
  fi
  if [ -n "$CLIENT_PUB_FULL" ] && [ "$INLIVE_FULL" != 1 ]; then
    awg set awg0 peer "$CLIENT_PUB_FULL" allowed-ips "${CLIENT_IP_FULL}/32" \
      || { echo "awg set (peer Полного VPN) не прошёл"; exit 21; }
  fi
  log "Peer'ы применены вживую, без перезапуска awg0 — существующие клиенты не затронуты"
elif [ "$LIVE_UP" = 1 ]; then
  log "Перезапуск awg0 (изменена секция [Interface]) — существующие клиенты переподключатся"
  ip link del awg0 >/dev/null 2>&1 || true
  systemctl restart awg-quick@awg0 2>/dev/null || awg-quick up awg0 || true
elif modprobe amneziawg >/dev/null 2>&1; then
  log "Поднятие awg0"
  systemctl restart awg-quick@awg0 2>/dev/null || awg-quick up awg0 || true
else
  NEEDS_REBOOT=1
  log "Kernel-модуль не собран под текущее ядро — перезагрузка сервера для активации"
fi
if [ "$NEEDS_REBOOT" = 0 ] && ! ip link show awg0 >/dev/null 2>&1; then
  log "ВНИМАНИЕ: awg0 не поднялся — проверьте systemctl status awg-quick@awg0"
fi

# 12. валидность конфига
awg-quick strip awg0 >/dev/null 2>&1 && log "Конфиг валиден" || log "ВНИМАНИЕ: awg-quick strip не прошёл"

# 13. результат (читаем из файлов — не зависит от того, поднят ли интерфейс)
echo "NEEDS_REBOOT=$NEEDS_REBOOT"
echo "SERVER_PUBLIC_KEY=$(awg pubkey < "$DIR/server_private.key")"
echo "AWG_PORT=$(awk -F' *= *' '/^ListenPort/{print $2; exit}' "$CONF")"
echo "SERVER_VPN=$SRV_IP"
echo "CLIENT_VPN=$CLIENT_IP"
if [ -n "$CLIENT_PUB_FULL" ]; then echo "CLIENT_VPN_FULL=$CLIENT_IP_FULL"; fi
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

# 14. ребут, если модуль не под текущее ядро — но ТОЛЬКО после того, как вывод выше
# ушёл клиенту. systemd-run регистрирует отдельный таймер и мгновенно возвращает
# управление, не держа SSH-канал; fallback — setsid, полностью отвязанный от сессии.
# awg-quick@awg0 в автозапуске поднимет awg0 с нужным ядром и уже с peer.
if [ "$NEEDS_REBOOT" = 1 ]; then
  systemd-run --on-active=5 systemctl reboot >/dev/null 2>&1 \
    || (setsid sh -c 'sleep 5; systemctl reboot' </dev/null >/dev/null 2>&1 &)
fi
`
