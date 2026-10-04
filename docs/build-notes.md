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
