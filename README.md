# Claude Proxy (macOS)

Menu bar приложение для macOS: локальный HTTP/HTTPS proxy для Claude Code,
трафик которого идёт через userspace-туннель AmneziaWG до выбранного VPS.
Fail-closed: нет туннеля — нет соединения, прямого выхода в системную сеть не существует.

ТЗ: `docs/spec.md`.

## Раскладка

```
core/   — ядро на Go (claude-proxy-core): AmneziaWG userspace + netstack + HTTP CONNECT proxy + control socket
app/    — menu bar UI на SwiftUI (MenuBarExtra)
docs/   — спека, протокол управления, серверные процедуры
scripts/— сборка .app, упаковка ядра в бандл
```

## Архитектура

```
Claude Code
  │  HTTP_PROXY/HTTPS_PROXY=http://127.0.0.1:8118
  ▼
core: HTTP CONNECT proxy (127.0.0.1:8118)
  │  только tnet.DialContext — обычного net.Dial в прокси-пути нет
  ▼
gVisor netstack (userspace TCP/IP)
  ▼
amneziawg-go device (Jc/S/H из профиля)
  ▼
UDP socket → VPS 222.167.208.108:51820 → Internet
```

Swift-приложение держит UI и ключи (приватный ключ клиента — в Keychain),
ядро — сеть. Общение по unix-сокету, протокол в `docs/control-protocol.md`.

## Где что лежит (не в репо)

| Что | Где |
|---|---|
| Приватный ключ клиента AmneziaWG | Keychain (генерится при первом запуске, не покидает устройство) |
| Server profiles | `~/Library/Application Support/ClaudeProxy/servers.json` |
| Технический лог | в памяти ядра (Copy diagnostics), наружу — без ключей/payload |
| Control socket | `~/Library/Application Support/ClaudeProxy/control.sock` |

## Статус MVP (Definition of Done, §20 ТЗ)

Проверено сквозняком на живом VPS `222.167.208.108`.

- [x] Запускается как macOS menu bar app (без иконки Dock)
- [x] Выбор server profile (picker + экран Servers)
- [x] Включение Proxy
- [x] Поднимается туннель AmneziaWG (handshake подтверждён)
- [x] Local proxy слушает только `127.0.0.1`
- [x] HTTPS CONNECT работает
- [x] Claude Code работает через `localhost:8118` (api.anthropic.com доступен через туннель)
- [x] Весь proxy-трафик идёт через AWG (egress = IP VPS)
- [x] При падении AWG нет direct fallback (fail-closed, проверено обрывом peer)
- [x] UI: connection status, ping, last check, uptime, last error
- [x] Работает Обновить (health check через туннель)
- [x] Работает смена сервера (контролируемо, без direct fallback)
- [x] Copy Public Key
- [x] Copy Claude command
- [x] Diagnostics log (Copy diagnostics)

Сервер (§19): peer клиента добавлен, `S3/S4=0`, `awg-quick@awg0` в автозапуске,
NAT/forward и `ip_forward` персистентны, ребут VPS проверен. Подробности —
`docs/server-runbook.md`, использование — `docs/usage.md`, сборка — `docs/build-notes.md`.
