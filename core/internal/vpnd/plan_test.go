package vpnd

import (
	"strings"
	"testing"

	"github.com/v0vchansky/claude-proxy/core/internal/vpnmut"
)

// TestBuildConnectPlanOrder — чистая проверка плана фаз 2–5 (без exec, без root):
// порядок, argv, фазы и kind каждого шага.
func TestBuildConnectPlanOrder(t *testing.T) {
	plan := buildConnectPlan(
		"utun9", "222.167.208.108", "192.168.2.1", "10.77.0.2",
		[]string{"Wi-Fi"}, []string{"1.1.1.1", "8.8.8.8"}, "RULES",
	)

	type exp struct {
		argv  string
		phase vpnmut.Phase
		kind  cmdKind
	}
	want := []exp{
		{"/sbin/ifconfig utun9 inet 10.77.0.2/32 10.77.0.2 alias", vpnmut.PhasePreparing, cmdPlain},
		{"/sbin/ifconfig utun9 up", vpnmut.PhasePreparing, cmdPlain},
		{"/sbin/route -q -n add -inet 222.167.208.108 -gateway 192.168.2.1", vpnmut.PhaseRouted, cmdPlain},
		{"/sbin/route -q -n add -inet 0.0.0.0/1 -interface utun9", vpnmut.PhaseRouted, cmdPlain},
		{"/sbin/route -q -n add -inet 128.0.0.0/1 -interface utun9", vpnmut.PhaseRouted, cmdPlain},
		{"/usr/sbin/networksetup -setdnsservers Wi-Fi 1.1.1.1 8.8.8.8", vpnmut.PhaseDNSSet, cmdPlain},
		{"/sbin/pfctl -a com.apple/250.ClaudeProxyVPN -f -", vpnmut.PhasePFSet, cmdPFLoad},
		{"/sbin/pfctl -E", vpnmut.PhasePFSet, cmdPFEnable},
	}
	if len(plan) != len(want) {
		t.Fatalf("план из %d шагов, ожидалось %d", len(plan), len(want))
	}
	for i, w := range want {
		if got := strings.Join(plan[i].argv, " "); got != w.argv {
			t.Fatalf("шаг #%d argv:\n got=%q\nwant=%q", i, got, w.argv)
		}
		if plan[i].phase != w.phase {
			t.Fatalf("шаг #%d phase=%s, ожидалось %s", i, plan[i].phase, w.phase)
		}
		if plan[i].kind != w.kind {
			t.Fatalf("шаг #%d kind=%d, ожидалось %d", i, plan[i].kind, w.kind)
		}
	}
	// Правила PF подаются на stdin шага pf-load.
	if plan[6].stdin != "RULES" {
		t.Fatalf("pf load stdin=%q, ожидалось RULES", plan[6].stdin)
	}
}

// TestBuildTeardownCommandsTable — чистая таблица «фаза × флаг → команды отката».
func TestBuildTeardownCommandsTable(t *testing.T) {
	base := func() *vpnmut.State {
		return &vpnmut.State{
			Utun: "utun9", ServerIP: "222.167.208.108", OrigGateway: "192.168.2.1",
			DnsSnapshot: map[string][]string{"Wi-Fi": nil},
			PFToken:     "12345", AnchorLoaded: true,
		}
	}

	cases := []struct {
		name   string
		mutate func(*vpnmut.State)
		keepPF bool
		want   []string
	}{
		{
			name:   "connected full",
			mutate: func(s *vpnmut.State) { s.Phase = vpnmut.PhaseConnected },
			want: []string{
				"/sbin/pfctl -a com.apple/250.ClaudeProxyVPN -F all",
				"/sbin/pfctl -X 12345",
				"/usr/sbin/networksetup -setdnsservers Wi-Fi empty",
				"/sbin/route -q -n delete -inet 0.0.0.0/1 -interface utun9",
				"/sbin/route -q -n delete -inet 128.0.0.0/1 -interface utun9",
				"/sbin/route -q -n delete -inet 222.167.208.108",
			},
		},
		{
			name: "routed only (pf/dns не трогаем)",
			mutate: func(s *vpnmut.State) {
				s.Phase = vpnmut.PhaseRouted
				s.AnchorLoaded = false
				s.PFToken = ""
			},
			want: []string{
				"/sbin/route -q -n delete -inet 0.0.0.0/1 -interface utun9",
				"/sbin/route -q -n delete -inet 128.0.0.0/1 -interface utun9",
				"/sbin/route -q -n delete -inet 222.167.208.108",
			},
		},
		{
			name:   "preparing (нечего снимать)",
			mutate: func(s *vpnmut.State) { s.Phase = vpnmut.PhasePreparing; s.AnchorLoaded = false; s.PFToken = "" },
			want:   nil,
		},
		{
			name:   "keepPF — PF остаётся",
			mutate: func(s *vpnmut.State) { s.Phase = vpnmut.PhaseConnected },
			keepPF: true,
			want: []string{
				"/usr/sbin/networksetup -setdnsservers Wi-Fi empty",
				"/sbin/route -q -n delete -inet 0.0.0.0/1 -interface utun9",
				"/sbin/route -q -n delete -inet 128.0.0.0/1 -interface utun9",
				"/sbin/route -q -n delete -inet 222.167.208.108",
			},
		},
		{
			name:   "pf loaded, enable не дошёл (токена нет) — flush без disable",
			mutate: func(s *vpnmut.State) { s.Phase = vpnmut.PhasePFSet; s.PFToken = "" },
			want: []string{
				"/sbin/pfctl -a com.apple/250.ClaudeProxyVPN -F all",
				"/usr/sbin/networksetup -setdnsservers Wi-Fi empty",
				"/sbin/route -q -n delete -inet 0.0.0.0/1 -interface utun9",
				"/sbin/route -q -n delete -inet 128.0.0.0/1 -interface utun9",
				"/sbin/route -q -n delete -inet 222.167.208.108",
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := base()
			c.mutate(st)
			cmds := buildTeardownCommands(st, c.keepPF)
			got := make([]string, len(cmds))
			for i, a := range cmds {
				got[i] = strings.Join(a, " ")
			}
			assertSeq(t, c.name, got, c.want)
		})
	}
}
