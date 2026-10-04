package vpnd

import (
	"fmt"

	"github.com/v0vchansky/claude-proxy/core/internal/killswitch"
	"github.com/v0vchansky/claude-proxy/core/internal/vpnmut"
)

// ifconfigBin — утилита назначения адреса интерфейса (фаза 2 §4). Абсолютный путь
// намеренно: демон root, фиксированный путь исключает перехват через PATH.
const ifconfigBin = "/sbin/ifconfig"

// routeBin дублирует путь из vpnmut для прямых вызовов watchdog (route -n get/add).
const routeBin = "/sbin/route"

// pfctlBin дублирует путь из killswitch для прямого вызова watchdog (pfctl -a ... -sr).
const pfctlBin = "/sbin/pfctl"

// cmdKind различает шаги плана: обычный exec, загрузка PF-правил (помечаем
// anchorLoaded после успеха), включение PF (парсим reference-токен из stderr).
type cmdKind int

const (
	cmdPlain    cmdKind = iota // обычная команда; результат не разбираем
	cmdPFLoad                  // pfctl -a ... -f - (правила на stdin) → anchorLoaded=true
	cmdPFEnable                // pfctl -E → распарсить Token из stderr
)

// plannedCmd — один шаг connect-плана: argv + (опц.) stdin + фаза, в которую
// переходит state ПЕРЕД выполнением шага (§9: пишем state до изменения системы).
type plannedCmd struct {
	name  string
	phase vpnmut.Phase
	kind  cmdKind
	argv  []string
	stdin string
}

// buildConnectPlan — ЧИСТАЯ функция: по известному имени utun и параметрам сети
// собирает упорядоченный план системных команд фаз 2–5 (§4), БЕЗ фазы 1 (открытие
// utun — не exec, делается отдельно до плана). Порядок:
//
//	фаза 2  ifconfig <utun> inet <client>/32 <client> alias   (peer == свой адрес, point-to-point)
//	        ifconfig <utun> up
//	фаза 3  route add host-route серверу → half0 → half1      (host-route ДО перехвата default)
//	фаза 4  networksetup -setdnsservers <svc> <dns...>        (по каждому активному сервису)
//	фаза 5  pfctl -a <anchor> -f -   (правила на stdin)       → anchorLoaded
//	        pfctl -E                                           → reference-токен
//
// teardown этих шагов — buildTeardownCommands (зеркально); адрес utun и half-routes
// снимать не нужно — они исчезают вместе с utun при fullvpn.Close (§2.1).
func buildConnectPlan(utun, serverIP, gateway, clientVPN string, dnsServices, dnsServers []string, pfRules string) []plannedCmd {
	var plan []plannedCmd

	// Фаза 2 — адрес интерфейса.
	plan = append(plan,
		plannedCmd{name: "ifconfig inet", phase: vpnmut.PhasePreparing, kind: cmdPlain,
			argv: []string{ifconfigBin, utun, "inet", clientVPN + "/32", clientVPN, "alias"}},
		plannedCmd{name: "ifconfig up", phase: vpnmut.PhasePreparing, kind: cmdPlain,
			argv: []string{ifconfigBin, utun, "up"}},
	)

	// Фаза 3 — маршруты (host-route сервера, затем две половинки default).
	for i, argv := range vpnmut.BuildRouteUp(serverIP, gateway, utun) {
		plan = append(plan, plannedCmd{
			name: fmt.Sprintf("route up #%d", i), phase: vpnmut.PhaseRouted, kind: cmdPlain, argv: argv,
		})
	}

	// Фаза 4 — DNS по каждому активному сервису.
	for i, argv := range vpnmut.BuildDNSSet(dnsServices, dnsServers) {
		plan = append(plan, plannedCmd{
			name: fmt.Sprintf("dns set #%d", i), phase: vpnmut.PhaseDNSSet, kind: cmdPlain, argv: argv,
		})
	}

	// Фаза 5 — PF kill-switch.
	plan = append(plan,
		plannedCmd{name: "pf load", phase: vpnmut.PhasePFSet, kind: cmdPFLoad,
			argv: killswitch.LoadRulesArgv(), stdin: pfRules},
		plannedCmd{name: "pf enable", phase: vpnmut.PhasePFSet, kind: cmdPFEnable,
			argv: killswitch.EnableArgv()},
	)

	return plan
}

// buildTeardownCommands — ЧИСТАЯ функция: по достигнутой фазе и полям state строит
// упорядоченный список команд отката, ЗЕРКАЛЬНЫЙ connect-плану (§4 teardown):
//
//	pfctl -a <anchor> -F all            (если якорь грузили и !keepPF)
//	pfctl -X <token>                    (если есть токен и !keepPF)
//	networksetup -setdnsservers ...     (restore из снимка; если дошли до dns-set)
//	route delete half0 → half1 → host   (если дошли до routed)
//
// Адрес utun и половинки /1 отдельно не снимаем — их уберёт fullvpn.Close вместе с
// интерфейсом (ядро вычистит маршруты через исчезнувший utun, §2.1). Каждая команда
// идемпотентна (delete отсутствующего маршрута / flush пустого якоря безвредны),
// поэтому список годится и для отката connect, и для disconnect, и для crash-recovery.
//
// keepPF=true (strict crash-recovery §9) оставляет PF-якорь активным — сеть остаётся
// закрытой (fail-closed), снимаются только DNS и маршруты.
func buildTeardownCommands(st *vpnmut.State, keepPF bool) [][]string {
	var cmds [][]string

	if !keepPF && (st.AnchorLoaded || st.PFToken != "") {
		cmds = append(cmds, killswitch.FlushAnchorArgv())
		if st.PFToken != "" {
			cmds = append(cmds, killswitch.DisableArgv(st.PFToken))
		}
	}

	if phaseRank(st.Phase) >= phaseRank(vpnmut.PhaseDNSSet) && st.DnsSnapshot != nil {
		cmds = append(cmds, vpnmut.BuildDNSRestore(st.DnsSnapshot)...)
	}

	if phaseRank(st.Phase) >= phaseRank(vpnmut.PhaseRouted) {
		cmds = append(cmds, vpnmut.BuildRouteDown(st.ServerIP, st.OrigGateway, st.Utun)...)
	}

	return cmds
}

// phaseRank задаёт линейный порядок фаз: каждая следующая означает, что все
// предыдущие слои системы тоже изменены (§9). Используется для выбора, какие слои
// снимать в teardown. PhaseTearingDown рангом выше connected — незавершённый
// teardown на старте демона трактуется как «снять всё» (идемпотентно).
func phaseRank(p vpnmut.Phase) int {
	switch p {
	case vpnmut.PhaseClean:
		return 0
	case vpnmut.PhasePreparing:
		return 1
	case vpnmut.PhaseRouted:
		return 2
	case vpnmut.PhaseDNSSet:
		return 3
	case vpnmut.PhasePFSet:
		return 4
	case vpnmut.PhaseConnected:
		return 5
	case vpnmut.PhaseTearingDown:
		return 6
	default:
		return -1
	}
}
