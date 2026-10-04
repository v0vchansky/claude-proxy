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
| Технический лог | `~/Library/Application Support/ClaudeProxy/diagnostics.log` |
| Control socket | `~/Library/Application Support/ClaudeProxy/control.sock` |
