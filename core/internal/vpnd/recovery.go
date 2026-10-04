package vpnd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/v0vchansky/claude-proxy/core/internal/killswitch"
	"github.com/v0vchansky/claude-proxy/core/internal/vpnmut"
)

// watchdogInterval — период проверок watchdog при connected (§9). 60с как в дизайне.
const watchdogInterval = 60 * time.Second

// Recover выполняет crash-recovery на старте демона (§9): читает stale state и, если
// сеанс не был чисто завершён (phase != clean), восстанавливает сеть. Туннеля в
// памяти нет — процесс/машина падали, utun исчез вместе с fd (и ядро убрало его
// маршруты, §2.1), поэтому остаётся снять host-route, DNS и PF по снимку из state.
//
// Политика (§9):
//   - non-strict (дефолт): полный teardown по снимку → сеть восстановлена, state=clean.
//   - strict: PF-kill-switch оставить активным (сеть закрыта, fail-closed), снять лишь
//     DNS и маршруты; state сводится к pf-set, чтобы последующий disconnect-full снял PF.
func (m *Manager) Recover() {
	m.mu.Lock()
	defer m.mu.Unlock()

	st, err := vpnmut.LoadOrClean(m.statePath)
	if err != nil {
		m.logf("recover: state-файл не читается (" + err.Error() + ") — пропускаю")
		return
	}
	if st.Phase == vpnmut.PhaseClean {
		m.st = *st
		return
	}

	m.logf(fmt.Sprintf("recover: незавершённый сеанс (phase=%s, strict=%v) — восстанавливаю сеть", st.Phase, st.StrictKillSwitch))
	m.tun = nil // туннеля в памяти быть не может: это свежий процесс

	if st.StrictKillSwitch {
		// Strict: PF оставляем, снимаем только DNS и маршруты.
		m.st = *st
		for _, argv := range buildTeardownCommands(&m.st, true /*keepPF*/) {
			if _, stderr, rerr := m.run.Run(context.Background(), "", argv); rerr != nil {
				m.logf(fmt.Sprintf("  recover: %v: %v (stderr=%q)", argv, rerr, strings.TrimSpace(stderr)))
			}
		}
		// Сводим state к «активен только PF», чтобы disconnect-full позже снял якорь.
		m.st = vpnmut.State{
			Phase:            vpnmut.PhasePFSet,
			ServerIP:         st.ServerIP,
			ServerPort:       st.ServerPort,
			AnchorLoaded:     st.AnchorLoaded,
			PFToken:          st.PFToken,
			StrictKillSwitch: true,
		}
		if werr := m.writeState(); werr != nil {
			m.logf("  recover: запись state: " + werr.Error())
		}
		m.logf("recover: strict — PF-kill-switch оставлен активным, сеть закрыта до явного connect-full/disconnect-full")
		return
	}

	// Non-strict: полный teardown, сеть восстановлена.
	m.st = *st
	for _, e := range m.teardownLocked(context.Background(), false) {
		m.logf("  recover: " + e.Error())
	}
	m.logf("recover: сеть восстановлена (non-strict)")
}

// startWatchdogLocked запускает watchdog-горутину сеанса. Требует mu.
func (m *Manager) startWatchdogLocked() {
	m.stopWatchdogLocked()
	stop := make(chan struct{})
	m.wdStop = stop
	sess := m.session
	go m.watchdogLoop(stop, sess)
}

// stopWatchdogLocked останавливает watchdog-горутину (идемпотентно). Требует mu.
func (m *Manager) stopWatchdogLocked() {
	if m.wdStop != nil {
		close(m.wdStop)
		m.wdStop = nil
	}
}

// watchdogLoop раз в watchdogInterval дергает watchdogCheck, пока жив сеанс.
func (m *Manager) watchdogLoop(stop <-chan struct{}, sess *session) {
	ticker := time.NewTicker(watchdogInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			m.watchdogCheck(context.Background(), sess)
		}
	}
}

// watchdogCheck — одна проверка здоровья kill-switch и host-route (§9, MVP):
//   - PF-якорь пуст/недоступен → перезагрузить правила из сеанса;
//   - host-route до сервера не идёт через физ. шлюз → переставить.
//
// Всё логируется; teardown по флагу strict в MVP не делаем — только чиним слои.
// Вынесена отдельным методом, чтобы тест мог вызвать её синхронно (без таймера).
func (m *Manager) watchdogCheck(ctx context.Context, sess *session) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.st.Phase != vpnmut.PhaseConnected || sess == nil {
		return
	}

	// 1. PF-якорь на месте? `pfctl -a <anchor> -sr` должен отдавать непустой ruleset.
	stdout, _, err := m.run.Run(ctx, "", []string{pfctlBin, "-a", killswitch.AnchorPath, "-sr"})
	if err != nil || strings.TrimSpace(stdout) == "" {
		m.logf("watchdog: PF-якорь пуст/недоступен — перезагружаю правила")
		if _, stderr, lerr := m.run.Run(ctx, sess.pfRules, killswitch.LoadRulesArgv()); lerr != nil {
			m.logf("watchdog: перезагрузка PF-правил не удалась: " + strings.TrimSpace(stderr))
		}
	}

	// 2. host-route до сервера жив и идёт через физ. шлюз?
	rstdout, _, rerr := m.run.Run(ctx, "", []string{routeBin, "-n", "get", sess.serverIP})
	if rerr != nil || !strings.Contains(rstdout, sess.gateway) {
		m.logf("watchdog: host-route до сервера слетел — переставляю через " + sess.gateway)
		if _, stderr, aerr := m.run.Run(ctx, "", []string{routeBin, "-q", "-n", "add", "-inet", sess.serverIP, "-gateway", sess.gateway}); aerr != nil {
			m.logf("watchdog: переустановка host-route не удалась: " + strings.TrimSpace(stderr))
		}
	}
}
