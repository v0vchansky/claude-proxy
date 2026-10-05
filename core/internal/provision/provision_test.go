package provision

import (
	"strings"
	"testing"
)

func TestWithDefaults(t *testing.T) {
	p := Params{}.withDefaults()
	if p.AWGPort != 51820 {
		t.Errorf("AWGPort=%d", p.AWGPort)
	}
	if p.ServerVpnAddress != "10.77.0.1" || p.ClientVpnAddress != "10.77.0.2" {
		t.Errorf("addrs %s/%s", p.ServerVpnAddress, p.ClientVpnAddress)
	}
	if p.ClientVpnAddressFull != "10.77.0.3" {
		t.Errorf("clientVpnAddressFull=%q, want 10.77.0.3", p.ClientVpnAddressFull)
	}
	if p.Jc != 5 || p.S1 != 64 || p.S2 != 128 || p.H1 != 1000001 {
		t.Errorf("params not defaulted: %+v", p)
	}
	// Явно заданные значения не перетираются.
	p2 := Params{AWGPort: 40000, Jc: 3, S1: 10, H1: 7, ClientVpnAddressFull: "10.77.0.9"}.withDefaults()
	if p2.AWGPort != 40000 || p2.Jc != 3 || p2.S1 != 10 || p2.H1 != 7 {
		t.Errorf("explicit overwritten: %+v", p2)
	}
	if p2.ClientVpnAddressFull != "10.77.0.9" {
		t.Errorf("clientVpnAddressFull overwritten: %q", p2.ClientVpnAddressFull)
	}
}

func TestParseResult(t *testing.T) {
	out := strings.Join([]string{
		"LOG:step",
		"MODE=adopt",
		"NEEDS_REBOOT=0",
		"SERVER_PUBLIC_KEY=WyhFpvvdzC2OBhpbcRAMzVZXsyWgToZ/4vtVkgBo4F4=",
		"AWG_PORT=51820",
		"SERVER_VPN=10.77.0.1",
		"CLIENT_VPN=10.77.0.4",
		"CLIENT_VPN_FULL=10.77.0.5",
		"JC=5", "JMIN=50", "JMAX=1000",
		"S1=64", "S2=128",
		"H1=1000001", "H2=1000002", "H3=1000003", "H4=1000004",
		"PROVISION_OK",
	}, "\n")
	r, err := parseResult(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.ServerPublicKey != "WyhFpvvdzC2OBhpbcRAMzVZXsyWgToZ/4vtVkgBo4F4=" {
		t.Errorf("pubkey=%q", r.ServerPublicKey)
	}
	if r.Port != 51820 || r.Jc != 5 || r.S2 != 128 || r.H4 != 1000004 {
		t.Errorf("fields: %+v", r)
	}
	if r.ServerVpnAddress != "10.77.0.1" {
		t.Errorf("servervpn=%q", r.ServerVpnAddress)
	}
	if r.RebootRequired {
		t.Error("RebootRequired должно быть false при NEEDS_REBOOT=0")
	}
	// Адреса клиента — фактические из вывода сервера, а не дефолтные .2/.3.
	if r.ClientVpnAddress != "10.77.0.4" || r.ClientVpnAddressFull != "10.77.0.5" {
		t.Errorf("client addrs %q / %q", r.ClientVpnAddress, r.ClientVpnAddressFull)
	}
}

// Без второго ключа сервер не выводит CLIENT_VPN_FULL — поле пустое, это не ошибка.
// Маска в выводе отрезается; CLIENT_VPN не путается с CLIENT_VPN_FULL по префиксу.
func TestParseResultWithoutFullAddress(t *testing.T) {
	out := "SERVER_PUBLIC_KEY=abc\nCLIENT_VPN_FULL_X=junk\nCLIENT_VPN=10.8.0.3/32\nPROVISION_OK\n"
	r, err := parseResult(out)
	if err != nil {
		t.Fatal(err)
	}
	if r.ClientVpnAddress != "10.8.0.3" {
		t.Errorf("clientVpnAddress=%q", r.ClientVpnAddress)
	}
	if r.ClientVpnAddressFull != "" {
		t.Errorf("clientVpnAddressFull=%q, ожидалась пустая строка", r.ClientVpnAddressFull)
	}
}

// Без CLIENT_VPN адрес клиента неизвестен — нельзя молча подставлять запрошенный
// (ровно так новый клиент получал адрес старого).
func TestParseResultMissingClientAddress(t *testing.T) {
	if _, err := parseResult("SERVER_PUBLIC_KEY=abc\nCLIENT_VPN_FULL=10.77.0.3\nPROVISION_OK\n"); err == nil {
		t.Fatal("ожидалась ошибка при отсутствии CLIENT_VPN")
	}
}

func TestParseResultRebootRequired(t *testing.T) {
	out := "NEEDS_REBOOT=1\nSERVER_PUBLIC_KEY=abc\nAWG_PORT=51820\nCLIENT_VPN=10.77.0.2\nPROVISION_OK\n"
	r, err := parseResult(out)
	if err != nil {
		t.Fatal(err)
	}
	if !r.RebootRequired {
		t.Error("ожидался RebootRequired=true при NEEDS_REBOOT=1")
	}
}

func TestParseResultMissingKey(t *testing.T) {
	if _, err := parseResult("MODE=fresh\nPROVISION_OK\n"); err == nil {
		t.Fatal("ожидалась ошибка при отсутствии SERVER_PUBLIC_KEY")
	}
}

func TestStripMask(t *testing.T) {
	cases := map[string]string{"10.77.0.2/32": "10.77.0.2", "10.77.0.1": "10.77.0.1", "": ""}
	for in, want := range cases {
		if got := stripMask(in); got != want {
			t.Errorf("stripMask(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestBuildScriptInjectsParamsAndMarkers(t *testing.T) {
	p := Params{AWGPort: 51820, ServerVpnAddress: "10.77.0.1", ClientVpnAddress: "10.77.0.2",
		ClientVpnAddressFull: "10.77.0.3",
		Jc:                   5, Jmin: 50, Jmax: 1000, S1: 64, S2: 128, H1: 1000001, H2: 1000002, H3: 1000003, H4: 1000004}
	s := buildScript(p, "CLIENTPUBKEY==", "FULLPUBKEY==")
	for _, want := range []string{
		"AWG_PORT=51820", "SERVER_VPN=\"10.77.0.1\"", "CLIENT_PUB=\"CLIENTPUBKEY==\"",
		"JC=5", "S1=64", "S2=128", "H1=1000001",
		"MODE=", "PROVISION_OK", "S3 = 0", "S4 = 0",
		"add-apt-repository -y ppa:amnezia/ppa",
		"NEEDS_REBOOT=", "modprobe amneziawg", "systemctl reboot",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("скрипт не содержит %q", want)
		}
	}
	// Клиентский публичный ключ не должен попасть как bash-инъекция (должен быть в кавычках).
	if !strings.Contains(s, `CLIENT_PUB="CLIENTPUBKEY=="`) {
		t.Error("CLIENT_PUB не закавычен")
	}
}

// При заданном втором публичном ключе скрипт несёт переменные Полного VPN,
// условие на непустоту и живое добавление второго peer'а. Первый peer — всегда.
func TestBuildScriptFullPeerPresent(t *testing.T) {
	p := Params{ClientVpnAddress: "10.77.0.2", ClientVpnAddressFull: "10.77.0.3"}.withDefaults()
	s := buildScript(p, "PROXYPUB==", "FULLPUB==")
	for _, want := range []string{
		`CLIENT_PUB_FULL="FULLPUB=="`,
		`CLIENT_VPN_FULL="10.77.0.3"`,
		`if [ -n "$CLIENT_PUB_FULL" ]; then`,
		`conf_upsert_peer "$CONF" "$CLIENT_PUB_FULL" "$CLIENT_IP_FULL"`,
		`awg set awg0 peer "$CLIENT_PUB_FULL" allowed-ips "${CLIENT_IP_FULL}/32"`,
		`echo "CLIENT_VPN_FULL=$CLIENT_IP_FULL"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("скрипт не содержит %q", want)
		}
	}
	// Первый (прокси) peer присутствует независимо от второго.
	if !strings.Contains(s, `conf_upsert_peer "$CONF" "$CLIENT_PUB" "$CLIENT_IP"`) {
		t.Error("отсутствует добавление первого (прокси) peer'а в конфиг")
	}
	if !strings.Contains(s, `awg set awg0 peer "$CLIENT_PUB" allowed-ips "${CLIENT_IP}/32"`) {
		t.Error("отсутствует живое добавление первого (прокси) peer'а")
	}
}

// При пустом втором публичном ключе блок второго peer'а не выполняется:
// переменная CLIENT_PUB_FULL пуста, но защита `if [ -n ... ]` в теле остаётся.
// Первый peer присутствует всегда.
func TestBuildScriptNoFullPeerWhenEmpty(t *testing.T) {
	p := Params{ClientVpnAddress: "10.77.0.2"}.withDefaults()
	s := buildScript(p, "PROXYPUB==", "")
	if !strings.Contains(s, `CLIENT_PUB_FULL=""`) {
		t.Error("CLIENT_PUB_FULL должен быть пустой строкой при отсутствии второго ключа")
	}
	// Защита на непустоту в теле — именно она гасит блок второго peer'а в runtime.
	if !strings.Contains(s, `if [ -n "$CLIENT_PUB_FULL" ]; then`) {
		t.Error("отсутствует защита `if [ -n \"$CLIENT_PUB_FULL\" ]`")
	}
	// Первый peer — всегда.
	if !strings.Contains(s, `CLIENT_PUB="PROXYPUB=="`) {
		t.Error("отсутствует первый (прокси) публичный ключ")
	}
	if !strings.Contains(s, `conf_upsert_peer "$CONF" "$CLIENT_PUB" "$CLIENT_IP"`) {
		t.Error("отсутствует добавление первого (прокси) peer'а")
	}
}

// Перезапуск awg0 (ip link del) — только в ветке «интерфейс поднят и [Interface] изменён»;
// живая донастройка идёт раньше и обходится без него. Выделение адресов — до правок
// конфига (S3/S4, peer'ы), чтобы при ошибке сервер остался как был.
func TestBuildScriptRestartOnlyWhenInterfaceChanged(t *testing.T) {
	s := buildScript(Params{}.withDefaults(), "PROXYPUB==", "FULLPUB==")
	if n := strings.Count(s, "ip link del awg0"); n != 1 {
		t.Fatalf("ip link del awg0 встречается %d раз, ожидался 1", n)
	}
	live := strings.Index(s, `if [ "$LIVE_UP" = 1 ] && [ "$IFACE_CHANGED" = 0 ]; then`)
	restart := strings.Index(s, `elif [ "$LIVE_UP" = 1 ]; then`)
	del := strings.Index(s, "ip link del awg0")
	if live < 0 || restart < 0 || !(live < restart && restart < del) {
		t.Errorf("порядок веток применения нарушен: live=%d restart=%d del=%d", live, restart, del)
	}
	alloc := strings.Index(s, `if ! alloc_addrs "$CONF"`)
	norm := strings.Index(s, `conf_normalize_s34 "$CONF"; then`)
	upsert := strings.Index(s, `conf_upsert_peer "$CONF" "$CLIENT_PUB"`)
	if alloc < 0 || !(alloc < norm && alloc < upsert) {
		t.Errorf("выделение адресов должно идти до правок конфига: alloc=%d norm=%d upsert=%d", alloc, norm, upsert)
	}
	for _, want := range []string{"echo \"CLIENT_VPN=$CLIENT_IP\"", "echo \"SERVER_VPN=$SRV_IP\"", "exit 20"} {
		if !strings.Contains(s, want) {
			t.Errorf("скрипт не содержит %q", want)
		}
	}
}
