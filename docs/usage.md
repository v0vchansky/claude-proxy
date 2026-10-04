# Claude Proxy — установка и использование

## Сборка

```bash
cd ~/Desktop/claude-proxy
bash scripts/build-app.sh          # соберёт dist/ClaudeProxy.app
open dist/ClaudeProxy.app
```

Требования: macOS 14+, установленные Go (1.24+) и Xcode toolchain (Swift 5.9+).

## Первый запуск

1. Приложение появляется в menu bar (иконка-щит). Иконки Dock нет.
2. При первом запуске генерируется клиентская пара ключей; приватный ключ кладётся
   в Keychain и не покидает устройство. Публичный ключ доступен в popover по кнопке
   **Copy Public Key** и дублируется в файл
   `~/Library/Application Support/ClaudeProxy/client-public.key`.
3. Публичный ключ нужно один раз добавить на VPS как peer (см. `docs/server-runbook.md`).
   Для текущего VPS это уже сделано для ключа установленного инстанса.

## Включение прокси

- В popover переключатель **Proxy → ON**: поднимается туннель AmneziaWG, затем
  локальный прокси `127.0.0.1:8118`. Статус показывает Connected, ping, время проверки,
  аптайм.
- Кнопка **Обновить** — повторная проверка через туннель.

## Запуск Claude Code через прокси

Кнопка **Copy Claude command** копирует готовую строку:

```bash
HTTP_PROXY=http://127.0.0.1:8118 HTTPS_PROXY=http://127.0.0.1:8118 claude
```

Весь HTTPS-трафик Claude Code идёт через CONNECT → AmneziaWG → VPS. TLS не
расшифровывается (нет MITM, не нужны локальные сертификаты).

## Fail-closed

Если туннель недоступен, прокси возвращает ошибку (502), а не выходит в обычную сеть.
Прямого fallback на Wi-Fi/en0 в коде нет: прокси умеет звонить наружу только через
туннельный диалер.

## Серверы

Popover → **Servers…**: добавить/редактировать/удалить профиль, сделать активным,
проверить соединение. Профили хранятся в
`~/Library/Application Support/ClaudeProxy/servers.json` (без приватного ключа).
Смена сервера при включённом прокси идёт контролируемо: Switching → новый туннель →
health check; при ошибке остаётся Error без прямого выхода.

> S3/S4 в профиле держите = 0: userspace-клиент (amneziawg-go) их не поддерживает,
> сервер приведён к тем же значениям. Обфускацию держат Jc/S1/S2/H1..H4.

## Автозапуск

Popover:
- **Launch at login** — регистрирует приложение в автозапуске (SMAppService).
  Надёжнее работает для подписанного приложения в `/Applications`.
- **Enable proxy on launch** — при старте автоматически подключается к активному серверу.

## Диагностика

Кнопка **Copy diagnostics** копирует технический лог (без ключей, payload и заголовков).
```
App started
Selected server: Netherlands / HOSTKEY
Tunnel starting
Handshake established
Proxy listening on 127.0.0.1:8118
Health check: 46 ms
Proxy CONNECT opened api.anthropic.com:443
```
