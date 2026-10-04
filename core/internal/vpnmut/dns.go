package vpnmut

import "sort"

const (
	// networksetupBin — утилита управления сетевыми настройками macOS.
	networksetupBin = "/usr/sbin/networksetup"

	// dnsEmpty — ровно это слово ждёт `networksetup -setdnsservers`, чтобы
	// очистить список DNS у сервиса (man networksetup, §4 фаза 4). Им же
	// восстанавливаем сервис, у которого изначально DNS не было.
	dnsEmpty = "empty"
)

// BuildDNSSet возвращает команды фазы 4 (§4): для каждого активного сервиса —
// установка наших DNS-серверов (из profile.dns). searchdomains не трогаем.
//
//	/usr/sbin/networksetup -setdnsservers "<service>" <dns...>
//
// Порядок команд повторяет порядок services. Если dns пуст — подставляем `empty`
// (пустой список серверов для -setdnsservers недопустим, см. dnsEmpty).
func BuildDNSSet(services []string, dns []string) [][]string {
	cmds := make([][]string, 0, len(services))
	for _, svc := range services {
		cmd := []string{networksetupBin, "-setdnsservers", svc}
		if len(dns) == 0 {
			cmd = append(cmd, dnsEmpty)
		} else {
			cmd = append(cmd, dns...)
		}
		cmds = append(cmds, cmd)
	}
	return cmds
}

// BuildDNSRestore возвращает команды восстановления DNS из снимка orig
// (сервис → исходные серверы). Пустой список серверов → слово `empty`
// (сервис, у которого DNS изначально не было). Снимок, в котором значение уже
// хранится как ["empty"], проходит насквозь и даёт тот же результат.
//
// Сервисы обходятся в отсортированном порядке — вывод детерминирован (ключи map
// иначе перебираются в случайном порядке), что важно и для тестов, и для
// предсказуемого teardown. Исчезновение сервиса между снимком и реальным restore
// на генерацию не влияет: здесь мы лишь строим argv, независимо по каждому ключу,
// а сбой exec'а одного сервиса (его уже нет) не затронет команды остальных.
func BuildDNSRestore(orig map[string][]string) [][]string {
	services := make([]string, 0, len(orig))
	for svc := range orig {
		services = append(services, svc)
	}
	sort.Strings(services)

	cmds := make([][]string, 0, len(services))
	for _, svc := range services {
		cmd := []string{networksetupBin, "-setdnsservers", svc}
		if len(orig[svc]) == 0 {
			cmd = append(cmd, dnsEmpty)
		} else {
			cmd = append(cmd, orig[svc]...)
		}
		cmds = append(cmds, cmd)
	}
	return cmds
}
