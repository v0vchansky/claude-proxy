package profile

import (
	"strings"
	"testing"

	"github.com/v0vchansky/claude-proxy/core/internal/wgkey"
)

func sampleProfile() Profile {
	return Profile{
		ID:                  "nl-hostkey",
		DisplayName:         "Netherlands / HOSTKEY",
		Host:                "222.167.208.108",
		Port:                51820,
		ServerPublicKey:     "WyhFpvvdzC2OBhpbcRAMzVZXsyWgToZ/4vtVkgBo4F4=",
		ClientVpnAddress:    "10.77.0.2",
		ServerVpnAddress:    "10.77.0.1",
		DNS:                 []string{"1.1.1.1", "8.8.8.8"},
		MTU:                 1420,
		PersistentKeepalive: 25,
		Jc:                  5, Jmin: 50, Jmax: 1000,
		S1: 64, S2: 128, S3: 0, S4: 0,
		H1: 1000001, H2: 1000002, H3: 1000003, H4: 1000004,
	}
}

func TestValidateOK(t *testing.T) {
	if err := sampleProfile().Validate(); err != nil {
		t.Fatalf("валидный профиль отвергнут: %v", err)
	}
}

func TestValidateErrors(t *testing.T) {
	cases := map[string]func(*Profile){
		"пустой host":          func(p *Profile) { p.Host = "" },
		"порт вне диапазона":   func(p *Profile) { p.Port = 70000 },
		"битый serverPublicKey": func(p *Profile) { p.ServerPublicKey = "не ключ" },
		"битый clientVpnAddress": func(p *Profile) { p.ClientVpnAddress = "999.1.1.1" },
		"битый dns":            func(p *Profile) { p.DNS = []string{"nope"} },
	}
	for name, mutate := range cases {
		p := sampleProfile()
		mutate(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("%s: ожидалась ошибка", name)
		}
	}
}

func TestClientAddrStripsMask(t *testing.T) {
	p := sampleProfile()
	p.ClientVpnAddress = "10.77.0.2/32"
	a, err := p.ClientAddr()
	if err != nil {
		t.Fatal(err)
	}
	if a.String() != "10.77.0.2" {
		t.Fatalf("got %s", a)
	}
}

func TestDNSAddrsDefault(t *testing.T) {
	p := sampleProfile()
	p.DNS = nil
	got := p.DNSAddrs()
	if len(got) != 2 {
		t.Fatalf("ожидалось 2 дефолтных DNS, got %d", len(got))
	}
}

func TestBuildUAPIContainsParamsAndHidesNothingSecret(t *testing.T) {
	p := sampleProfile()
	priv, _, _ := wgkey.Generate()
	uapi, err := p.BuildUAPI(priv)
	if err != nil {
		t.Fatalf("BuildUAPI: %v", err)
	}
	// Параметры обфускации присутствуют.
	for _, want := range []string{
		"jc=5", "jmin=50", "jmax=1000", "s1=64", "s2=128",
		"h1=1000001", "h2=1000002", "h3=1000003", "h4=1000004",
		"endpoint=222.167.208.108:51820",
		"allowed_ip=0.0.0.0/0",
		"persistent_keepalive_interval=25",
	} {
		if !strings.Contains(uapi, want) {
			t.Errorf("в uapi нет %q", want)
		}
	}
	// s3/s4 НЕ должны уходить в uapi: amneziawg-go v1.0.4 их не поддерживает.
	if strings.Contains(uapi, "s3=") || strings.Contains(uapi, "s4=") {
		t.Error("uapi не должен содержать s3/s4")
	}
	// private_key записан как hex (64 символа), а не как исходный base64.
	if strings.Contains(uapi, priv) {
		t.Error("приватный ключ попал в uapi в base64 — должен быть hex")
	}
	if !strings.Contains(uapi, "private_key=") {
		t.Error("нет private_key в uapi")
	}
}

func TestBuildUAPIRejectsBadPrivateKey(t *testing.T) {
	p := sampleProfile()
	if _, err := p.BuildUAPI("мусор"); err == nil {
		t.Fatal("ожидалась ошибка на невалидный приватный ключ")
	}
}
