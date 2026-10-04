# ТЗ: Claude Proxy для macOS

## 1. Цель

Разработать небольшое нативное macOS-приложение, которое работает из menu bar и предоставляет локальный HTTP/HTTPS proxy для Claude Code.

Приложение должно проксировать трафик через userspace-туннель AmneziaWG до выбранного VPS.

Приложение не должно менять системные маршруты macOS и не должно включать системный VPN для работы режима Claude Proxy.

Архитектура:

```text
Claude Code
    ↓
HTTP_PROXY / HTTPS_PROXY
127.0.0.1:8118
    ↓
Claude Proxy
    ↓
userspace AmneziaWG tunnel
    ↓
выбранный VPS
    ↓
Internet
```

Обязательное поведение: fail-closed.

Если туннель недоступен, local proxy не должен отправлять трафик напрямую через обычное интернет-соединение macOS.

---

## 2. Текущий статус инфраструктуры

### 2.1. VPS

Текущий сервер:

```text
Provider: HOSTKEY
OS: Ubuntu 24.04.5 LTS
Kernel: 6.8.0-146-generic
Public IPv4: 222.167.208.108
External interface: ens1
AmneziaWG UDP port: 51820
VPN subnet: 10.77.0.0/24
Server VPN address: 10.77.0.1/24
```

Server public key:

```text
WyhFpvvdzC2OBhpbcRAMzVZXsyWgToZ/4vtVkgBo4F4=
```

Текущие параметры AmneziaWG:

```text
Jc = 5
Jmin = 50
Jmax = 1000

S1 = 64
S2 = 128
S3 = 32
S4 = 16

H1 = 1000001
H2 = 1000002
H3 = 1000003
H4 = 1000004

RandomTrailers = off
DisableCookies = off
```

На сервере уже выполнено:

```text
[x] Установлен amneziawg
[x] Загружен kernel module amneziawg
[x] Создан интерфейс awg0
[x] awg0 имеет адрес 10.77.0.1/24
[x] AmneziaWG слушает UDP 51820
[x] Включен net.ipv4.ip_forward = 1
[x] Установлен nftables
[x] Настроен NAT/MASQUERADE 10.77.0.0/24 -> ens1
[x] Настроен forwarding awg0 <-> ens1
[x] Сгенерирован серверный private/public key
```

Текущий nftables:

```text
table inet filter {
    chain forward {
        type filter hook forward priority filter; policy drop;
        iifname "awg0" oifname "ens1" accept
        iifname "ens1" oifname "awg0" ct state established,related accept
    }
}

table ip nat {
    chain postrouting {
        type nat hook postrouting priority srcnat; policy accept;
        oifname "ens1" ip saddr 10.77.0.0/24 masquerade
    }
}
```

Что еще не завершено на сервере:

```text
[ ] Добавление клиентского peer
[ ] Постоянный конфиг peer
[ ] Автозапуск awg0 после reboot
[ ] Проверка handshake с клиентом
[ ] Проверка выхода клиента в Internet через VPS
```

### 2.2. Клиент

Клиентское приложение пока не реализовано.

Текущий статус:

```text
[ ] macOS menu bar app
[ ] local HTTP/HTTPS proxy
[ ] userspace AmneziaWG client
[ ] client keypair generation
[ ] health checks
[ ] fail-closed networking
[ ] server switching
[ ] logs
[ ] launch at login
```

Планируемый client VPN address для первого сервера:

```text
10.77.0.2/32
```

Private key клиента должен генерироваться локально при первом запуске и не покидать устройство.

Public key клиента должен быть доступен в UI для копирования и последующего добавления на VPS как peer.

---

## 3. Требования к приложению

### 3.1. Формат

Нативное macOS-приложение.

Приложение должно жить в menu bar рядом с Wi-Fi, батареей и другими системными индикаторами.

После запуска основное окно приложения отдельно не показывается.

По нажатию на иконку открывается компактный popover.

---

## 4. Основной UI

Пример:

```text
┌──────────────────────────────┐
│ Claude Proxy                 │
│                              │
│ Proxy                 [ ON ] │
│                              │
│ ● Connected                  │
│ Ping: 47 ms                  │
│ Checked: 8 sec ago           │
│ Connected for: 3h 17m        │
│                              │
│ Server                       │
│ Netherlands / HOSTKEY     ▼  │
│                              │
│ 222.167.208.108:51820        │
│                              │
│ Local proxy                  │
│ 127.0.0.1:8118               │
│                              │
│ [        Обновить         ]  │
│                              │
│ Last error: —                │
└──────────────────────────────┘
```

---

## 5. Главный switch Proxy

### ON

При включении:

1. Получить активный server profile.
2. Поднять userspace AmneziaWG tunnel.
3. Выполнить handshake с выбранным VPS.
4. Проверить работоспособность туннеля.
5. Поднять local HTTP/HTTPS proxy на:

```text
127.0.0.1:8118
```

6. Весь трафик, поступивший на local proxy, отправлять только через userspace AmneziaWG stack.
7. Показать статус Connected.

Состояния:

```text
Disconnected
Connecting
Connected
Error
```

### OFF

При выключении:

1. Перестать принимать новые proxy connections.
2. Закрыть активные proxy sessions.
3. Остановить userspace tunnel.
4. Освободить local proxy port.
5. Показать Disconnected.

---

## 6. Local HTTP/HTTPS proxy

Адрес по умолчанию:

```text
http://127.0.0.1:8118
```

Минимально необходимо поддержать HTTP CONNECT для HTTPS-трафика.

Приложение не должно выполнять TLS MITM.

Не нужны локальные сертификаты и расшифровка HTTPS.

Claude Code должен запускаться, например, так:

```bash
HTTP_PROXY=http://127.0.0.1:8118 \
HTTPS_PROXY=http://127.0.0.1:8118 \
claude
```

Добавить кнопку:

```text
Copy Claude command
```

Она копирует готовую команду запуска.

---

## 7. Fail-closed

Обязательное требование.

Запрещенная схема:

```text
AmneziaWG failed
    ↓
обычный net.Dial()
    ↓
en0 / Wi-Fi
    ↓
Internet
```

Допустимая схема:

```text
AmneziaWG failed
    ↓
proxy request fails
```

Local proxy не должен иметь прямой fallback на системную сеть.

Если туннель отсутствует, запрос должен завершаться ошибкой.

---

## 8. AmneziaWG client

AmneziaWG должен работать внутри приложения в userspace.

Предпочтительная архитектура:

```text
HTTP CONNECT proxy
    ↓
userspace TCP/IP stack
    ↓
userspace AmneziaWG
    ↓
UDP socket до VPS
```

Не использовать системный full-tunnel для режима Claude Proxy.

Не изменять глобальную таблицу маршрутизации macOS.

Не менять системный proxy macOS.

---

## 9. Key management

При первом запуске:

1. Проверить наличие client private key.
2. Если ключа нет — сгенерировать новую пару ключей.
3. Private key сохранить локально.
4. Public key показать в Settings.
5. Добавить кнопку Copy Public Key.

Private key не должен логироваться.

Private key не должен отправляться на сервер.

Для каждого server profile допускается отдельный VPN IP, но одна client keypair может использоваться на нескольких серверах, если это явно задано конфигурацией.

---

## 10. Health check

Приложение должно показывать:

```text
Status
Ping
Last check
Connected duration
Last error
```

Кнопка:

```text
Обновить
```

выполняет новую проверку.

Проверка должна идти через userspace-туннель.

Не использовать обычный системный маршрут macOS для health check.

Если проверка не проходит:

```text
Status: Error
Ping: —
Last error: <конкретная причина>
```

Примеры ошибок:

```text
Handshake timeout
Server unreachable
Tunnel not initialized
DNS resolution failed
Proxy port unavailable
```

---

## 11. Логика смены сервера

В UI должен быть selectable server profile.

Пример:

```text
Server
Netherlands / HOSTKEY     ▼
```

При нажатии открывается список:

```text
● Netherlands / HOSTKEY
  Germany / Backup
  Add server...
```

### Server profile

Каждый профиль должен содержать:

```text
id
displayName
country
provider
host
port
serverPublicKey
clientVpnAddress
serverVpnAddress
Jc
Jmin
Jmax
S1
S2
S3
S4
H1
H2
H3
H4
```

Для текущего профиля:

```text
displayName: Netherlands / HOSTKEY
country: Netherlands
provider: HOSTKEY
host: 222.167.208.108
port: 51820

serverPublicKey:
WyhFpvvdzC2OBhpbcRAMzVZXsyWgToZ/4vtVkgBo4F4=

serverVpnAddress: 10.77.0.1
clientVpnAddress: 10.77.0.2

Jc: 5
Jmin: 50
Jmax: 1000

S1: 64
S2: 128
S3: 32
S4: 16

H1: 1000001
H2: 1000002
H3: 1000003
H4: 1000004
```

### Переключение сервера при Proxy = OFF

Просто сохранить выбранный server profile как active.

Следующее включение Proxy использует новый сервер.

### Переключение сервера при Proxy = ON

Переключение должно происходить контролируемо:

```text
1. UI -> Switching...
2. Перестать принимать новые proxy connections.
3. Закрыть текущий tunnel.
4. Загрузить новый server profile.
5. Поднять новый AmneziaWG tunnel.
6. Выполнить health check.
7. Если успешно:
      восстановить local proxy
      status = Connected
8. Если ошибка:
      local proxy не должен уходить в direct mode
      status = Error
```

Никакого прямого fallback через домашний интернет.

### Failover

Автоматический failover между серверами не требуется для MVP.

Архитектура должна позволять добавить его позже.

---

## 12. Server management UI

Добавить отдельный Settings / Servers screen.

Функции:

```text
Add server
Edit server
Delete server
Set as active
Test connection
```

Для MVP допустимо хранить server profiles локально в JSON/plist.

Не нужно создавать backend или cloud sync.

---

## 13. Status data в popover

Показывать:

```text
Server name
Server host
Connection state
Ping
Last health check
Connection uptime
Local proxy address
Last error
```

Опционально:

```text
Uploaded bytes
Downloaded bytes
```

Traffic counters можно реализовать после основной функциональности.

---

## 14. Menu bar icon

Состояние желательно отражать иконкой:

```text
Disconnected -> нейтральная
Connecting   -> промежуточная
Connected    -> активная
Error        -> error state
```

Не требуется сложная анимация.

---

## 15. Автозапуск

Settings:

```text
[ ] Launch at login
[ ] Enable proxy on launch
```

Launch at login запускает приложение.

Enable proxy on launch автоматически подключается к последнему выбранному server profile.

---

## 16. Diagnostics / Logs

Хранить короткий локальный технический лог.

Пример:

```text
20:14:02 App started
20:14:03 Selected server: Netherlands / HOSTKEY
20:14:03 Tunnel starting
20:14:04 Handshake established
20:14:04 Proxy listening on 127.0.0.1:8118
20:15:10 Health check: 46 ms
20:17:22 Proxy CONNECT opened
```

Не логировать:

```text
private keys
proxy payload
HTTPS contents
authorization headers
cookies
```

Добавить кнопку:

```text
Copy diagnostics
```

---

## 17. Что не входит в MVP

Не реализовывать сейчас:

```text
system-wide VPN
macOS Network Extension
изменение системных routes
изменение системного proxy
browser extension
автоматический failover
VLESS
REALITY
Hysteria2
cloud backend
user accounts
sync
graphs
auto-update
```

---

## 18. Этапы реализации

### Stage 1

Сделать menu bar app и UI без networking.

### Stage 2

Реализовать:

```text
server profiles
key generation
settings
logging
```

### Stage 3

Реализовать userspace AmneziaWG connection.

Добиться handshake с:

```text
222.167.208.108:51820
```

### Stage 4

Добавить клиента как peer на VPS.

Проверить:

```text
client -> tunnel -> VPS
```

### Stage 5

Подключить userspace TCP/IP stack.

Проверить исходящий TCP request через tunnel.

### Stage 6

Добавить HTTP CONNECT proxy на:

```text
127.0.0.1:8118
```

Проверить:

```text
curl -> local proxy -> AWG -> VPS -> Internet
```

### Stage 7

Проверить Claude Code:

```text
Claude Code
    ↓
localhost:8118
    ↓
Claude Proxy
    ↓
AmneziaWG
    ↓
VPS
```

### Stage 8

Проверить fail-closed:

1. Proxy ON.
2. Убедиться, что запросы работают.
3. Оборвать AWG tunnel.
4. Убедиться, что запросы перестали работать.
5. Убедиться, что direct connection через macOS не появился.

### Stage 9

Реализовать смену server profile без direct fallback.

---

## 19. Сервер: что нужно доделать после появления клиента

После того как клиент сгенерирует public key:

```text
1. Добавить client public key как peer awg0.
2. Allowed IP:
   10.77.0.2/32

3. Проверить:
   awg show awg0

4. Проверить handshake.
5. Проверить RX/TX.
6. Проверить Internet egress.
7. Перенести конфигурацию awg0 в постоянный startup config.
8. Настроить автоматический запуск awg0 после reboot.
9. Проверить reboot VPS.
```

До появления client public key серверный peer создавать не нужно.

---

## 20. Definition of Done для MVP

MVP считается готовым, когда:

```text
[x] Приложение запускается как macOS menu bar app.
[x] Можно выбрать server profile.
[x] Можно включить Proxy.
[x] Устанавливается AmneziaWG tunnel.
[x] Local proxy слушает только 127.0.0.1.
[x] HTTPS CONNECT работает.
[x] Claude Code может работать через localhost proxy.
[x] Весь proxy traffic идет через AWG.
[x] При падении AWG direct fallback отсутствует.
[x] UI показывает connection status.
[x] UI показывает ping.
[x] UI показывает last check.
[x] Работает Refresh.
[x] Работает смена сервера.
[x] Есть client public key copy.
[x] Есть Copy Claude command.
[x] Есть diagnostics log.
```
