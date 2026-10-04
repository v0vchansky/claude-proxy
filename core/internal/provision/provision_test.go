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
	if p.Jc != 5 || p.S1 != 64 || p.S2 != 128 || p.H1 != 1000001 {
		t.Errorf("params not defaulted: %+v", p)
	}
	// Явно заданные значения не перетираются.
	p2 := Params{AWGPort: 40000, Jc: 3, S1: 10, H1: 7}.withDefaults()
	if p2.AWGPort != 40000 || p2.Jc != 3 || p2.S1 != 10 || p2.H1 != 7 {
		t.Errorf("explicit overwritten: %+v", p2)
	}
}

func TestParseResult(t *testing.T) {
	out := strings.Join([]string{
		"LOG:step",
		"MODE=adopt",
		"SERVER_PUBLIC_KEY=WyhFpvvdzC2OBhpbcRAMzVZXsyWgToZ/4vtVkgBo4F4=",
		"AWG_PORT=51820",
		"SERVER_VPN=10.77.0.1",
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
		Jc: 5, Jmin: 50, Jmax: 1000, S1: 64, S2: 128, H1: 1000001, H2: 1000002, H3: 1000003, H4: 1000004}
	s := buildScript(p, "CLIENTPUBKEY==")
	for _, want := range []string{
		"AWG_PORT=51820", "SERVER_VPN=\"10.77.0.1\"", "CLIENT_PUB=\"CLIENTPUBKEY==\"",
		"JC=5", "S1=64", "S2=128", "H1=1000001",
		"MODE=", "PROVISION_OK", "S3 = 0", "S4 = 0",
		"add-apt-repository -y ppa:amnezia/ppa",
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
