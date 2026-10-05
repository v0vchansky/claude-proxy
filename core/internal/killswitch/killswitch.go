// Package killswitch генерирует PF kill-switch для full-tunnel VPN: текст правил
// суб-якоря com.apple/250.ClaudeProxyVPN (§7 дизайна) и argv-команды pfctl для
// загрузки/снятия якоря и включения/выключения PF с reference-токеном.
//
// Пакет НИЧЕГО не выполняет: это чистые функции рендера правил, сборки argv и
// разбора stderr `pfctl -E`. Запуск pfctl и работа с реальным PF — выше по стеку
// (демон vpnd), здесь только детерминированная генерация и парсинг.
package killswitch

import (
	"fmt"
	"regexp"
	"strings"
)

// AnchorPath — путь суб-якоря PF под стоковым wildcard-якорем com.apple/* (§7).
// Стоковый /etc/pf.conf содержит `anchor "com.apple/*"`, который исполняет все
// непосредственно подвешенные дочерние якоря, поэтому загрузка правил сюда делает
// их активными без правки main ruleset. Число 250 ставит нас по алфавиту после
// якорей Apple (200.AirDrop, 400.AdaptiveFirewall и т.п.).
const AnchorPath = "com.apple/250.ClaudeProxyVPN"

// LANTableName — имя PF-таблицы с подсетями направленного LAN (§7).
const LANTableName = "cpx_lan"

// pfctlBin — абсолютный путь к pfctl. Абсолютный намеренно: демон работает от root,
// и фиксированный путь исключает перехват через PATH.
const pfctlBin = "/sbin/pfctl"

// DefaultLANSubnets — дефолтный состав таблицы <cpx_lan> из §7: приватные сети
// RFC1918, link-local (169.254/16), мультикаст/mDNS (224.0.0.0/4) и
// DHCP-broadcast (255.255.255.255/32 покрывает renew 68→67 на broadcast).
var DefaultLANSubnets = []string{
	"10.0.0.0/8",
	"172.16.0.0/12",
	"192.168.0.0/16",
	"169.254.0.0/16",
	"224.0.0.0/4",
	"255.255.255.255/32",
}

// Params — вход генерации правил kill-switch.
//
// Физический интерфейс (en0 и т.п.) в Params намеренно отсутствует: правило для
// UDP-транспорта WG не привязано к имени интерфейса (§7) — host-route из фазы 3
// направляет этот трафик в физический линк, а правило продолжает работать при
// смене сети (Wi-Fi → Ethernet). Привязка к имени только сломала бы это свойство.
type Params struct {
	// UtunIf — имя туннельного интерфейса, напр. "utun9". Через него разрешён
	// весь исходящий inet (единственный широкий выход — всё, что легло в туннель).
	UtunIf string
	// ServerIP — IPv4-адрес UDP-endpoint'а сервера WG (транспорт туннеля).
	ServerIP string
	// ServerPort — UDP-порт endpoint'а.
	ServerPort int
	// AllowLAN — разрешить направленный LAN/DHCP/mDNS (таблица <cpx_lan>).
	// false — строгий режим: LAN-правила не рендерятся, проходит только туннель,
	// lo0 и UDP-транспорт к серверу.
	AllowLAN bool
	// LANSubnets — состав таблицы <cpx_lan>. Пусто → DefaultLANSubnets (§7).
	// Учитывается только при AllowLAN=true.
	LANSubnets []string
}

// RenderRules возвращает полный текст правил суб-якоря (§7).
//
// Политика fail-closed: по умолчанию закрыт весь исходящий (inet и inet6 — так
// v6-утечки гибнут здесь же, туннель v4-only). Разрешения — только `quick`, по
// убыванию специфичности: lo0 (петля целиком, оба направления, без state —
// локальный трафик к 127.0.0.1 должен работать при активном kill-switch), туннель
// utunN, UDP-транспорт к серверу и (при AllowLAN) направленный LAN. Прочие
// входящие не трогаем — цель анти-утечка исходящего, inbound закрывает NAT/stateful.
func RenderRules(p Params) string {
	lan := p.LANSubnets
	if len(lan) == 0 {
		lan = DefaultLANSubnets
	}

	var b strings.Builder

	b.WriteString(`# Claude Proxy Full VPN kill-switch. Снимается: pfctl -a "` + AnchorPath + `" -F all` + "\n")
	b.WriteString("# Направленный LAN/DHCP-трафик; 255.255.255.255 — DHCP-broadcast.\n")

	// Таблица подсетей LAN рендерится только когда LAN вообще разрешён — иначе на
	// неё никто не ссылается и держать её в якоре незачем.
	if p.AllowLAN {
		b.WriteString("table <" + LANTableName + "> const { " + strings.Join(lan, ", ") + " }\n")
	}

	b.WriteString("\n# По умолчанию: весь исходящий закрыт (inet и inet6 — v6-утечки гибнут здесь же).\n")
	// `block return out all` без указания address family закрывает и inet, и inet6.
	// `return`, а не `drop`: приложения получают мгновенный RST/ICMP-unreachable
	// вместо зависших таймаутов.
	b.WriteString("block return out all\n")

	b.WriteString("\n# Разрешения — только quick, по убыванию специфичности:\n")
	// lo0 — локальная петля целиком, в ОБОИХ направлениях и БЕЗ состояния.
	//
	// Почему не `pass out quick on lo0 all`: на macOS это правило петлю НЕ чинит —
	// коннект к 127.0.0.1 (включая локальный HTTP-прокси, через который ходит
	// Claude Code) всё равно рвётся при активном kill-switch. Доказано независимым
	// TCP-сервером на 127.0.0.1: под VPN недоступен, VPN выкл → доступен мгновенно.
	// Две причины, обе снимаются здесь:
	//   1) stateful-by-default: голый `pass` на macOS/OpenBSD неявно держит state.
	//      На loopback один и тот же сегмент проходит lo0 дважды (out, затем in);
	//      state, созданный на out-проходе, ломает обратный in-проход. `no state`
	//      убирает учёт состояния на петле целиком.
	//   2) направление: директива без in/out покрывает оба прохода пакета по lo0.
	// Это дословный аналог канонического `set skip on lo0`, но в виде ПРАВИЛА
	// внутри суб-якоря. Сам `set skip` здесь не используется намеренно: это
	// глобальная опция main ruleset — в дочернем якоре она не применяется, а если
	// бы применилась, то выставила бы глобальный флаг интерфейса (сбрасывается лишь
	// `pfctl -F Reset`, а не flush'ем якоря) — необратимо утёк бы за пределы нашего
	// якоря. Правило же полностью живёт и снимается вместе с якорем (откатываемо).
	// `all` включает и inet6 (::1) — это локальная петля, наружу не утекает, безопасно.
	b.WriteString("pass quick on lo0 all no state\n")
	// Туннель — единственный широкий выход: всё, что легло в utunN.
	b.WriteString(fmt.Sprintf("pass out quick on %s inet all keep state\n", p.UtunIf))
	// UDP-транспорт самого WG к endpoint'у сервера на физическом линке (без привязки
	// к имени интерфейса — см. комментарий к Params).
	b.WriteString(fmt.Sprintf("pass out quick inet proto udp from any to %s port = %d keep state\n", p.ServerIP, p.ServerPort))
	// Направленный LAN — опциональный компромисс «Allow LAN».
	if p.AllowLAN {
		b.WriteString(fmt.Sprintf("pass out quick inet to <%s> keep state\n", LANTableName))
	}

	return b.String()
}

// LoadRulesArgv — argv загрузки правил в якорь. Правила подаются на stdin («-f -»):
// pfctl -a "<anchor>" -f -. Загрузка атомарна — заменяет весь якорь целиком.
func LoadRulesArgv() []string {
	return []string{pfctlBin, "-a", AnchorPath, "-f", "-"}
}

// FlushAnchorArgv — argv полного сброса якоря: pfctl -a "<anchor>" -F all.
// Снимает весь якорь целиком; по пустому якорю безвредно (идемпотентно).
func FlushAnchorArgv() []string {
	return []string{pfctlBin, "-a", AnchorPath, "-F", "all"}
}

// EnableArgv — argv включения PF с инкрементом reference count: pfctl -E.
// В stderr вернётся «pf enabled» и «Token : <число>» — токен разобрать
// через ParseEnableToken и сохранить в state.
func EnableArgv() []string {
	return []string{pfctlBin, "-E"}
}

// DisableArgv — argv отпускания reference по токену: pfctl -X <token>.
// Благодаря reference count мы не выключим PF под тем, кто его тоже включил.
func DisableArgv(token string) []string {
	return []string{pfctlBin, "-X", token}
}

// enableTokenRe вытаскивает reference-токен из stderr `pfctl -E`.
// Формат строки: «Token : 4620695902811238862» (токен — десятичное число).
var enableTokenRe = regexp.MustCompile(`(?m)^\s*Token\s*:\s*(\d+)\s*$`)

// ParseEnableToken выдёргивает reference-токен из stderr команды `pfctl -E`.
//
// Пример входа:
//
//	pf enabled
//	Token : 4620695902811238862
//
// Возвращает «4620695902811238862». Пустой или не содержащий валидной строки
// «Token : <число>» stderr → ошибка.
func ParseEnableToken(stderr string) (string, error) {
	m := enableTokenRe.FindStringSubmatch(stderr)
	if m == nil {
		return "", fmt.Errorf("killswitch: в stderr pfctl -E не найден reference-токен (формат «Token : <число>»): %q", stderr)
	}
	return m[1], nil
}
