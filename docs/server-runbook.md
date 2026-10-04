# Серверный runbook (VPS)

VPS: `222.167.208.108`, Ubuntu 24.04, AmneziaWG на `awg0` (10.77.0.1/24), UDP 51820.
Доступ по SSH под root (пароль у владельца; в репозиторий не кладётся).

## Текущее состояние (выполнено)

- `awg0` под управлением systemd, автозапуск включён:
  `systemctl enable --now awg-quick@awg0` → `enabled` + `active`.
- Конфиг `/etc/amnezia/amneziawg/awg0.conf` приведён к `S3 = 0`, `S4 = 0`
  (userspace-клиент не поддерживает s3/s4; при ненулевых значениях transport-пакеты
  не сходятся и данные не идут).
- Клиентский peer добавлен (live + в конфиге), AllowedIPs `10.77.0.2/32`.
- `net.ipv4.ip_forward=1` персистентно (`/etc/sysctl.d/99-amneziawg.conf`).
- nftables: NAT `10.77.0.0/24 → ens1` + forward awg0↔ens1, автозапуск `nftables` включён.
- Проверено: ребут VPS — всё поднимается само, egress клиента восстанавливается.

## Добавить нового клиента (его публичный ключ)

Публичный ключ клиента — из popover приложения (**Copy Public Key**) или из файла
`~/Library/Application Support/ClaudeProxy/client-public.key`.

```bash
CLIENT_PUB="<base64 публичный ключ>"
# применить вживую
awg set awg0 peer "$CLIENT_PUB" allowed-ips 10.77.0.2/32
# сохранить в постоянный конфиг
cd /etc/amnezia/amneziawg
printf '\n[Peer]\nPublicKey = %s\nAllowedIPs = 10.77.0.2/32\n' "$CLIENT_PUB" >> awg0.conf
awg-quick strip awg0 >/dev/null && echo "conf OK"
```

> Один `10.77.0.2/32` — для одного активного клиента. Для нескольких клиентов выдавайте
> разные адреса (10.77.0.3/32, …) и правьте `clientVpnAddress` в профиле приложения.

## Проверки

```bash
awg show awg0                 # интерфейс, параметры, peers, RX/TX, последний handshake
awg show awg0 peers           # список peer'ов
systemctl status awg-quick@awg0
nft list ruleset              # NAT/forward
sysctl net.ipv4.ip_forward    # = 1
```

Признак рабочего клиента: у peer растут `transfer: ... received / ... sent` и свежий
`latest handshake`.

## Проверка egress (с клиента)

```bash
curl -x http://127.0.0.1:8118 https://api.ipify.org    # должен вернуть 222.167.208.108
```

## После reboot VPS

Ничего делать не нужно — `awg-quick@awg0` и `nftables` стартуют автоматически.
Клиент восстанавливает соединение сам (persistent keepalive).
