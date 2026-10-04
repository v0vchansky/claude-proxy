package vpnd

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/v0vchansky/claude-proxy/core/internal/killswitch"
	"github.com/v0vchansky/claude-proxy/core/internal/logbuf"
	"github.com/v0vchansky/claude-proxy/core/internal/netcfg"
	"github.com/v0vchansky/claude-proxy/core/internal/profile"
	"github.com/v0vchansky/claude-proxy/core/internal/vpnmut"
)

// handshakeTimeout — потолок ожидания первого handshake в фазе 1 (как в прокси, §4).
const handshakeTimeout = 10 * time.Second

// session — параметры активного сеанса, нужные watchdog'у для переустановки
// слетевших слоёв (PF-якорь, host-route) без повторного preflight.
type session struct {
	pfRules  string
	serverIP string
	gateway  string
	utun     string
}

// Manager — оркестратор full-tunnel VPN: держит состояние сеанса, выполняет фазовые
// connect/disconnect (§4), crash-recovery и watchdog (§9). Все изменения системы
// идут через commandRunner (exec за интерфейсом → подменяем в тестах), туннель — через
// tunnelOpener (fullvpn → подменяем в тестах). Весь внешний доступ сериализован mu.
type Manager struct {
	mu        sync.Mutex
	statePath string
	strict    bool // strict: без Allow-LAN в PF и с сохранением kill-switch при крахе
	logf      func(string)

	run     commandRunner
	open    tunnelOpener
	capture func(ctx context.Context) (*netcfg.Snapshot, error)

	tun     tunnelHandle // активный туннель (nil вне сеанса)
	st      vpnmut.State // зеркало state-файла в памяти
	session *session

	wdStop chan struct{} // закрытие останавливает watchdog-горутину
}

// NewManager собирает боевой Manager: exec поверх os/exec, туннель через fullvpn,
// снимок сети через netcfg.Capture. logf пишет в журнал (может быть nil).
func NewManager(statePath string, strict bool, log *logbuf.Buffer) *Manager {
	logf := func(string) {}
	if log != nil {
		logf = func(s string) { log.Logf("%s", s) }
	}
	return &Manager{
		statePath: statePath,
		strict:    strict,
		logf:      logf,
		run:       execRunner{},
		open:      realOpener,
		capture:   netcfg.Capture,
		st:        vpnmut.State{Phase: vpnmut.PhaseClean},
	}
}

// writeState атомарно сохраняет зеркало state в файл.
func (m *Manager) writeState() error {
	return vpnmut.WriteState(m.statePath, &m.st)
}

// Connect выполняет полную последовательность фаз 0–6 (§4). На любой ошибке —
// откат уже сделанных шагов (teardownLocked) и возврат ошибки без утечки: сеть
// возвращается в исходное состояние.
func (m *Manager) Connect(p profile.Profile, privateKeyB64 string) (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.tun != nil || m.st.Phase != vpnmut.PhaseClean {
		return nil, fmt.Errorf("vpnd: сеанс уже активен (phase=%s) — сначала disconnect-full", m.st.Phase)
	}
	if err := p.Validate(); err != nil {
		return nil, fmt.Errorf("vpnd: профиль невалиден: %w", err)
	}

	ctx := context.Background()

	// Фаза 0 — preflight: снимок сети (ничего не меняем).
	m.logf("connect-full: preflight — снимок сети")
	snap, err := m.capture(ctx)
	if err != nil {
		return nil, fmt.Errorf("vpnd: preflight (netcfg.Capture): %w", err)
	}
	serverIP, err := resolveServerIP(p.Host)
	if err != nil {
		return nil, fmt.Errorf("vpnd: серверный IP: %w", err)
	}
	clientAddr, err := p.ClientAddr()
	if err != nil {
		return nil, fmt.Errorf("vpnd: clientVpnAddress: %w", err)
	}
	gateway := snap.DefaultRoute.Gateway
	physIf := snap.DefaultRoute.Device
	dnsServices := enabledServiceNames(snap.Services)
	dnsServers := tunnelDNS(p)
	doubleVPN := strings.HasPrefix(physIf, "utun")

	// Инициализируем state фазой preparing ДО любых изменений системы (§9).
	m.st = vpnmut.State{
		Phase:             vpnmut.PhasePreparing,
		ServerIP:          serverIP,
		ServerPort:        p.Port,
		OrigGateway:       gateway,
		PhysIf:            physIf,
		DnsSnapshot:       snap.DNS,
		StrictKillSwitch:  m.strict,
		ReconnectIntended: true,
	}
	if err := m.writeState(); err != nil {
		m.st = vpnmut.State{Phase: vpnmut.PhaseClean}
		return nil, fmt.Errorf("vpnd: запись state (preparing): %w", err)
	}

	// Фаза 1 — utun + устройство WG + handshake. Пока сеть не тронута: неудача → Close.
	m.logf("connect-full: фаза 1 — поднимаю utun и устройство WG")
	tun, err := m.open(p, privateKeyB64, p.DNSAddrs(), m.logf)
	if err != nil {
		m.rollback(ctx, "открытие utun/WG")
		return nil, fmt.Errorf("vpnd: фаза 1 (utun/WG): %w", err)
	}
	m.tun = tun
	m.st.Utun = tun.DeviceName()
	if err := m.writeState(); err != nil {
		m.rollback(ctx, "запись state (utun)")
		return nil, err
	}

	hctx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	err = tun.WaitHandshake(hctx)
	cancel()
	if err != nil {
		m.rollback(ctx, "handshake")
		return nil, fmt.Errorf("vpnd: фаза 1 (handshake): %w", err)
	}
	m.logf("connect-full: handshake ок, интерфейс " + m.st.Utun)

	// Фазы 2–5 — системные изменения строго по плану, state пишется до каждого шага.
	pfRules := killswitch.RenderRules(killswitch.Params{
		UtunIf:     m.st.Utun,
		ServerIP:   serverIP,
		ServerPort: p.Port,
		AllowLAN:   !m.strict,
	})
	plan := buildConnectPlan(m.st.Utun, serverIP, gateway, clientAddr.String(), dnsServices, dnsServers, pfRules)

	for _, c := range plan {
		if c.phase != "" && c.phase != m.st.Phase {
			m.st.Phase = c.phase
			if err := m.writeState(); err != nil {
				m.rollback(ctx, "запись state ("+string(c.phase)+")")
				return nil, err
			}
		}
		_, stderr, runErr := m.run.Run(ctx, c.stdin, c.argv)
		if runErr != nil {
			m.logf(fmt.Sprintf("connect-full: шаг %q упал: %v; stderr=%q", c.name, runErr, strings.TrimSpace(stderr)))
			m.rollback(ctx, c.name)
			return nil, fmt.Errorf("vpnd: шаг %q: %w", c.name, runErr)
		}
		switch c.kind {
		case cmdPFLoad:
			// Якорь загружен — фиксируем до включения, чтобы крах между load и
			// enable был снимаемым в recovery.
			m.st.AnchorLoaded = true
			if err := m.writeState(); err != nil {
				m.rollback(ctx, "запись state (anchor)")
				return nil, err
			}
		case cmdPFEnable:
			token, perr := killswitch.ParseEnableToken(stderr)
			if perr != nil {
				m.logf("connect-full: " + perr.Error())
				m.rollback(ctx, "парс pf-токена")
				return nil, fmt.Errorf("vpnd: шаг %q: %w", c.name, perr)
			}
			m.st.PFToken = token
			if err := m.writeState(); err != nil {
				m.rollback(ctx, "запись state (pf-token)")
				return nil, err
			}
		}
	}

	// Фаза 6 — рабочий режим.
	m.st.Phase = vpnmut.PhaseConnected
	if err := m.writeState(); err != nil {
		m.rollback(ctx, "запись state (connected)")
		return nil, err
	}

	m.session = &session{pfRules: pfRules, serverIP: serverIP, gateway: gateway, utun: m.st.Utun}
	m.startWatchdogLocked()

	m.logf(fmt.Sprintf("connect-full: подключено (utun=%s, server=%s, doubleVPN=%v)", m.st.Utun, serverIP, doubleVPN))
	res := m.statusLocked()
	res["doubleVpnWarning"] = doubleVPN
	return res, nil
}

// Disconnect выполняет штатный teardown (§4, зеркально). Идемпотентно: по чистому
// состоянию ничего не делает.
func (m *Manager) Disconnect() (map[string]any, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.st.Phase == vpnmut.PhaseClean && m.tun == nil {
		m.logf("disconnect-full: уже отключено")
		return m.statusLocked(), nil
	}
	m.logf("disconnect-full: teardown (phase=" + string(m.st.Phase) + ")")
	errs := m.teardownLocked(context.Background(), false)
	for _, e := range errs {
		m.logf("  teardown: " + e.Error())
	}
	m.logf("disconnect-full: сеть восстановлена")
	return m.statusLocked(), nil
}

// Status возвращает текущее состояние сеанса.
func (m *Manager) Status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.statusLocked()
}

// rollback — откат частично выполненного connect: полный teardown текущего state.
// Возвращаемые ошибки только логируются — наверх уходит исходная ошибка connect.
func (m *Manager) rollback(ctx context.Context, reason string) {
	m.logf("connect-full: откат из-за ошибки на шаге «" + reason + "»")
	for _, e := range m.teardownLocked(ctx, false) {
		m.logf("  откат: " + e.Error())
	}
}

// teardownLocked снимает все слои, зафиксированные в m.st (по достигнутой фазе и
// полям), закрывает туннель, останавливает watchdog и переводит state в clean.
// Ошибки шагов НЕ прерывают teardown (§4): собираем все и возвращаем. Требует mu.
func (m *Manager) teardownLocked(ctx context.Context, keepPF bool) []error {
	var errs []error

	for _, argv := range buildTeardownCommands(&m.st, keepPF) {
		if _, stderr, err := m.run.Run(ctx, "", argv); err != nil {
			errs = append(errs, fmt.Errorf("%v: %w (stderr=%q)", argv, err, strings.TrimSpace(stderr)))
		}
	}

	if m.tun != nil {
		if err := m.tun.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close utun: %w", err))
		}
		m.tun = nil
	}

	m.stopWatchdogLocked()
	m.session = nil
	m.st = vpnmut.State{Phase: vpnmut.PhaseClean}
	if err := m.writeState(); err != nil {
		errs = append(errs, fmt.Errorf("write clean state: %w", err))
	}
	return errs
}

// statusLocked собирает ответ status-full из зеркала state и (если есть) счётчиков
// активного туннеля. Требует удержания mu.
func (m *Manager) statusLocked() map[string]any {
	res := map[string]any{
		"phase":         string(m.st.Phase),
		"state":         string(m.st.Phase),
		"utun":          m.st.Utun,
		"serverHost":    m.st.ServerIP,
		"killSwitch":    m.st.AnchorLoaded && m.st.PFToken != "",
		"dnsOverridden": phaseRank(m.st.Phase) >= phaseRank(vpnmut.PhaseDNSSet),
		"strict":        m.st.StrictKillSwitch,
	}
	if m.tun != nil {
		if s, err := m.tun.Stats(); err == nil {
			res["handshakeOK"] = s.LastHandshakeUnix > 0
			res["lastHandshakeUnix"] = s.LastHandshakeUnix
			res["rxBytes"] = s.RxBytes
			res["txBytes"] = s.TxBytes
		}
	}
	return res
}
