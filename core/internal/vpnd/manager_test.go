package vpnd

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0vchansky/claude-proxy/core/internal/fullvpn"
	"github.com/v0vchansky/claude-proxy/core/internal/netcfg"
	"github.com/v0vchansky/claude-proxy/core/internal/profile"
	"github.com/v0vchansky/claude-proxy/core/internal/vpnmut"
)

// --- фейки exec/opener/capture (всё без root) ---------------------------------

type recordedCall struct {
	argv  []string
	stdin string
}

// fakeRunner записывает каждый вызов и умеет падать на заданном индексе. Для
// `pfctl -E` отдаёт stderr с reference-токеном; для `pfctl -a ... -sr` — непустой
// ruleset (watchdog видит якорь на месте), если не переопределено.
type fakeRunner struct {
	calls        []recordedCall
	failAt       int    // индекс вызова (0-based), на котором вернуть ошибку; -1 = не падать
	enableStderr string // stderr для pfctl -E
	anchorShow   string // stdout для pfctl -a ... -sr
	routeGet     string // stdout для route -n get
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		failAt:       -1,
		enableStderr: "pf enabled\nToken : 12345\n",
		anchorShow:   "block return out all\n",
		routeGet:     "   gateway: 192.168.2.1\n",
	}
}

func (f *fakeRunner) Run(_ context.Context, stdin string, argv []string) (string, string, error) {
	idx := len(f.calls)
	f.calls = append(f.calls, recordedCall{argv: append([]string(nil), argv...), stdin: stdin})
	if f.failAt >= 0 && idx == f.failAt {
		return "", "boom", fmt.Errorf("fakeRunner: сбой на вызове %d (%v)", idx, argv)
	}
	switch {
	case contains(argv, "-E"):
		return "", f.enableStderr, nil
	case contains(argv, "-sr"):
		return f.anchorShow, "", nil
	case len(argv) >= 2 && argv[1] == "-n" && contains(argv, "get"):
		return f.routeGet, "", nil
	}
	return "", "", nil
}

func (f *fakeRunner) lines() []string {
	out := make([]string, len(f.calls))
	for i, c := range f.calls {
		out[i] = strings.Join(c.argv, " ")
	}
	return out
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

type fakeTunnel struct {
	name   string
	hsErr  error
	closed bool
	stats  fullvpn.Stats
}

func (f *fakeTunnel) DeviceName() string                    { return f.name }
func (f *fakeTunnel) WaitHandshake(_ context.Context) error { return f.hsErr }
func (f *fakeTunnel) Stats() (fullvpn.Stats, error)         { return f.stats, nil }
func (f *fakeTunnel) Close() error                          { f.closed = true; return nil }

func fakeCapture(context.Context) (*netcfg.Snapshot, error) {
	return &netcfg.Snapshot{
		DefaultRoute: netcfg.DefaultRoute{Gateway: "192.168.2.1", Device: "en0"},
		Services:     []netcfg.Service{{Name: "Wi-Fi", Device: "en0", Enabled: true}},
		DNS:          map[string][]string{"Wi-Fi": nil},
	}, nil
}

// newTestManager собирает Manager на фейках. tun — туннель, который вернёт opener
// (nil → исправный utun9). r — runner (nil → исправный fakeRunner).
func newTestManager(t *testing.T, tun *fakeTunnel) *Manager {
	t.Helper()
	if tun == nil {
		tun = &fakeTunnel{name: "utun9"}
	}
	return &Manager{
		statePath: filepath.Join(t.TempDir(), "state.json"),
		logf:      func(string) {},
		run:       newFakeRunner(),
		open: func(profile.Profile, string, []netip.Addr, func(string)) (tunnelHandle, error) {
			return tun, nil
		},
		capture: fakeCapture,
		st:      vpnmut.State{Phase: vpnmut.PhaseClean},
	}
}

func testProfile() profile.Profile {
	return profile.Profile{
		Host:             "222.167.208.108",
		Port:             51820,
		ServerPublicKey:  base64.StdEncoding.EncodeToString(make([]byte, 32)),
		ClientVpnAddress: "10.77.0.2/32",
		DNS:              []string{"1.1.1.1", "8.8.8.8"},
		MTU:              1420,
	}
}

// --- connect: порядок системных команд ---------------------------------------

func TestConnectCommandOrder(t *testing.T) {
	m := newTestManager(t, nil)
	res, err := m.Connect(testProfile(), "priv")
	if err != nil {
		t.Fatalf("connect: неожиданная ошибка: %v", err)
	}

	fr := m.run.(*fakeRunner)
	want := []string{
		"/sbin/ifconfig utun9 inet 10.77.0.2/32 10.77.0.2 alias",
		"/sbin/ifconfig utun9 up",
		"/sbin/route -q -n add -inet 222.167.208.108 -gateway 192.168.2.1",
		"/sbin/route -q -n add -inet 0.0.0.0/1 -interface utun9",
		"/sbin/route -q -n add -inet 128.0.0.0/1 -interface utun9",
		"/usr/sbin/networksetup -setdnsservers Wi-Fi 1.1.1.1 8.8.8.8",
		"/sbin/pfctl -a com.apple/250.ClaudeProxyVPN -f -",
		"/sbin/pfctl -E",
	}
	assertSeq(t, "connect", fr.lines(), want)

	// Правила PF поданы на stdin pf-load (предпоследний вызов).
	if sd := fr.calls[len(fr.calls)-2].stdin; !strings.Contains(sd, "block return out all") {
		t.Fatalf("pf load: правила не поданы на stdin: %q", sd)
	}
	// Итог: connected, kill-switch включён, state на диске тоже connected.
	if res["phase"] != "connected" || res["killSwitch"] != true {
		t.Fatalf("status после connect: %+v", res)
	}
	st, _ := vpnmut.ReadState(m.statePath)
	if st.Phase != vpnmut.PhaseConnected || st.PFToken != "12345" || st.Utun != "utun9" {
		t.Fatalf("state после connect: %+v", st)
	}
}

func TestConnectBusyRejectsSecond(t *testing.T) {
	m := newTestManager(t, nil)
	if _, err := m.Connect(testProfile(), "priv"); err != nil {
		t.Fatalf("первый connect: %v", err)
	}
	if _, err := m.Connect(testProfile(), "priv"); err == nil {
		t.Fatal("второй connect: ожидалась ошибка «сеанс уже активен»")
	}
}

// --- connect: откат на ошибке шага N ------------------------------------------

func TestConnectRollbackOnRouteFailure(t *testing.T) {
	tun := &fakeTunnel{name: "utun9"}
	m := newTestManager(t, tun)
	fr := newFakeRunner()
	// Вызовы: 0,1 ifconfig; 2 host-route; 3,4 half-routes; ... Падаем на 3 (half0).
	fr.failAt = 3
	m.run = fr

	_, err := m.Connect(testProfile(), "priv")
	if err == nil {
		t.Fatal("ожидалась ошибка connect при сбое маршрута")
	}

	// До сбоя: 2 ifconfig + host-route + (попытка half0 = сам сбойный вызов).
	// После: откат фазы routed — route delete half0, half1, host (dns/pf не трогаем).
	want := []string{
		"/sbin/ifconfig utun9 inet 10.77.0.2/32 10.77.0.2 alias",
		"/sbin/ifconfig utun9 up",
		"/sbin/route -q -n add -inet 222.167.208.108 -gateway 192.168.2.1",
		"/sbin/route -q -n add -inet 0.0.0.0/1 -interface utun9", // сбойный вызов
		// откат (зеркально фазе routed):
		"/sbin/route -q -n delete -inet 0.0.0.0/1 -interface utun9",
		"/sbin/route -q -n delete -inet 128.0.0.0/1 -interface utun9",
		"/sbin/route -q -n delete -inet 222.167.208.108",
	}
	assertSeq(t, "rollback@route", fr.lines(), want)

	// Туннель закрыт, state чист.
	if !tun.closed {
		t.Fatalf("при откате туннель должен быть закрыт")
	}
	st, _ := vpnmut.ReadState(m.statePath)
	if st.Phase != vpnmut.PhaseClean {
		t.Fatalf("после отката state должен быть clean, got %s", st.Phase)
	}
}

func TestConnectRollbackOnHandshakeFailure(t *testing.T) {
	tun := &fakeTunnel{name: "utun9", hsErr: fmt.Errorf("таймаут")}
	m := newTestManager(t, tun)

	_, err := m.Connect(testProfile(), "priv")
	if err == nil {
		t.Fatal("ожидалась ошибка handshake")
	}
	// Ни одной системной команды быть не должно: сеть не трогали.
	if n := len(m.run.(*fakeRunner).calls); n != 0 {
		t.Fatalf("после провала handshake системных команд быть не должно, got %d: %v", n, m.run.(*fakeRunner).lines())
	}
	if !tun.closed {
		t.Fatal("туннель должен быть закрыт после провала handshake")
	}
	st, _ := vpnmut.ReadState(m.statePath)
	if st.Phase != vpnmut.PhaseClean {
		t.Fatalf("state должен быть clean, got %s", st.Phase)
	}
}

func TestConnectRollbackOnMissingPFToken(t *testing.T) {
	m := newTestManager(t, nil)
	fr := newFakeRunner()
	fr.enableStderr = "pf enabled\n" // без строки Token → ParseEnableToken упадёт
	m.run = fr

	_, err := m.Connect(testProfile(), "priv")
	if err == nil {
		t.Fatal("ожидалась ошибка парса pf-токена")
	}
	// На момент сбоя: якорь загружен (anchorLoaded=true), токена нет. Откат:
	// pf flush (без disable — токена нет), dns restore, route down.
	got := fr.lines()
	tail := got[len(got)-5:] // последние 5 — откат: flush + dns + 3×route (disable нет — токена нет)
	want := []string{
		"/sbin/pfctl -a com.apple/250.ClaudeProxyVPN -F all",
		"/usr/sbin/networksetup -setdnsservers Wi-Fi empty",
		"/sbin/route -q -n delete -inet 0.0.0.0/1 -interface utun9",
		"/sbin/route -q -n delete -inet 128.0.0.0/1 -interface utun9",
		"/sbin/route -q -n delete -inet 222.167.208.108",
	}
	// pf flush должен быть, pf disable (pfctl -X) — НЕТ (токена нет).
	if contains2(got, "-X") {
		t.Fatalf("при отсутствии токена pfctl -X вызываться не должен: %v", got)
	}
	assertSeq(t, "rollback@pf-token (хвост)", tail, want)
}

// --- disconnect: зеркальный teardown ------------------------------------------

func TestDisconnectMirrorsConnect(t *testing.T) {
	m := newTestManager(t, nil)
	if _, err := m.Connect(testProfile(), "priv"); err != nil {
		t.Fatalf("connect: %v", err)
	}
	fr := m.run.(*fakeRunner)
	before := len(fr.calls)

	if _, err := m.Disconnect(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	teardown := fr.lines()[before:]
	want := []string{
		"/sbin/pfctl -a com.apple/250.ClaudeProxyVPN -F all",
		"/sbin/pfctl -X 12345",
		"/usr/sbin/networksetup -setdnsservers Wi-Fi empty",
		"/sbin/route -q -n delete -inet 0.0.0.0/1 -interface utun9",
		"/sbin/route -q -n delete -inet 128.0.0.0/1 -interface utun9",
		"/sbin/route -q -n delete -inet 222.167.208.108",
	}
	assertSeq(t, "disconnect", teardown, want)

	st, _ := vpnmut.ReadState(m.statePath)
	if st.Phase != vpnmut.PhaseClean {
		t.Fatalf("после disconnect state должен быть clean, got %s", st.Phase)
	}
	// Повторный disconnect идемпотентен: новых команд нет.
	n := len(fr.calls)
	if _, err := m.Disconnect(); err != nil {
		t.Fatalf("повторный disconnect: %v", err)
	}
	if len(fr.calls) != n {
		t.Fatalf("повторный disconnect не должен слать команд, прибавилось %d", len(fr.calls)-n)
	}
}

// --- crash-recovery: таблица фаза × флаг → действия ---------------------------

func TestRecoverNonStrictRestoresNetwork(t *testing.T) {
	m := newTestManager(t, nil)
	writeStale(t, m, vpnmut.State{
		Phase: vpnmut.PhaseConnected, Utun: "utun9",
		ServerIP: "222.167.208.108", ServerPort: 51820, OrigGateway: "192.168.2.1",
		DnsSnapshot: map[string][]string{"Wi-Fi": nil}, PFToken: "12345", AnchorLoaded: true,
	})

	m.Recover()

	fr := m.run.(*fakeRunner)
	want := []string{
		"/sbin/pfctl -a com.apple/250.ClaudeProxyVPN -F all",
		"/sbin/pfctl -X 12345",
		"/usr/sbin/networksetup -setdnsservers Wi-Fi empty",
		"/sbin/route -q -n delete -inet 0.0.0.0/1 -interface utun9",
		"/sbin/route -q -n delete -inet 128.0.0.0/1 -interface utun9",
		"/sbin/route -q -n delete -inet 222.167.208.108",
	}
	assertSeq(t, "recover non-strict", fr.lines(), want)
	st, _ := vpnmut.ReadState(m.statePath)
	if st.Phase != vpnmut.PhaseClean {
		t.Fatalf("после recovery state должен быть clean, got %s", st.Phase)
	}
}

func TestRecoverStrictKeepsKillSwitch(t *testing.T) {
	m := newTestManager(t, nil)
	writeStale(t, m, vpnmut.State{
		Phase: vpnmut.PhaseConnected, Utun: "utun9",
		ServerIP: "222.167.208.108", ServerPort: 51820, OrigGateway: "192.168.2.1",
		DnsSnapshot: map[string][]string{"Wi-Fi": nil}, PFToken: "12345", AnchorLoaded: true,
		StrictKillSwitch: true,
	})

	m.Recover()

	fr := m.run.(*fakeRunner)
	// Strict: PF НЕ трогаем (ни -F all, ни -X), только DNS и маршруты.
	if contains2(fr.lines(), "-F") || contains2(fr.lines(), "-X") {
		t.Fatalf("strict recovery не должен снимать PF: %v", fr.lines())
	}
	want := []string{
		"/usr/sbin/networksetup -setdnsservers Wi-Fi empty",
		"/sbin/route -q -n delete -inet 0.0.0.0/1 -interface utun9",
		"/sbin/route -q -n delete -inet 128.0.0.0/1 -interface utun9",
		"/sbin/route -q -n delete -inet 222.167.208.108",
	}
	assertSeq(t, "recover strict", fr.lines(), want)
	st, _ := vpnmut.ReadState(m.statePath)
	if st.Phase != vpnmut.PhasePFSet || st.PFToken != "12345" {
		t.Fatalf("strict recovery должен сохранить PF в state (pf-set, token): %+v", st)
	}
}

func TestRecoverCleanIsNoop(t *testing.T) {
	m := newTestManager(t, nil)
	writeStale(t, m, vpnmut.State{Phase: vpnmut.PhaseClean})
	m.Recover()
	if n := len(m.run.(*fakeRunner).calls); n != 0 {
		t.Fatalf("recovery по чистому state не должен слать команд, got %d", n)
	}
}

// --- watchdog -----------------------------------------------------------------

func TestWatchdogReloadsOnEmptyAnchor(t *testing.T) {
	m := newTestManager(t, nil)
	fr := newFakeRunner()
	fr.anchorShow = "" // якорь слетел
	m.run = fr
	m.st = vpnmut.State{Phase: vpnmut.PhaseConnected, Utun: "utun9"}
	sess := &session{pfRules: "block return out all\n", serverIP: "222.167.208.108", gateway: "192.168.2.1", utun: "utun9"}

	m.watchdogCheck(context.Background(), sess)

	if !contains2(fr.lines(), "-f") {
		t.Fatalf("watchdog при пустом якоре должен перезагрузить правила (pfctl -f -): %v", fr.lines())
	}
}

// --- вспомогалки --------------------------------------------------------------

func writeStale(t *testing.T, m *Manager, st vpnmut.State) {
	t.Helper()
	if err := vpnmut.WriteState(m.statePath, &st); err != nil {
		t.Fatalf("writeStale: %v", err)
	}
}

func contains2(lines []string, token string) bool {
	for _, l := range lines {
		for _, f := range strings.Fields(l) {
			if f == token {
				return true
			}
		}
	}
	return false
}

// assertSeq сверяет фактическую последовательность строк-команд с ожидаемой.
func assertSeq(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d команд, ожидалось %d\n got=%v\nwant=%v", label, len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: команда #%d:\n got=%q\nwant=%q", label, i, got[i], want[i])
		}
	}
}
