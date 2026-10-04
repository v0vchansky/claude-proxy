// Package vpnmut — чистые генераторы системных команд правки маршрутов и DNS
// для full-tunnel VPN, плюс state-файл фаз для crash-recovery (docs/full-vpn-design.md §4, §9).
//
// Функции НЕ выполняют команды: они возвращают argv (срез аргументов,
// где элемент [0] — путь к утилите), который демон-исполнитель передаёт в exec.
// Это разделяет генерацию (тестируется юнитами на fixture) и исполнение
// (интеграция/живой тест под root — отдельно).
package vpnmut

const (
	// routeBin — системная утилита маршрутизации macOS.
	routeBin = "/sbin/route"

	// half0 и half1 — две половинки default-маршрута (трюк wg-quick darwin, §2.4).
	// 0.0.0.0/1 + 128.0.0.0/1 покрывают весь 0.0.0.0/0, но каждая специфичнее
	// системного default, поэтому перекрывают его НЕ удаляя: откат — просто delete
	// этих строк, исходный default никто не трогал.
	half0 = "0.0.0.0/1"
	half1 = "128.0.0.0/1"
)

// BuildRouteUp возвращает команды фазы 3 (§4) в порядке применения.
//
// Порядок важен: host-route до сервера ставится ДО перехвата default, иначе в
// зазоре между двумя половинками WG-UDP уйдёт в собственный туннель и зациклится.
//
//	/sbin/route -q -n add -inet <serverIP> -gateway <gateway>
//	/sbin/route -q -n add -inet 0.0.0.0/1   -interface <utun>
//	/sbin/route -q -n add -inet 128.0.0.0/1 -interface <utun>
func BuildRouteUp(serverIP, gateway, utun string) [][]string {
	return [][]string{
		{routeBin, "-q", "-n", "add", "-inet", serverIP, "-gateway", gateway},
		{routeBin, "-q", "-n", "add", "-inet", half0, "-interface", utun},
		{routeBin, "-q", "-n", "add", "-inet", half1, "-interface", utun},
	}
}

// BuildRouteDown возвращает команды teardown'а фазы 3 (§4) — зеркало BuildRouteUp
// в обратном порядке: сперва снимаем перехват default, затем host-route сервера.
//
//	/sbin/route -q -n delete -inet 0.0.0.0/1   -interface <utun>
//	/sbin/route -q -n delete -inet 128.0.0.0/1 -interface <utun>
//	/sbin/route -q -n delete -inet <serverIP>
//
// Идемпотентно по смыслу: `route delete` по уже отсутствующему маршруту безвреден,
// поэтому teardown можно гонять повторно (crash-recovery). Для delete host-route
// шлюз не нужен (ядро находит запись по адресу назначения), поэтому gateway здесь
// не используется — параметр сохранён ради симметрии сигнатуры с BuildRouteUp.
func BuildRouteDown(serverIP, gateway, utun string) [][]string {
	_ = gateway // см. комментарий выше: delete адресуется по назначению
	return [][]string{
		{routeBin, "-q", "-n", "delete", "-inet", half0, "-interface", utun},
		{routeBin, "-q", "-n", "delete", "-inet", half1, "-interface", utun},
		{routeBin, "-q", "-n", "delete", "-inet", serverIP},
	}
}
