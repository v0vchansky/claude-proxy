# Заметки по сборке ядра

## Зависимости AmneziaWG / gvisor

Ядро использует userspace-реализацию `amneziawg-go` c netstack (gVisor) — другого
способа встроить AmneziaWG в процесс без правки системных маршрутов нет.

В `go.mod` два `replace`, оба обязательны:

```
replace gvisor.dev/gvisor => gvisor.dev/gvisor v0.0.0-20250503011706-39ed1f5ac29c
replace github.com/amnezia-vpn/amneziawg-go => ./third_party/amneziawg-go
```

**Почему gvisor переопределён.** `amneziawg-go v1.0.4` тянет
`gvisor@v0.0.0-20250606...` с ветки master, в которой НЕ коммитятся сгенерированные
файлы (их разворачивает Bazel: `MaskOf64`, `waiterList` и т.п.). Такой gvisor не
собирается `go build`. Версия `v0.0.0-20250503...` опубликована с ветки `go` (со всеми
генерёнными файлами) — та же, что использует upstream `wireguard-go`.

**Почему amnezia скопирован локально с патчем.** Майский gvisor отдаёт из
`channel.Endpoint.Read()` указатель `*stack.PacketBuffer` (nil = пусто), а июньский —
значение с методом `IsNil()`. `amneziawg-go v1.0.4` написан под июньский API и зовёт
`pkt.IsNil()`. В локальной копии `tun/netstack/tun.go` одна строка заменена на
`if pkt == nil {` — семантически эквивалентно. Тест-файлы зависимости удалены
(не нужны и ломают классификацию пакетов у go-tool).

Патч проверен сквозным тестом против живого VPS: handshake, egress через VPS,
fail-closed и восстановление — всё работает.

## Параметр S3/S4

`amneziawg-go v1.0.4` в uapi поддерживает только `s1/s2` (есть ещё `i1..i5`), но НЕ
`s3/s4`. Сервер приведён к `s3=0 s4=0`, иначе transport-пакеты не сходятся по размеру
и данные не идут (handshake при этом проходит). Обфускацию держат `jc/s1/s2/h1..h4`.

## Пересоздать копию amnezia (если удалили third_party)

```bash
GOMODCACHE=$(go env GOMODCACHE)
cp -R "$GOMODCACHE/github.com/amnezia-vpn/amneziawg-go@v1.0.4" core/third_party/amneziawg-go
chmod -R u+w core/third_party/amneziawg-go
find core/third_party/amneziawg-go -name '*_test.go' -delete
sed -i '' 's/if pkt\.IsNil() {/if pkt == nil {/' core/third_party/amneziawg-go/tun/netstack/tun.go
```

## Сборка

```bash
cd core && go build -o bin/claude-proxy-core .
./bin/claude-proxy-core -genkey        # сгенерировать клиентскую пару ключей
./bin/claude-proxy-core -sock /tmp/cpc.sock -proxy 127.0.0.1:8118 -verbose
```

## Тестовое ядро рядом с приложением — безопасно

Боевое ядро приложения слушает `127.0.0.1:8118`, сокет
`~/Library/Application Support/ClaudeProxy/control.sock`, журнал
`~/Library/Application Support/ClaudeProxy/diagnostics.log` (передаётся флагом `-log`).
Ручной запуск рядом с ним:

```bash
./bin/claude-proxy-core -sock /tmp/cpc-t.sock -proxy 127.0.0.1:8119 -log /tmp/cpc-t.log
```

- **Отдельный `-sock` и `-proxy`** — иначе конфликт с сокетом/портом приложения.
- **`-log`** — свой файл или не задавать вовсе: без `-log` proxy-режим пишет только в
  память (`logs` отдаёт кольцевой буфер). В боевой журнал тестовое ядро больше не
  пишет. vpnd без `-log` по-прежнему пишет в `diagnostics.log` в Application Support
  пользователя, под которым запущен (под root — `/var/root/...`), поэтому ручной
  `sudo … -mode vpnd` рядом с установленным демоном запускать тоже с `-log`.
- **Не подключать боевым ключом**, пока приложение подключено. Два процесса с одним
  ключом — один WG-peer с двух endpoint'ов: сервер перекидывает сессию, у обоих
  потери пакетов, ретрансмиты, DNS-таймауты. Ядро это запрещает само: `connect`/`switch`
  берут `flock` на `~/Library/Application Support/ClaudeProxy/locks/tunnel-<hash>.lock`
  (общий для всех `-sock`), второй процесс получает ошибку «Этот ключ уже используется
  другим процессом claude-proxy-core (pid N, сокет …)» и туннель не поднимает
  (docs/control-protocol.md, `connect`). Для тестов — отдельная пара ключей
  (`-genkey`) и отдельный peer на сервере. vpnd держит такой же лок на свой ключ в
  `/var/lib/claude-proxy/locks`.
- **Гасить после теста.** Приложение при старте ищет посторонние `claude-proxy-core`
  текущего пользователя (кроме vpnd и своего дочернего): о каждом — предупреждение в
  UI, а ядро из бандла или `core/bin`, которое подключено или занимает 8118/наш
  сокет, завершает (SIGTERM, через 2 с SIGKILL).

## Тесты

```bash
cd core && go test ./... && go vet ./...
cd app && swift build -c release && swift test   # swift test требует Xcode (XCTest)
```
