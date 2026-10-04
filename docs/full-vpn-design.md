# Full VPN (full-tunnel) — дизайн

Статус: дизайн выверен, код не написан. Все системные факты в этом документе проверены
на этой машине (macOS 15.1 / 24B83) чтением состояния или одноразовыми пробами —
ничего в системе не менялось. Внешние факты — со ссылками.

## 1. Резюме

Добавляем второй режим — «Full VPN»: классический VPN на весь системный трафик.
Тот же бинарь `claude-proxy-core`, те же профили (`servers.json`) и тот же приватный
ключ из Keychain, но процесс запускается **root-демоном** (LaunchDaemon) и вместо
gVisor netstack использует **реальный utun** из вендорённого
`core/third_party/amneziawg-go/tun/tun_darwin.go`.

Текущий прокси-режим не трогаем: он остаётся userspace-процессом без root, со своим
сокетом и своим fail-closed (нет туннеля — нет `net.Dial`). Full VPN — отдельная
подсистема со своим сокетом, своим состоянием и своим fail-closed механизмом (PF
kill-switch): в полном туннеле «нет прямого пути наружу» руками кода не обеспечить —
трафик шлют чужие процессы, поэтому закрывает его файрвол.

**Сервер менять не нужно.** Проверено по `docs/spec.md` §2.1: nftables уже содержит
`oifname "ens1" ip saddr 10.77.0.0/24 masquerade` и forward `awg0 <-> ens1`,
`ip_forward=1`. Клиентский uapi уже ставит `allowed_ip=0.0.0.0/0`
(см. `profile.BuildUAPI`, `core/internal/profile/profile.go`) — т.е. криптомаршрутизация
«всё в туннель» на клиенте уже разрешена; дело только за системными маршрутами macOS.

**Ключевое ограничение:** прокси-режим и Full VPN используют один и тот же ключ WG и
один и тот же peer на сервере. Два одновременных сеанса с одним ключом заставляют
сервер «прыгать» между endpoint-ами (roaming) — сессии душат друг друга. Режимы
**взаимоисключающие**, UI обязан принудительно выключать один при включении другого (§8).

## 2. Проверенные факты

### 2.1. utun: `CreateTUN` в tun_darwin.go — подтверждено по исходнику и пробой

`core/third_party/amneziawg-go/tun/tun_darwin.go`:

- Сигнатура: `func CreateTUN(name string, mtu int) (Device, error)`.
  `name` — `"utun"` (ядро выдаст первый свободный номер) или `"utunN"` (явный номер,
  внутри превращается в `Unit: uint32(ifIndex)+1`). Любое другое имя — ошибка
  `Interface name must be utun[0-9]*`.
- Механика: сокет `AF_SYSTEM/SOCK_DGRAM/SYSPROTO_CONTROL`, `ioctl CTLIOCGINFO` по имени
  контрола `com.apple.net.utun_control`, затем `connect` на `SockaddrCtl`. Это
  штатный kernel control API utun, никакого kext.
- Возвращает `tun.Device` (конкретно `*NativeTun`); фактическое имя интерфейса
  читается через `dev.Name()` (`getsockopt UTUN_OPT_IFNAME`) — его и подставляем
  в маршруты/PF.
- MTU задаётся прямо в `CreateTUN(name, mtu)` — внутри `setMTU` делает
  `ioctl SIOCSIFMTU`. IP-адрес интерфейса `CreateTUN` **не** назначает — это наша
  забота через `ifconfig` (§4, фаза 2).
- `Close()` закрывает fd — **интерфейс utun при этом исчезает из системы**, и ядро
  само вычищает все маршруты через него. Это важно для сценария краша (§9).

**Root обязателен — проверено пробой.** Одноразовая Go-проба (scratchpad, модуль с
`replace` на вендорённую копию), запуск под обычным пользователем:

```
euid=501
CreateTUN error: operation not permitted
```

`connect` к `com.apple.net.utun_control` требует привилегии (ядро проверяет
`PRIV_NET_PRIVILEGED_...` / root) — создать utun из обычного процесса нельзя. Apple
даёт непривилегированный путь только через NetworkExtension (`NEPacketTunnelProvider`),
который требует entitlement и подписи Developer ID — для ad-hoc приложения недоступен.
Отсюда и весь дизайн с root-демоном.

### 2.2. Состояние этой машины (снято live)

```
$ sw_vers                       → macOS 15.1 (24B83)
$ route -n get default          → interface: utun4 (!)  — сейчас активен сторонний VPN (v2RayTun)
$ route -n get -ifscope en0 default
   gateway: 192.168.2.1  interface: en0   ← физический шлюз виден даже под чужим VPN
$ netstat -rn -f inet | head
   default   link#26        UCSg    utun4
   default   192.168.2.1    UGScIg  en0     ← scoped-default физического интерфейса
$ networksetup -listallnetworkservices
   USB 10/100/1000 LAN / Thunderbolt Bridge / Wi-Fi / iPhone USB / v2RayTun / peer1
$ networksetup -getdnsservers Wi-Fi
   There aren't any DNS Servers set on Wi-Fi.    ← «пусто» = DHCP, восстанавливать словом "empty"
$ pfctl -s info                 → pfctl: /dev/pf: Permission denied   ← PF тоже только под root
$ grep anchor /etc/pf.conf      → scrub-/nat-/rdr-/dummynet-anchor "com.apple/*", anchor "com.apple/*"
```

Выводы: (а) способ найти физический шлюз должен переживать чужой utun —
`route -n get default` недостаточно; (б) на машине уже живут другие VPN-сервисы —
перечисление сервисов для DNS обязано быть динамическим; (в) точка подвеса PF-якоря
`com.apple/*` в стоковом `/etc/pf.conf` присутствует.

### 2.3. Подпись приложения

```
$ codesign -dv dist/ClaudeProxy.app
   CodeDirectory ... flags=0x2(adhoc)  Signature=adhoc  TeamIdentifier=not set
```

`scripts/build-app.sh` подписывает `codesign --sign -` (ad-hoc). Это решает выбор
способа установки демона (§6).

### 2.4. Эталон поведения — wg-quick darwin

Официальный `wg-quick` для macOS ([darwin.bash](https://git.zx2c4.com/wireguard-tools/plain/src/wg-quick/darwin.bash))
делает ровно то, что мы планируем, — это наш образец:

- default через туннель — **двумя половинками**, не трогая настоящий default:
  `route -q -n add -inet 0.0.0.0/1 -interface utunN` и `... 128.0.0.0/1 ...`;
- физический шлюз ищет разбором `netstat -nr -f inet`: берёт строки `default`,
  **пропуская** шлюзы вида `link#N` (это и есть чужие utun-default'ы);
- host-route до endpoint: `route -q -n add -inet <serverIP> -gateway <GATEWAY4>`,
  а при отсутствии шлюза — blackhole-заглушку;
- DNS — только через `networksetup`: снимок `-getdnsservers`/`-getsearchdomains`
  по каждому сервису, установка своих, восстановление исходных (слово `empty`,
  если не было);
- следит за сетью через `route -n monitor` и переставляет host-route endpoint'а
  при событиях RTM_*.

Мы повторяем эту схему 1:1 (в Go — exec системных утилит + AF_ROUTE-сокет, который
в вендорённом коде уже используется в `routineRouteListener`).

## 3. Архитектура

```
                        ┌───────────────────────────── user session ─────────────────────────────┐
                        │  ClaudeProxy.app (menu bar)                                            │
                        │   ├─ CoreProcess → claude-proxy-core (user)  ── control.sock (user)    │
                        │   │     прокси-режим: netstack + CONNECT :8118   (КАК СЕЙЧАС)          │
                        │   └─ VpnClient ───────────────┐                                        │
                        └───────────────────────────────┼────────────────────────────────────────┘
                                                        │ /var/run/claude-proxy-vpnd.sock
                                                        │ (JSON Lines, peercred-проверка uid)
┌─ system (root, launchd: com.claudeproxy.vpnd) ────────▼────────────────────────────────────────┐
│  claude-proxy-core -mode vpnd                                                                  │
│   ├─ tun.CreateTUN("utun", mtu) → utunN         (реальный интерфейс)                           │
│   ├─ device.NewDevice(utun, NewDefaultBind())   (тот же AmneziaWG, тот же BuildUAPI)           │
│   ├─ netcfg: ifconfig / route / networksetup    (фазы §4)                                      │
│   ├─ pf: anchor com.apple/250.ClaudeProxyVPN    (kill-switch §7)                               │
│   └─ state-файл + route monitor + watchdog      (§9)                                           │
└────────────────────────────────────────────────────────────────────────────────────────────────┘

Поток трафика (full-tunnel):
  любой процесс → routing table (0/1+128/1 → utunN) → utunN → awg device →
  UDP :51820 → host-route serverIP via 192.168.2.1 @ en0 → VPS → MASQUERADE ens1 → Internet
Исключения из туннеля: loopback; UDP до serverIP:port (сам транспорт WG);
  направленный LAN-трафик (узко, §7). Всё прочее мимо utun режет PF.
```

## 4. Пофазный план connect / teardown (точные команды)

Все значения в `<...>` демон подставляет из профиля и снимка сети. Пример ниже —
профиль `nl-hostkey`: serverIP `222.167.208.108`, порт `51820`, клиентский адрес
`10.77.0.2`, MTU `1420`, DNS `1.1.1.1 8.8.8.8`.

### Фаза 0 — preflight (ничего не меняем)

1. Физический шлюз и интерфейс — метод wg-quick: разбор `netstat -rn -f inet`,
   строки `default`, шлюз не `link#*`. Для самоконтроля: `route -n get -ifscope <if> default`.
2. Сервисы для DNS: `networksetup -listnetworkserviceorder` → карта
   «сервис → Device: enX»; активные сервисы без `*` из `networksetup -listallnetworkservices`.
3. Снимок DNS по каждому сервису:
   `networksetup -getdnsservers "<svc>"` и `-getsearchdomains "<svc>"`.
   Ответ «There aren't any DNS Servers set…» кодируем как `empty`
   (man networksetup: *«type "empty" in place of the DNS server names»* — проверено в man на этой машине).
4. Если default уже на чужом `utun*` — в `status` поднимаем предупреждение
   (double-VPN), но не блокируем: физический шлюз всё равно находится (см. 2.2).
5. **До любых изменений** — записать state-файл (§9) с фазой `preparing` и снимком.

### Фаза 1 — utun + устройство WG

```go
dev, err := tun.CreateTUN("utun", 1420)   // root; имя отдаст система
name, _ := dev.Name()                     // напр. "utun9" — дальше везде оно
wg := device.NewDevice(dev, conn.NewDefaultBind(), logger)
wg.IpcSet(profile.BuildUAPI(priv))        // БЕЗ изменений: allowed_ip=0.0.0.0/0 уже там
wg.Up()
```

Handshake проверяем как в прокси-режиме (`last_handshake_time_sec > 0`, таймаут 10 с) —
он идёт по текущему default-маршруту, система ещё не тронута. Не вышло —
`wg.Close()` и выходим: система в исходном состоянии.

### Фаза 2 — адрес интерфейса

```
/sbin/ifconfig utun9 inet 10.77.0.2/32 10.77.0.2 alias
/sbin/ifconfig utun9 up
```

(ровно так назначает адрес wg-quick darwin: utun — point-to-point, peer = свой адрес;
MTU уже выставлен CreateTUN'ом.)

### Фаза 3 — маршруты

Порядок важен: host-route до сервера — **до** перехвата default, иначе в зазоре
между двумя половинками WG-UDP уйдёт в собственный туннель и зациклится.

```
/sbin/route -q -n add -inet 222.167.208.108 -gateway 192.168.2.1
/sbin/route -q -n add -inet 0.0.0.0/1   -interface utun9
/sbin/route -q -n add -inet 128.0.0.0/1 -interface utun9
```

- `0/1 + 128/1` точнее, чем `0/0`, поэтому перекрывают системный default, не удаляя его —
  откат это просто `route delete` этих трёх строк, исходный default никто не трогал.
- Если у физического интерфейса нет шлюза (point-to-point) — fallback wg-quick:
  `route -q -n add -inet <serverIP> -interface <physIf>`; совсем нет пути — не поднимаемся.
- IPv6 **не** маршрутизируем (туннель v4-only). Утечку v6 закрывает PF (§7), которая
  блокирует весь inet6 — это осознанный fail-closed для v6.
- LAN остаётся доступен сам по себе: directly-connected `192.168.2.0/24 → en0`
  специфичнее наших `/1` (но проходимость решает PF, см. §7).

### Фаза 4 — DNS

Для каждого активного сервиса из снимка:

```
/usr/sbin/networksetup -setdnsservers "Wi-Fi" 1.1.1.1 8.8.8.8
```

(серверы — из `profile.dns`; они маршрутизируются в utun половинками `/1` — резолв
идёт через туннель). `searchdomains` не трогаем.

Альтернатива — `scutil` / SCDynamicStore (`State:/Network/Service/<id>/DNS`): не
персистентна (сама исчезает при ребуте), но требует постоянно живого издателя и
заметно больше кода; v2RayTun на этой машине делает именно так. Выбираем
`networksetup` — путь wg-quick, тривиальный exec, предсказуемое восстановление;
цену персистентности (резидуальный DNS после краха+ребута) гасит boot-recovery §9.

### Фаза 5 — kill-switch (PF)

Записать файл правил (§7) и:

```
/sbin/pfctl -a "com.apple/250.ClaudeProxyVPN" -f /var/db/claude-proxy/pf-rules.conf
/sbin/pfctl -E     # stderr: "pf enabled" + "Token : <число>" — токен сохранить в state
```

`-E` включает PF с reference count (man pfctl, проверено: *«Enable the packet filter
and increment the pf enable reference count»*) — мы не выключим PF под тем, кто его
тоже включил, и наоборот.

### Фаза 6 — рабочий режим

- `status-full` отдаёт state connected + счётчики (`IpcGet`, как сейчас).
- Route monitor: AF_ROUTE-сокет (паттерн `routineRouteListener` из tun_darwin.go) или
  exec `route -n monitor`; по RTM_* пересчитать физический шлюз и переставить
  host-route endpoint'а (смена Wi-Fi → новый gateway). Это тоже поведение wg-quick.
- Health: та же логика стухшего handshake, что в `control/daemon.go` (3 мин).

### Teardown (зеркально, в обратном порядке; каждый шаг идемпотентен)

```
/sbin/pfctl -a "com.apple/250.ClaudeProxyVPN" -F all      # снять правила якоря
/sbin/pfctl -X <token>                                     # отпустить reference
networksetup -setdnsservers "Wi-Fi" <снимок | empty>       # по каждому сервису
/sbin/route -q -n delete -inet 0.0.0.0/1   -interface utun9
/sbin/route -q -n delete -inet 128.0.0.0/1 -interface utun9
/sbin/route -q -n delete -inet 222.167.208.108
wg.Close()   // устройство + utun; остаточные маршруты через utun9 ядро удалит само
state-файл → фаза clean
```

Ошибка любого шага не прерывает teardown: выполняем все шаги, собираем ошибки в лог.
`route delete` по уже отсутствующему маршруту и `pfctl -F` по пустому якорю безвредны.

## 5. Протокол app ⇄ root-демон

**Решение: отдельный сокет, тот же формат.** Пользовательский `control.sock` и его
жизненный цикл (child-процесс приложения) не трогаем. Root-демон слушает свой сокет;
протокол — те же JSON Lines `{id, cmd, ...} → {id, ok, result|error}`, реализация
переиспользует `internal/control` (вынести общий сервер/диспетчер, два набора команд).
Так `docs/control-protocol.md` расширяется новой секцией, а не ломается.

- Сокет: `/var/run/claude-proxy-vpnd.sock`.
- Права: демон создаёт сокет, затем `chown <installUID> + chmod 0600` — подключиться
  может только установивший пользователь и root. Дополнительно на каждом accept —
  `getsockopt(LOCAL_PEERCRED)` (`unix.GetsockoptXucred` в Go) и сверка uid с
  `/var/db/claude-proxy/vpnd.conf` (пишется установщиком, root:wheel 0600). Двойной
  забор: права файла — от случайных, peercred — от обхода через наследование fd.
  Это важно: по сокету проходит приватный ключ.
- Команды:

```json
{ "id": 1, "cmd": "ping" }            → { "pong": true, "version": "<build>", "mode": "vpnd" }
{ "id": 2, "cmd": "connect-full",  "profile": <Profile>, "privateKey": "<base64>" }
{ "id": 3, "cmd": "disconnect-full" }
{ "id": 4, "cmd": "status-full" }     → State + поля: "utun": "utun9", "killSwitch": true,
                                         "dnsOverridden": true, "doubleVpnWarning": false
{ "id": 5, "cmd": "logs" }
```

- `version` в `ping` — защита от рассинхрона app/демон после обновления приложения:
  не совпало — UI предлагает переустановить хелпер (§6).
- Ключ, как и сейчас, живёт только в памяти демона на время сессии и не логируется.

Почему не XPC: XPC-доверие между ad-hoc приложением и демоном не верифицируемо
(нет стабильного code signing identity, `SecCodeCheckValidity` по Team ID невозможен) —
получили бы тот же uid-check, но с большим объёмом Swift/C-обвязки. Unix-сокет +
peercred даёт те же гарантии и уже обкатан в проекте.

## 6. Установка root-демона при ad-hoc подписи

### Почему не SMAppService

`SMAppService.daemon(plistName:)` (macOS 13+) — штатный путь, но он завязан на
валидную подпись: с ad-hoc («Sign to Run Locally») регистрация демонов блокируется
(`ServiceManagementMutationAdmission` отклоняет ad-hoc; обсуждения и подтверждения:
[Apple Forums — Privileged daemon appears as unsigned](https://developer.apple.com/forums/thread/757463),
[go-idavoll/idunn PR #41 — «ServiceManagementMutationAdmission blocks ad-hoc signing»](https://github.com/go-idavoll/idunn/pull/41),
[SwiftAuthorizationSample — «Sign to Run Locally is not supported»](https://github.com/trilemma-dev/SwiftAuthorizationSample)).
Вдобавок ad-hoc CDHash меняется при каждой пересборке — даже там, где регистрация
проходит, связка app↔demon разваливается при обновлении. Наше приложение подписано
ad-hoc (проверено, §2.3) → SMAppService отпадает.

### Выбранный способ: разовая установка LaunchDaemon через osascript

`launchd` не требует подписи от обычных LaunchDaemons — работает с любым бинарём,
лишь бы plist был `root:wheel 0644`, а бинарь невладимым для не-root. GUI-запрос
пароля администратора:

```swift
// из приложения, один раз (и при несовпадении version)
let script = "do shell script \"/bin/sh '\(installerPath)' install '\(coreBinPath)' \(getuid())\" " +
             "with administrator privileges with prompt \"Claude Proxy установит VPN-хелпер (нужен пароль администратора)\""
NSAppleScript(source: script)?.executeAndReturnError(&err)
```

`installer.sh install <src-binary> <uid>` (кладётся в бандл, выполняется под root):

```sh
set -eu
BIN=/Library/PrivilegedHelperTools/com.claudeproxy.vpnd
PLIST=/Library/LaunchDaemons/com.claudeproxy.vpnd.plist
mkdir -p /Library/PrivilegedHelperTools /var/db/claude-proxy
install -o root -g wheel -m 755 "$1" "$BIN"
printf 'allowedUid=%s\n' "$2" > /var/db/claude-proxy/vpnd.conf
chmod 600 /var/db/claude-proxy/vpnd.conf
cat > "$PLIST" <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>Label</key><string>com.claudeproxy.vpnd</string>
  <key>ProgramArguments</key><array>
    <string>/Library/PrivilegedHelperTools/com.claudeproxy.vpnd</string>
    <string>-mode</string><string>vpnd</string>
    <string>-sock</string><string>/var/run/claude-proxy-vpnd.sock</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
</dict></plist>
EOF
chown root:wheel "$PLIST"; chmod 644 "$PLIST"
launchctl bootout system/com.claudeproxy.vpnd 2>/dev/null || true
launchctl bootstrap system "$PLIST"
```

- `RunAtLoad=true` — обязателен: boot-recovery после краха+ребута (§9).
- `KeepAlive=true` — launchd поднимает демон после краша; свежезапущенный демон
  видит stale state-файл и чинит сеть (§9).
- Управление: `sudo launchctl kickstart -k system/com.claudeproxy.vpnd` (рестарт),
  `launchctl print system/com.claudeproxy.vpnd` (диагностика).
- Деинсталляция (`installer.sh uninstall`, тоже через osascript): демону —
  `disconnect-full`; `launchctl bootout system/com.claudeproxy.vpnd`; удалить plist,
  бинарь, `/var/db/claude-proxy`, сокет.
- Обновление: при несовпадении `version` из `ping` приложение повторяет install
  (тот же скрипт, bootout+bootstrap).

Осознанный компромисс (следствие ad-hoc, озвучить в README): в момент установки под
root копируется бинарь из пользовательской папки — цепочка доверия держится на том,
что пароль администратора вводит владелец машины для своего же бинаря. SMJobBless/
SMAppService защищают ровно от этого, но требуют Developer ID. После установки
эскалации нет: `/Library/PrivilegedHelperTools/...` — root:wheel 755, пользователю
не перезаписать.

## 7. Kill-switch: PF целиком

Точка подвеса — **суб-якорь под `com.apple/*`**: стоковый `/etc/pf.conf` содержит
`anchor "com.apple/*"` (проверено, §2.2), а по man pf.conf wildcard-якорь исполняет
все непосредственно подвешенные дочерние якоря. Значит, загрузка правил в
`com.apple/250.ClaudeProxyVPN` делает их активными **без правки main ruleset** —
приём, устоявшийся у сторонних kill-switch'ей
([ZorroVPN: pf vpn-only](https://zorrovpn.com/articles/osx-pf-vpn-only?lang=en),
[vpn-kill-switch/killswitch](https://github.com/vpn-kill-switch/killswitch)).
Загрузка/снятие атомарны на уровне якоря: `pfctl -a <anchor> -f file` заменяет весь
якорь целиком, `-F all` — целиком снимает. Число `250` ставит нас по алфавиту после
якорей Apple (`200.AirDrop`, `400.AdaptiveFirewall` и т.п.).

`/var/db/claude-proxy/pf-rules.conf` (root:wheel 0600; `$utun_if`, `$server_ip`,
`$server_port` демон подставляет при генерации):

```pf
# Claude Proxy Full VPN kill-switch. Снимается: pfctl -a "com.apple/250.ClaudeProxyVPN" -F all
# Направленный LAN/DHCP-трафик; 255.255.255.255 — DHCP-broadcast.
table <cpx_lan> const { 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, \
                        169.254.0.0/16, 224.0.0.0/4, 255.255.255.255/32 }

# По умолчанию: весь исходящий закрыт (inet и inet6 — v6-утечки гибнут здесь же).
block return out all

# Разрешения — только quick, по убыванию специфичности:
pass out quick on lo0 all
pass out quick on utun9 inet all keep state
pass out quick inet proto udp from any to 222.167.208.108 port = 51820 keep state
pass out quick inet to <cpx_lan> keep state
```

Разбор:

- `block return out all` — fail-closed по умолчанию, включая **весь IPv6** (туннель
  v4-only; это осознанная политика «нет v6, пока VPN включён»). `return`, а не `drop`:
  приложения получают мгновенный RST/unreachable вместо зависших таймаутов.
- `pass out quick on utun9` — единственный широкий выход: всё, что легло в туннель.
- UDP до `serverIP:51820` — транспорт самого WG на физическом интерфейсе (host-route
  из фазы 3 направляет его в en0; при смене сети правило продолжает работать —
  оно не привязано к имени интерфейса).
- `<cpx_lan>` — направленный LAN (принтеры, DHCP-renew: 68→67 на broadcast покрыт
  записью 255.255.255.255), mDNS (224.0.0.0/4). Это компромисс в духе «Allow LAN»
  у Mullvad; строгий режим (без LAN-строки) — флаг конфигурации демона.
- Входящие не трогаем: цель — анти-утечка исходящего; inbound закрывает NAT/stateful.
- Главный системный риск: якорная схема работает, пока main ruleset содержит
  `anchor "com.apple/*"`. Чужой софт, грузящий собственный main ruleset без этой
  строки (другой kill-switch), молча отключит наш. Митигировать дёшево: watchdog
  демона раз в 60 с проверяет `pfctl -sr | grep com.apple/*`-эквивалент
  (`pfctl -a com.apple/250.ClaudeProxyVPN -sr` на непустоту + наличие wildcard в main)
  и при пропаже перезагружает якорь и поднимает `lastError`.

Проверка на живой системе (ручной e2e, войдёт в приёмку):

```
curl -s ifconfig.me                         # = IP VPS
curl -s --interface en0 ifconfig.me         # должен УПАСТЬ (kill-switch)
curl -6 -s ifconfig.co                      # должен УПАСТЬ (v6 закрыт)
scutil --dns | head                         # resolver #1 = 1.1.1.1 через utunN
```

## 8. Сосуществование режимов в UI

- Один переключатель-сегмент: `Proxy` / `Full VPN` (+ Off). Включение Full VPN при
  активном прокси: app шлёт `disconnect` пользовательскому ядру, дожидается, затем
  `connect-full` демону. Обратно — зеркально. Причина запрета одновременности —
  не UI-вкус, а протокол WG: один ключ = один endpoint на сервере, двойная сессия
  флапает (§1). В `status-full` демон дополнительно отражает попытку двойного
  включения как ошибку, если app прислал `connect-full`, когда и user-core подключён?
  — демон этого не видит; забор держит только app. Зафиксировать в
  `docs/control-protocol.md` как известное ограничение.
- Если установлен хелпер, но пользователь им не пользуется — демон спит (KeepAlive
  держит процесс, но сеть не тронута; state=disconnected).
- Прокси-режим остаётся дефолтом и не требует пароля администратора вообще —
  установка хелпера предлагается только при первом включении Full VPN.

## 9. Безопасный teardown, краши, ребут

Инварианты: (1) любое изменение системы записано в state-файл **до** его выполнения;
(2) любой запуск демона начинается с выверки state-файла; (3) teardown идемпотентен.

**State-файл** `/var/db/claude-proxy/fullvpn-state.json` (root 0600, запись атомарно
через rename):

```json
{
  "phase": "clean | preparing | routed | dns-set | pf-set | connected | tearing-down",
  "utun": "utun9",
  "serverIP": "222.167.208.108", "serverPort": 51820,
  "origGateway": "192.168.2.1", "physIf": "en0",
  "dnsSnapshot": { "Wi-Fi": ["empty"], "USB 10/100/1000 LAN": ["empty"] },
  "pfToken": "4620695...", "anchorLoaded": true,
  "reconnectIntended": true, "strictKillSwitch": false
}
```

Сценарии:

- **Штатное выключение** (disconnect-full, SIGTERM от launchd): полный teardown §4,
  фаза `clean`.
- **Краш демона**: utun умирает вместе с процессом (fd закрыт → интерфейс и его
  маршруты убирает ядро — свойство NativeTun, §2.1). Остаются: половинки `/1`
  (умирают вместе с utun, т.к. привязаны к интерфейсу), host-route (висит), DNS
  (висит), PF-якорь (висит). Итог сам по себе fail-closed: PF пропускает только
  исчезнувший utun → сети нет. `KeepAlive` перезапускает демон, тот видит
  `phase != clean` и действует по флагу:
  - `strictKillSwitch=false` (дефолт): полный teardown по снимку → сеть
    восстановлена, state=error «VPN crashed, network restored».
  - `strictKillSwitch=true`: PF оставить, попытаться переподключиться
    (`reconnectIntended`); N неудач → остаться закрытым, ждать команды.
- **Ребут во включённом состоянии**: маршруты и PF-якорь не переживают ребут,
  pf-токен обнуляется; переживает только DNS (networksetup пишет в preferences).
  `RunAtLoad` поднимает демон → `phase != clean` → восстановить DNS из снимка,
  фаза `clean`. (Снимок DNS в state-файле обязателен ровно из-за этого случая.)
- **Watchdog** (горутина раз в 60 с при state=connected): жив ли utun
  (`net.InterfaceByName`), свеж ли handshake (3 мин, как в прокси), на месте ли
  PF-якорь (§7), на месте ли half-routes (`route -n get 1.1.1.1` → interface=utunN).
  Расхождение → лог + `lastError` + повторная установка недостающего слоя либо
  управляемый teardown по флагу.
- **Аварийный ручной откат** (документировать в usage.md):
  `sudo pfctl -a "com.apple/250.ClaudeProxyVPN" -F all; sudo pfctl -d;
  networksetup -setdnsservers Wi-Fi empty; sudo route delete 222.167.208.108` —
  возвращает сеть при любом состоянии демона.

## 10. Разбиение на задачи реализации

Порядок — от простого к сложному; каждая задача самостоятельна и завершается
работающим инкрементом. Тесты: юнит — на чистые функции (парсеры, генераторы команд
и правил) с fixture-выводами реальных утилит, включая негативные/краевые; e2e по
сети — ручной чек-лист (root + живой VPS, в CI не автоматизируется).

1. **Рефакторинг control: общий сервер под два набора команд.**
   Вынести из `internal/control` транспорт (listener, JSON Lines, dispatch-таблица);
   `-mode vpnd` в main.go поднимает vpnd-сокет с ping/logs-заглушками.
   Приёмка: существующие тесты зелёные; `claude-proxy-core -mode vpnd -sock /tmp/x.sock`
   отвечает на ping `{mode:"vpnd", version}`; обычный режим не изменился байт-в-байт
   по протоколу.
2. **Peercred-забор сокета.** chown/chmod сокета по `vpnd.conf`,
   `unix.GetsockoptXucred` на accept, отказ чужому uid.
   Приёмка: юнит с двумя uid (root/юзер — через сокетпару с подменой проверки);
   негативный тест: чужой uid получает отказ до чтения первой команды.
3. **netcfg: снимок сети (read-only).** Парсеры `netstat -rn -f inet` (пропуск
   `link#`, выбор физического default), `networksetup -listnetworkserviceorder`
   (сервис→device), `-getdnsservers` (включая «There aren't any…»→empty).
   Приёмка: юнит на fixture-выводах этой машины (из §2.2), краевые: чужой VPN держит
   default; сервис без устройства; выключенный сервис со звёздочкой.
4. **fullvpn: utun + WG-устройство + handshake.** `tun.CreateTUN` + ifconfig-фаза 2 +
   `device.NewDevice` + `BuildUAPI` (переиспользование) + ожидание handshake;
   чистый `Close`. Приёмка (ручная, root): `sudo ./core -mode vpnd ...`,
   connect-full → handshake, `ping 10.77.0.1` отвечает; disconnect — utun исчез.
5. **Маршруты + state-файл.** Фаза 3 и её teardown; state-файл с фазами, атомарная
   запись. Приёмка: юнит на генерацию/разбор команд и переходы фаз; ручная: egress
   IP = VPS, `route -n get 8.8.8.8` → utunN, после disconnect маршруты исходные
   (диф `netstat -rn` до/после пуст).
6. **DNS-менеджер.** Фаза 4 + восстановление, включая `empty` и несколько сервисов.
   Приёмка: юнит на fixture; ручная: `scutil --dns` показывает 1.1.1.1 при connect и
   исходное после disconnect; негатив: сервис исчез между снимком и restore — restore
   остальных не прерывается.
7. **PF kill-switch.** Генерация правил §7, `pfctl -a ... -f/-F`, `-E`/`-X` с парсом
   токена из stderr. Приёмка: юнит на рендер правил и парс токена (включая кривой
   stderr); ручная e2e: четыре проверки из §7 (egress, `--interface en0` падает,
   v6 падает, resolver туннельный), после disconnect `pfctl -s References` без нашего
   токена, якорь пуст.
8. **Crash-recovery + watchdog.** Логика §9: разбор stale state на старте, оба режима
   strict/non-strict, route monitor для host-route, 60-с проверки. Приёмка: юнит на
   таблицу «фаза × флаг → действия»; ручная: `sudo kill -9` демона при connected →
   launchd перезапустил → сеть восстановлена (non-strict) ≤10 с; ребут при connected →
   после входа DNS исходный.
9. **Установщик.** `installer.sh` (install/uninstall), plist, вызов из приложения
   через osascript, version-handshake. Приёмка: установка с нуля (один запрос пароля),
   `launchctl print system/com.claudeproxy.vpnd` = running; uninstall не оставляет
   файлов; обновление бинаря ловится по version и переустанавливается.
10. **UI + взаимное исключение.** VpnClient в Swift, сегмент Proxy/Full VPN/Off,
    принудительный disconnect встречного режима, статус (utun, kill-switch,
    double-VPN warning), onboarding установки хелпера. Приёмка: ручной чек-лист
    переключений во всех 6 переходах без осиротевших соединений.
11. **Документация.** Секция в `control-protocol.md` (vpnd-сокет и команды),
    обновление README/usage (аварийный откат из §9), build-notes (упаковка installer.sh
    и plist в бандл).

## 11. Открытые вопросы

1. **Политика при крахе: strict по умолчанию?** Дизайн предлагает non-strict
   (восстановить сеть, сообщить об ошибке) — это «классический VPN», а не паранойя;
   strict оставлен флагом. Нужно решение владельца.
2. **LAN-доступ при kill-switch** — разрешён по умолчанию (таблица `<cpx_lan>`).
   Альтернатива Mullvad-style «блокировать LAN, галочка Allow LAN» — вопрос UX.
3. **Двойной VPN** (у владельца фактически запущен v2RayTun): мы корректно находим
   физический шлюз и перехватываем default половинками, но смысловая картина «чей
   трафик куда» мутная. Предлагается: предупреждение в UI, без жёсткого запрета.
4. **IPv6 внутри туннеля** — сервер и профиль v4-only; если когда-нибудь добавится
   v6-адрес клиента, снимаются и v6-блок PF, и отсутствие v6-маршрутов (отдельная
   задача, сейчас вне скоупа).
5. **`/var/run` vs `/var/db`** для сокета: `/var/run` чистится на ребуте (плюс), но
   демон в любом случае пересоздаёт сокет на старте; альтернатив не требуется —
   подтвердить при реализации, что путь не конфликтует с sandbox-профилями системы.
