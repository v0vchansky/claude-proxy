package control

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/v0vchansky/claude-proxy/core/internal/health"
	"github.com/v0vchansky/claude-proxy/core/internal/logbuf"
	"github.com/v0vchansky/claude-proxy/core/internal/profile"
	"github.com/v0vchansky/claude-proxy/core/internal/provision"
	"github.com/v0vchansky/claude-proxy/core/internal/proxy"
	"github.com/v0vchansky/claude-proxy/core/internal/tunnel"
)

const (
	handshakeTimeout = 10 * time.Second
	healthInterval   = 15 * time.Second
	healthDialTO     = 8 * time.Second
	// staleHandshake — если handshake старше этого, считаем туннель упавшим.
	staleHandshake = 3 * time.Minute
)

// Daemon держит сеть и состояние. Команды Connect/Disconnect/Switch сериализованы
// opMu; поля состояния защищены mu (короткие критические секции под опрос status).
type Daemon struct {
	proxyAddr    string
	healthTarget string
	log          *logbuf.Buffer
	verbose      bool

	opMu sync.Mutex // сериализует длинные операции (connect/disconnect/switch)

	mu   sync.Mutex // защищает поля ниже
	st   State
	tun  *tunnel.Tunnel
	prx  *proxy.Proxy
	prof profile.Profile
	priv string // приватный ключ клиента в памяти; не логируется, не сохраняется

	healthCancel context.CancelFunc
}

// NewDaemon создаёт демон. proxyAddr обычно "127.0.0.1:8118".
func NewDaemon(proxyAddr, healthTarget string, log *logbuf.Buffer, verbose bool) *Daemon {
	if healthTarget == "" {
		healthTarget = health.DefaultTarget
	}
	return &Daemon{
		proxyAddr:    proxyAddr,
		healthTarget: healthTarget,
		log:          log,
		verbose:      verbose,
		st: State{
			State:      StateDisconnected,
			LocalProxy: proxyAddr,
			PingMs:     -1,
		},
	}
}

// Status возвращает текущий снимок состояния (c дозаполнением live-счётчиков).
func (d *Daemon) Status() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	st := d.st
	if d.tun != nil {
		if s, err := d.tun.Stats(); err == nil {
			st.RxBytes = s.RxBytes
			st.TxBytes = s.TxBytes
			st.LastHandshakeUnix = s.LastHandshakeUnix
		}
	}
	return st
}

// Logs возвращает строки технического лога.
func (d *Daemon) Logs() []string {
	return d.log.Lines()
}

// dial — единственный путь наружу для proxy. Если туннеля нет, соединение не
// устанавливается (fail-closed): прямого выхода в системную сеть не существует.
func (d *Daemon) dial(ctx context.Context, network, address string) (net.Conn, error) {
	d.mu.Lock()
	t := d.tun
	d.mu.Unlock()
	if t == nil {
		return nil, fmt.Errorf("Tunnel not initialized")
	}
	return t.DialContext(ctx, network, address)
}

// Connect поднимает туннель по профилю и запускает local proxy.
func (d *Daemon) Connect(p profile.Profile, privateKey string) (State, error) {
	d.opMu.Lock()
	defer d.opMu.Unlock()

	if d.isConnected() {
		d.teardown("переподключение")
	}
	d.setState(StateConnecting, "")
	d.log.Logf("Selected server: %s", p.DisplayName)
	d.log.Logf("Tunnel starting")

	if err := d.bringUp(p, privateKey); err != nil {
		return d.fail(err), err
	}
	return d.Status(), nil
}

// Switch — контролируемая смена сервера без прямого fallback.
func (d *Daemon) Switch(p profile.Profile, privateKey string) (State, error) {
	d.opMu.Lock()
	defer d.opMu.Unlock()

	d.setState(StateSwitching, "")
	d.log.Logf("Switching to: %s", p.DisplayName)
	d.teardown("смена сервера")

	if err := d.bringUp(p, privateKey); err != nil {
		return d.fail(err), err
	}
	return d.Status(), nil
}

// Disconnect останавливает proxy и туннель.
func (d *Daemon) Disconnect() State {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	d.teardown("выключение")
	d.setState(StateDisconnected, "")
	d.mu.Lock()
	d.st.PingMs = -1
	d.st.ConnectedSinceUnix = 0
	d.mu.Unlock()
	d.log.Logf("Disconnected")
	return d.Status()
}

// Provision разворачивает/усыновляет сервер по SSH. Не трогает текущий туннель.
// Возвращает результат с профилем и собранным логом шагов.
func (d *Daemon) Provision(ssh provision.SSHConfig, params provision.Params, clientPub string) (provision.Result, error) {
	d.log.Logf("Provision: %s", ssh.Host)
	var steps []string
	res, err := provision.Provision(ssh, params, clientPub, func(s string) {
		steps = append(steps, s)
		d.log.Logf("provision: %s", s)
	})
	res.Log = steps
	if err != nil {
		d.log.Logf("Provision error: %v", err)
	}
	return res, err
}

// Healthcheck выполняет проверку сейчас и обновляет состояние.
func (d *Daemon) Healthcheck() State {
	if !d.isConnected() {
		return d.Status()
	}
	ctx, cancel := context.WithTimeout(context.Background(), healthDialTO)
	defer cancel()
	ms, err := health.Check(ctx, d.dial, d.healthTarget)
	d.mu.Lock()
	d.st.LastCheckUnix = time.Now().Unix()
	if err != nil {
		d.st.PingMs = -1
		d.st.State = StateError
		d.st.LastError = err.Error()
	} else {
		d.st.PingMs = ms
		if d.st.State == StateError {
			d.st.State = StateConnected
			d.st.LastError = ""
		}
	}
	d.mu.Unlock()
	if err != nil {
		d.log.Logf("Health check failed: %v", err)
	} else {
		d.log.Logf("Health check: %d ms", ms)
	}
	return d.Status()
}

// bringUp открывает туннель, ждёт handshake, запускает proxy и первый health check.
// Вызывается под opMu.
func (d *Daemon) bringUp(p profile.Profile, privateKey string) error {
	var devLog func(string, ...any)
	if d.verbose {
		devLog = d.log.Logf
	}
	t, err := tunnel.Open(p, privateKey, devLog)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	err = t.WaitHandshake(ctx)
	cancel()
	if err != nil {
		t.Close()
		return err
	}
	d.log.Logf("Handshake established")

	d.mu.Lock()
	d.tun = t
	d.prof = p
	d.priv = privateKey
	d.mu.Unlock()

	if err := d.ensureProxy(); err != nil {
		d.mu.Lock()
		d.tun = nil
		d.mu.Unlock()
		t.Close()
		return err
	}

	// Первая проверка — наполнить ping перед выдачей Connected.
	hctx, hcancel := context.WithTimeout(context.Background(), healthDialTO)
	ms, herr := health.Check(hctx, d.dial, d.healthTarget)
	hcancel()

	d.mu.Lock()
	d.st.State = StateConnected
	d.st.ProfileID = p.ID
	d.st.ServerName = p.DisplayName
	d.st.ServerHost = p.Host
	d.st.ServerPort = p.Port
	d.st.LocalProxy = d.proxyAddr
	d.st.ConnectedSinceUnix = time.Now().Unix()
	d.st.LastCheckUnix = time.Now().Unix()
	d.st.LastError = ""
	if herr != nil {
		d.st.PingMs = -1
	} else {
		d.st.PingMs = ms
	}
	d.mu.Unlock()

	d.startHealthLoop()
	return nil
}

func (d *Daemon) ensureProxy() error {
	d.mu.Lock()
	running := d.prx != nil
	d.mu.Unlock()
	if running {
		return nil
	}
	prx, addr, err := proxy.Start(d.proxyAddr, d.dial, d.log.Logf)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.prx = prx
	d.proxyAddr = addr
	d.st.LocalProxy = addr
	d.mu.Unlock()
	d.log.Logf("Proxy listening on %s", addr)
	return nil
}

// teardown останавливает health loop, proxy и туннель. Вызывается под opMu.
func (d *Daemon) teardown(reason string) {
	d.stopHealthLoop()

	d.mu.Lock()
	prx := d.prx
	tun := d.tun
	d.prx = nil
	d.tun = nil
	d.priv = ""
	d.mu.Unlock()

	if prx != nil {
		prx.Stop()
		d.log.Logf("Proxy stopped (%s)", reason)
	}
	if tun != nil {
		tun.Close()
		d.log.Logf("Tunnel stopped (%s)", reason)
	}
}

func (d *Daemon) startHealthLoop() {
	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.healthCancel = cancel
	d.mu.Unlock()

	go func() {
		ticker := time.NewTicker(healthInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				d.runHealthTick(ctx)
			}
		}
	}()
}

func (d *Daemon) stopHealthLoop() {
	d.mu.Lock()
	cancel := d.healthCancel
	d.healthCancel = nil
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (d *Daemon) runHealthTick(ctx context.Context) {
	// Проверка свежести handshake.
	d.mu.Lock()
	t := d.tun
	d.mu.Unlock()
	if t == nil {
		return
	}
	if s, err := t.Stats(); err == nil && s.LastHandshakeUnix > 0 {
		age := time.Since(time.Unix(s.LastHandshakeUnix, 0))
		if age > staleHandshake {
			d.mu.Lock()
			d.st.State = StateError
			d.st.PingMs = -1
			d.st.LastError = "Server unreachable"
			d.st.LastCheckUnix = time.Now().Unix()
			d.mu.Unlock()
			d.log.Logf("Health check: handshake stale (%s)", age.Round(time.Second))
			return
		}
	}

	hctx, cancel := context.WithTimeout(ctx, healthDialTO)
	ms, err := health.Check(hctx, d.dial, d.healthTarget)
	cancel()

	d.mu.Lock()
	d.st.LastCheckUnix = time.Now().Unix()
	if err != nil {
		d.st.PingMs = -1
		d.st.State = StateError
		d.st.LastError = err.Error()
	} else {
		d.st.PingMs = ms
		if d.st.State == StateError {
			d.st.State = StateConnected
			d.st.LastError = ""
		}
	}
	d.mu.Unlock()
	if err == nil {
		d.log.Logf("Health check: %d ms", ms)
	}
}

func (d *Daemon) isConnected() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.tun != nil
}

func (d *Daemon) setState(s ConnState, lastErr string) {
	d.mu.Lock()
	d.st.State = s
	if s != StateError {
		d.st.LastError = lastErr
	} else if lastErr != "" {
		d.st.LastError = lastErr
	}
	d.mu.Unlock()
}

func (d *Daemon) fail(err error) State {
	d.teardown("ошибка подключения")
	d.mu.Lock()
	d.st.State = StateError
	d.st.PingMs = -1
	d.st.LastError = err.Error()
	d.mu.Unlock()
	d.log.Logf("Error: %v", err)
	return d.Status()
}

// Shutdown корректно гасит демон при завершении процесса.
func (d *Daemon) Shutdown() {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	d.teardown("завершение процесса")
}
