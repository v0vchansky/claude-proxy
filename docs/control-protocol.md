# Control protocol (app ⇄ core)

Ядро `claude-proxy-core` слушает **unix domain socket**:

```
~/Library/Application Support/ClaudeProxy/control.sock
```

Транспорт — **JSON Lines**: один JSON-объект на строку (`\n`), запрос → один ответ.
Соединение держится постоянным; приложение опрашивает `status` раз в ~2 с и шлёт команды по действию пользователя.

Ядро **не хранит** конфигурацию и ключи на диске. Приложение — источник правды:
активный профиль и приватный ключ клиента (из Keychain) передаются в команде `connect`/`switch`.
Приватный ключ живёт только в памяти ядра на время сессии и не логируется.

## Запрос

```json
{ "id": 1, "cmd": "<command>", ...params }
```

## Ответ

```json
{ "id": 1, "ok": true,  "result": { ... } }
{ "id": 1, "ok": false, "error": "human readable reason" }
```

## Команды

### `status`
Без параметров. Возвращает текущий `State` (см. ниже).

### `connect`
```json
{ "id": 2, "cmd": "connect", "profile": <Profile>, "privateKey": "<base64>" }
```
Поднимает туннель, ждёт handshake, запускает local proxy. Возвращает `State`
(уже `connected`, либо `error` с причиной). Долгая операция — ответ приходит по завершении.

### `disconnect`
Без параметров. Останавливает proxy и туннель. Возвращает `State` (`disconnected`).

### `switch`
```json
{ "id": 3, "cmd": "switch", "profile": <Profile>, "privateKey": "<base64>" }
```
Контролируемая смена сервера: перестать принимать соединения → закрыть туннель →
поднять новый → health check. При ошибке остаётся `error`, **без** прямого выхода в сеть.

### `healthcheck`
Без параметров. Выполняет проверку через туннель прямо сейчас, обновляет и возвращает `State`.

### `logs`
```json
{ "id": 4, "cmd": "logs" }
```
Возвращает `{ "lines": ["20:14:02 ...", ...] }` — технический лог (без секретов).

### `ping` (служебная)
Проверка, что ядро живо. Возвращает `{ "pong": true }`.

## Profile

```json
{
  "id": "nl-hostkey",
  "displayName": "Netherlands / HOSTKEY",
  "country": "Netherlands",
  "provider": "HOSTKEY",
  "host": "222.167.208.108",
  "port": 51820,
  "serverPublicKey": "WyhFpvvdzC2OBhpbcRAMzVZXsyWgToZ/4vtVkgBo4F4=",
  "clientVpnAddress": "10.77.0.2",
  "serverVpnAddress": "10.77.0.1",
  "dns": ["1.1.1.1", "8.8.8.8"],
  "mtu": 1420,
  "persistentKeepalive": 25,
  "jc": 5, "jmin": 50, "jmax": 1000,
  "s1": 64, "s2": 128, "s3": 0, "s4": 0,
  "h1": 1000001, "h2": 1000002, "h3": 1000003, "h4": 1000004
}
```

> `s3`/`s4` = 0: userspace-клиент (amneziawg-go v1.0.4) не умеет junk на cookie/transport
> пакетах; сервер приведён к `s3=0 s4=0`, иначе transport-пакеты не сходятся. Обфускацию
> держат `jc`/`s1`/`s2`/`h1..h4`.

## State

```json
{
  "state": "disconnected|connecting|connected|switching|error",
  "profileId": "nl-hostkey",
  "serverName": "Netherlands / HOSTKEY",
  "serverHost": "222.167.208.108",
  "serverPort": 51820,
  "localProxy": "127.0.0.1:8118",
  "pingMs": 47,
  "lastCheckUnix": 1690000000,
  "connectedSinceUnix": 1690000000,
  "lastHandshakeUnix": 1690000000,
  "rxBytes": 12345,
  "txBytes": 6789,
  "lastError": ""
}
```

`pingMs` = `-1`, если проверка не проходила/не прошла. `lastError` пустой, если ошибок нет.
