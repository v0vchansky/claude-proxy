// Package fullvpn поднимает РЕАЛЬНЫЙ туннель full-tunnel VPN: kernel utun через
// amneziawg-go/tun.CreateTUN + устройство AmneziaWG на нём. В отличие от
// internal/tunnel (netstack-вариант userspace-прокси), здесь интерфейс — настоящий
// utunN в системе, через который пойдёт весь трафик ОС (docs/full-vpn-design.md §2.1, §4 фаза 1).
//
// Пакет НЕ трогает системную сеть: он только создаёт utun, поднимает на нём WG и
// отдаёт имя интерфейса. Назначение IP (ifconfig), маршруты, DNS и PF kill-switch —
// забота оркестратора (демон vpnd), фазы 2–5 §4.
//
// Создание utun и Up требуют root (connect к com.apple.net.utun_control, §2.1),
// поэтому живой подъём проверяется вручную под sudo; юнит-тесты покрывают только
// то, что не требует root (разбор счётчиков, вспомогательные функции).
package fullvpn

import (
	"context"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun"

	"github.com/v0vchansky/claude-proxy/core/internal/profile"
)

// handshakePollInterval — как часто опрашиваем last_handshake_time_sec при ожидании
// первого handshake (как в internal/tunnel).
const handshakePollInterval = 200 * time.Millisecond

// Tunnel — поднятый full-tunnel: реальный utun + устройство AmneziaWG на нём.
type Tunnel struct {
	dev  *device.Device
	tun  tun.Device
	name string // имя интерфейса, напр. "utun9" — его оркестратор подставит в ifconfig/route/PF
}

// Stats — счётчики и время последнего handshake устройства.
type Stats struct {
	RxBytes           int64
	TxBytes           int64
	LastHandshakeUnix int64
}

// Open создаёт kernel utun (имя отдаёт система — первый свободный utunN), поднимает
// на нём устройство AmneziaWG с конфигом профиля (allowed_ip=0.0.0.0/0 уже в
// BuildUAPI) и включает его. Возвращает Tunnel; имя интерфейса — через DeviceName.
//
// dns передаётся ради единообразия сигнатуры с tunnel.Open и возможного будущего
// использования, но на РЕАЛЬНОМ utun DNS назначается не здесь: это фаза 4 §4
// (networksetup), её делает оркестратор. CreateTUN адрес/DNS интерфейса не трогает.
//
// logf — приёмник verbose-логов устройства (может быть nil). Приватный ключ туда
// не попадает: device логирует только метаданные, а BuildUAPI-строку мы не логируем.
//
// Требует root: без привилегий CreateTUN вернёт "operation not permitted".
func Open(p profile.Profile, privateKeyB64 string, dns []netip.Addr, logf func(string)) (*Tunnel, error) {
	_ = dns // DNS на реальном utun ставит оркестратор (фаза 4), не этот пакет

	if err := p.Validate(); err != nil {
		return nil, err
	}

	// Имя "utun" → ядро выдаст первый свободный utunN. MTU выставляется прямо здесь
	// (внутри CreateTUN через ioctl SIOCSIFMTU), отдельной командой не нужен.
	utun, err := tun.CreateTUN("utun", p.MTUOrDefault())
	if err != nil {
		return nil, fmt.Errorf("fullvpn: создание utun (нужен root): %w", err)
	}

	name, err := utun.Name()
	if err != nil {
		_ = utun.Close()
		return nil, fmt.Errorf("fullvpn: чтение имени utun: %w", err)
	}

	logger := silentLogger()
	if logf != nil {
		logger = &device.Logger{
			Verbosef: func(format string, args ...any) { logf(fmt.Sprintf("awg: "+format, args...)) },
			Errorf:   func(format string, args ...any) { logf(fmt.Sprintf("awg error: "+format, args...)) },
		}
	}

	dev := device.NewDevice(utun, conn.NewDefaultBind(), logger)

	uapi, err := p.BuildUAPI(privateKeyB64)
	if err != nil {
		dev.Close() // закроет и utun — интерфейс исчезнет из системы
		return nil, err
	}
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return nil, fmt.Errorf("fullvpn: применение конфига устройства: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("fullvpn: поднятие устройства: %w", err)
	}

	return &Tunnel{dev: dev, tun: utun, name: name}, nil
}

// DeviceName возвращает имя интерфейса (utunN), которое система выдала при создании.
func (t *Tunnel) DeviceName() string { return t.name }

// WaitHandshake ждёт первый успешный handshake (last_handshake_time_sec > 0) или
// истечения ctx. Handshake идёт по текущему системному default-маршруту — на этом
// шаге сеть ещё не тронута (маршруты/DNS/PF ставятся позже), поэтому неудача здесь
// означает «вернуть всё как было» простым Close.
func (t *Tunnel) WaitHandshake(ctx context.Context) error {
	ticker := time.NewTicker(handshakePollInterval)
	defer ticker.Stop()
	for {
		if st, err := t.Stats(); err == nil && st.LastHandshakeUnix > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("fullvpn: handshake не состоялся: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// Stats читает счётчики устройства через IpcGet (тот же парсинг, что в internal/tunnel).
func (t *Tunnel) Stats() (Stats, error) {
	raw, err := t.dev.IpcGet()
	if err != nil {
		return Stats{}, err
	}
	return parseStats(raw), nil
}

// Close останавливает устройство и закрывает utun. При закрытии fd интерфейс utun
// исчезает из системы, и ядро само вычищает все маршруты через него (§2.1) — это
// опора сценария краша (§9).
func (t *Tunnel) Close() error {
	if t.dev != nil {
		t.dev.Close() // device.Close закрывает и переданный tun.Device
	}
	return nil
}

// parseStats разбирает ответ IpcGet (строки key=value) в Stats. Вынесено отдельной
// чистой функцией — тестируется без root.
func parseStats(raw string) Stats {
	var s Stats
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "rx_bytes":
			s.RxBytes, _ = strconv.ParseInt(v, 10, 64)
		case "tx_bytes":
			s.TxBytes, _ = strconv.ParseInt(v, 10, 64)
		case "last_handshake_time_sec":
			s.LastHandshakeUnix, _ = strconv.ParseInt(v, 10, 64)
		}
	}
	return s
}

func silentLogger() *device.Logger {
	return &device.Logger{
		Verbosef: func(string, ...any) {},
		Errorf:   func(string, ...any) {},
	}
}
