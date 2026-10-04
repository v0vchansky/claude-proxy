package netcfg

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

// fixtureNetstat — реальный вывод `netstat -rn -f inet` с этой машины (§2.2):
// активен сторонний VPN (utun4), его default висит на link#26; физический
// default — через 192.168.2.1 @ en0. Парсер обязан вернуть именно физический.
const fixtureNetstat = `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
default            link#26            UCSg                utun4
default            192.168.2.1        UGScIg                en0
127                127.0.0.1          UCS                   lo0
127.0.0.1          127.0.0.1          UH                    lo0
169.254            link#11            UCS                   en0      !
192.168.2          link#11            UCS                   en0      !
192.168.2.1        0:1:2:3:4:5        UHLWIir               en0   1200
224.0.0            link#11            UmCS                  en0      !
`

func TestParseDefaultRouteSkipsForeignVPN(t *testing.T) {
	gw, dev, err := parseDefaultRoute(fixtureNetstat)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if gw != "192.168.2.1" {
		t.Errorf("gateway: got %q, want физический 192.168.2.1 (не utun/link#)", gw)
	}
	if dev != "en0" {
		t.Errorf("device: got %q, want en0", dev)
	}
}

func TestParseDefaultRouteNoDefault(t *testing.T) {
	out := `Routing tables

Internet:
Destination        Gateway            Flags               Netif Expire
127                127.0.0.1          UCS                   lo0
192.168.2          link#11            UCS                   en0      !
`
	if _, _, err := parseDefaultRoute(out); err == nil {
		t.Fatal("ожидалась ошибка: физического default нет")
	}
}

func TestParseDefaultRouteOnlyLinkDefault(t *testing.T) {
	// Единственный default — чужой utun через link#; физического next-hop нет.
	out := `Destination        Gateway            Flags               Netif Expire
default            link#26            UCSg                utun4
`
	if _, _, err := parseDefaultRoute(out); err == nil {
		t.Fatal("ожидалась ошибка: единственный default — link# (не физический)")
	}
}

func TestParseDefaultRouteMultiplePhysical(t *testing.T) {
	// Несколько физических default — берём первый по порядку.
	out := `Destination        Gateway            Flags               Netif Expire
default            link#26            UCSg                utun4
default            10.0.0.1           UGScIg                en1
default            192.168.2.1        UGScIg                en0
`
	gw, dev, err := parseDefaultRoute(out)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if gw != "10.0.0.1" || dev != "en1" {
		t.Errorf("got %q/%q, want первый физический 10.0.0.1/en1", gw, dev)
	}
}

func TestParseDefaultRouteEmpty(t *testing.T) {
	if _, _, err := parseDefaultRoute(""); err == nil {
		t.Fatal("ожидалась ошибка на пустом вводе")
	}
}

// fixtureServiceOrder — вывод `networksetup -listnetworkserviceorder` в стиле
// этой машины (§2.2): среди сервисов есть отключённый (*) и сервис без Device.
const fixtureServiceOrder = `An asterisk (*) denotes that a network service is disabled.
(1) USB 10/100/1000 LAN
(Hardware Port: USB 10/100/1000 LAN, Device: en5)

(2) Thunderbolt Bridge
(Hardware Port: Thunderbolt Bridge, Device: bridge0)

(3) Wi-Fi
(Hardware Port: Wi-Fi, Device: en0)

(*) v2RayTun
(Hardware Port: v2RayTun, Device: )

(4) iPhone USB
(Hardware Port: iPhone USB, Device: en6)

(5) peer1
(Hardware Port: peer1, Device: utun3)
`

func TestParseServiceOrder(t *testing.T) {
	got := parseServiceOrder(fixtureServiceOrder)
	want := []Service{
		{Name: "USB 10/100/1000 LAN", Device: "en5", Enabled: true},
		{Name: "Thunderbolt Bridge", Device: "bridge0", Enabled: true},
		{Name: "Wi-Fi", Device: "en0", Enabled: true},
		{Name: "v2RayTun", Device: "", Enabled: false},
		{Name: "iPhone USB", Device: "en6", Enabled: true},
		{Name: "peer1", Device: "utun3", Enabled: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseServiceOrder:\n got %#v\nwant %#v", got, want)
	}
}

func TestParseServiceOrderDisabledFlag(t *testing.T) {
	got := parseServiceOrder(fixtureServiceOrder)
	for _, s := range got {
		if s.Name == "v2RayTun" {
			if s.Enabled {
				t.Error("v2RayTun помечен (*) — должен быть Enabled=false")
			}
			if s.Device != "" {
				t.Errorf("v2RayTun без устройства — Device должно быть пусто, got %q", s.Device)
			}
			return
		}
	}
	t.Fatal("сервис v2RayTun не найден")
}

func TestParseServiceOrderEmpty(t *testing.T) {
	if got := parseServiceOrder(""); len(got) != 0 {
		t.Fatalf("ожидался пустой список, got %#v", got)
	}
}

func TestParseDNSList(t *testing.T) {
	got := parseDNS("1.1.1.1\n8.8.8.8\n")
	want := []string{"1.1.1.1", "8.8.8.8"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestParseDNSEmpty(t *testing.T) {
	out := "There aren't any DNS Servers set on Wi-Fi."
	if got := parseDNS(out); len(got) != 0 {
		t.Fatalf("«There aren't any…» → ожидался пустой список, got %#v", got)
	}
}

func TestParseDNSBlank(t *testing.T) {
	if got := parseDNS("\n  \n"); len(got) != 0 {
		t.Fatalf("пустой/пробельный ввод → пусто, got %#v", got)
	}
}

// fakeRunner подменяет exec: отдаёт заранее заданный вывод по имени команды.
func fakeRunner(responses map[string]string, errs map[string]error) func(context.Context, string, ...string) (string, error) {
	return func(_ context.Context, name string, args ...string) (string, error) {
		key := name
		if name == "networksetup" && len(args) > 0 {
			key = name + " " + args[0]
		}
		if err, ok := errs[key]; ok {
			return "", err
		}
		return responses[key], nil
	}
}

func TestCaptureAssemblesSnapshot(t *testing.T) {
	orig := runner
	defer func() { runner = orig }()
	runner = fakeRunner(map[string]string{
		"netstat":                               fixtureNetstat,
		"networksetup -listnetworkserviceorder": fixtureServiceOrder,
		"networksetup -getdnsservers":           "1.1.1.1\n8.8.8.8\n",
	}, nil)

	snap, err := Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if snap.DefaultRoute.Gateway != "192.168.2.1" || snap.DefaultRoute.Device != "en0" {
		t.Errorf("default route: got %+v", snap.DefaultRoute)
	}
	if len(snap.Services) != 6 {
		t.Errorf("ожидалось 6 сервисов, got %d", len(snap.Services))
	}
	// DNS снимаются только с включённых сервисов — v2RayTun (disabled) не должен попасть.
	if _, ok := snap.DNS["v2RayTun"]; ok {
		t.Error("отключённый сервис v2RayTun не должен быть в карте DNS")
	}
	if got := snap.DNS["Wi-Fi"]; !reflect.DeepEqual(got, []string{"1.1.1.1", "8.8.8.8"}) {
		t.Errorf("DNS Wi-Fi: got %#v", got)
	}
}

func TestCapturePropagatesCommandError(t *testing.T) {
	orig := runner
	defer func() { runner = orig }()
	runner = fakeRunner(nil, map[string]error{
		"netstat": fmt.Errorf("netcfg: команда \"netstat\" не найдена в PATH"),
	})
	if _, err := Capture(context.Background()); err == nil {
		t.Fatal("ожидалась ошибка, если netstat недоступен")
	}
}

// TestLiveCapture опционально дёргает реальные системные команды.
// Пропускается при -short (зависит от окружения: наличие утилит, права).
func TestLiveCapture(t *testing.T) {
	if testing.Short() {
		t.Skip("live Snapshot пропущен в -short режиме")
	}
	snap, err := Capture(context.Background())
	if err != nil {
		t.Skipf("живой Capture недоступен в этом окружении: %v", err)
	}
	if snap.DefaultRoute.Device == "" {
		t.Error("живой снимок: пустое устройство физического default")
	}
	t.Logf("live snapshot: default=%+v services=%d", snap.DefaultRoute, len(snap.Services))
}
