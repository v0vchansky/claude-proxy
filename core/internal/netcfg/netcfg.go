// Package netcfg снимает read-only снимок сетевой конфигурации macOS,
// нужный для будущего режима full-tunnel VPN (docs/full-vpn-design.md §2, §4).
//
// Пакет ничего в системе не меняет: он только читает вывод системных
// утилит (netstat, networksetup) и разбирает его чистыми парсерами.
// Парсеры (parseDefaultRoute/parseServiceOrder/parseDNS) отделены от exec,
// чтобы тестироваться на зафиксированных fixtures из §2.2 без доступа к ОС.
package netcfg

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultRoute — физический default-маршрут IPv4.
//
// Важно: Gateway — именно физический next-hop (напр. 192.168.2.1), а не
// link#-шлюз чужого utun. См. parseDefaultRoute и §2.4 (эталон wg-quick).
type DefaultRoute struct {
	Gateway string // next-hop, напр. "192.168.2.1"
	Device  string // BSD-устройство, напр. "en0"
}

// Service — один сетевой сервис macOS и привязанное к нему устройство.
// Соответствует записи из `networksetup -listnetworkserviceorder`.
type Service struct {
	Name    string // имя сервиса, напр. "Wi-Fi"
	Device  string // BSD-устройство, напр. "en0"; пусто, если у сервиса его нет
	Enabled bool   // false, если сервис отключён (помечен "(*)" в выводе)
}

// Snapshot — read-only снимок сетевой конфигурации macOS.
type Snapshot struct {
	DefaultRoute DefaultRoute        // физический default-маршрут
	Services     []Service           // карта сервис → устройство + флаг включённости
	DNS          map[string][]string // сервис → его DNS-серверы (пусто = DHCP/не заданы)
}

// linkGatewayPrefix — префикс шлюза, который wg-quick darwin пропускает при
// поиске физического шлюза: это link#N (default чужого utun, не физический).
const linkGatewayPrefix = "link#"

// parseDefaultRoute достаёт физический шлюз и устройство из вывода
// `netstat -rn -f inet`.
//
// Метод wg-quick darwin (§2.4): среди строк с Destination == "default"
// пропускаем те, у кого Gateway вида link#N — это default чужого utun при
// активном стороннем VPN, а не реальный next-hop. Берём первую оставшуюся
// строку: её Gateway — физический шлюз, Netif — физическое устройство.
//
// Формат строки netstat: Destination  Gateway  Flags  Netif [Expire].
func parseDefaultRoute(netstatOutput string) (gateway, device string, err error) {
	for _, line := range strings.Split(netstatOutput, "\n") {
		fields := strings.Fields(line)
		// Нужны минимум Destination, Gateway, Flags, Netif.
		if len(fields) < 4 {
			continue
		}
		if fields[0] != "default" {
			continue
		}
		// Пропускаем link#-шлюзы (чужой utun держит свой default).
		if strings.HasPrefix(fields[1], linkGatewayPrefix) {
			continue
		}
		return fields[1], fields[3], nil
	}
	return "", "", fmt.Errorf("netcfg: физический default-маршрут не найден в выводе netstat")
}

// serviceHeaderRe — заголовок сервиса: "(1) Wi-Fi" или "(*) v2RayTun".
// Группа 1 — номер либо "*" (звёздочка = сервис отключён), группа 2 — имя.
var serviceHeaderRe = regexp.MustCompile(`^\((\*|\d+)\)\s+(.*\S)\s*$`)

// serviceDeviceRe — строка с устройством:
// "(Hardware Port: Wi-Fi, Device: en0)". Группа 1 — устройство (может быть пустой).
var serviceDeviceRe = regexp.MustCompile(`Device:\s*([^)]*)\)`)

// parseServiceOrder разбирает `networksetup -listnetworkserviceorder`.
//
// Вывод идёт парами строк: заголовок "(N) Имя" / "(*) Имя" и следом строка
// "(Hardware Port: ..., Device: enX)". Звёздочка в заголовке = сервис отключён.
// Device у некоторых сервисов отсутствует (пустая строка после "Device:").
func parseServiceOrder(output string) []Service {
	var services []Service
	var pending *Service // сервис, которому ещё не нашли устройство

	flush := func() {
		if pending != nil {
			services = append(services, *pending)
			pending = nil
		}
	}

	for _, line := range strings.Split(output, "\n") {
		if m := serviceHeaderRe.FindStringSubmatch(line); m != nil {
			// Новый заголовок — предыдущий сервис мог остаться без устройства.
			flush()
			pending = &Service{
				Name:    strings.TrimSpace(m[2]),
				Enabled: m[1] != "*",
			}
			continue
		}
		if pending != nil {
			if m := serviceDeviceRe.FindStringSubmatch(line); m != nil {
				pending.Device = strings.TrimSpace(m[1])
				flush()
			}
		}
	}
	flush()
	return services
}

// noDNSMarker — начало строки-ответа networksetup, когда DNS не заданы.
const noDNSMarker = "There aren't any"

// parseDNS разбирает `networksetup -getdnsservers <service>`.
//
// Либо список адресов (по одному в строке), либо ответ
// "There aren't any DNS Servers set on <service>." — в этом случае возвращаем
// пустой слайс (DNS берутся из DHCP, своих не задано).
func parseDNS(output string) []string {
	var addrs []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, noDNSMarker) {
			return nil
		}
		addrs = append(addrs, line)
	}
	return addrs
}
