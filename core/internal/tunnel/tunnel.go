// Package tunnel поднимает userspace AmneziaWG + netstack и даёт диалер,
// через который ходит ВЕСЬ трафик local proxy. Обычного net.Dial здесь нет —
// это и есть fail-closed: нет туннеля, нет netstack — нет соединения.
package tunnel

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun/netstack"

	"github.com/v0vchansky/claude-proxy/core/internal/profile"
)

// Tunnel — поднятый туннель. Все методы безопасно звать после Open.
type Tunnel struct {
	dev  *device.Device
	tnet *netstack.Net
}

// Stats — счётчики и время handshake из устройства.
type Stats struct {
	RxBytes           int64
	TxBytes           int64
	LastHandshakeUnix int64
}

// Open поднимает netstack-устройство и applies uapi-конфиг профиля.
// logf — приёмник verbose-логов устройства (может быть nil). Секреты туда не уходят:
// device логирует только метаданные, приватный ключ в лог не пишет.
func Open(p profile.Profile, privateKeyB64 string, logf func(format string, args ...any)) (*Tunnel, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	clientAddr, err := p.ClientAddr()
	if err != nil {
		return nil, err
	}

	tunDev, tnet, err := netstack.CreateNetTUN(
		[]netip.Addr{clientAddr},
		p.DNSAddrs(),
		p.MTUOrDefault(),
	)
	if err != nil {
		return nil, fmt.Errorf("создание netstack TUN: %w", err)
	}

	logger := silentLogger()
	if logf != nil {
		logger = &device.Logger{
			Verbosef: func(format string, args ...any) { logf("awg: "+format, args...) },
			Errorf:   func(format string, args ...any) { logf("awg error: "+format, args...) },
		}
	}

	dev := device.NewDevice(tunDev, conn.NewDefaultBind(), logger)

	uapi, err := p.BuildUAPI(privateKeyB64)
	if err != nil {
		dev.Close()
		return nil, err
	}
	if err := dev.IpcSet(uapi); err != nil {
		dev.Close()
		return nil, fmt.Errorf("применение конфига устройства: %w", err)
	}
	if err := dev.Up(); err != nil {
		dev.Close()
		return nil, fmt.Errorf("поднятие устройства: %w", err)
	}

	return &Tunnel{dev: dev, tnet: tnet}, nil
}

// WaitHandshake ждёт первый успешный handshake (last_handshake_time_sec > 0)
// или истечения ctx. Возвращает понятную ошибку при таймауте.
func (t *Tunnel) WaitHandshake(ctx context.Context) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if st, err := t.Stats(); err == nil && st.LastHandshakeUnix > 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("Handshake timeout")
		case <-ticker.C:
		}
	}
}

// Stats читает счётчики устройства через IpcGet.
func (t *Tunnel) Stats() (Stats, error) {
	raw, err := t.dev.IpcGet()
	if err != nil {
		return Stats{}, err
	}
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
	return s, nil
}

// DialContext — единственный путь наружу: резолв имени через туннельный DNS,
// затем TCP через netstack. Поддерживается только tcp.
func (t *Tunnel) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if !strings.HasPrefix(network, "tcp") {
		return nil, fmt.Errorf("через туннель поддерживается только tcp, запрошено %q", network)
	}
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("адрес %q: %w", address, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("порт %q: %w", portStr, err)
	}

	ip, err := netip.ParseAddr(host)
	if err != nil {
		// Это имя — резолвим ТОЛЬКО через туннельный DNS.
		addrs, lerr := t.tnet.LookupContextHost(ctx, host)
		if lerr != nil {
			return nil, fmt.Errorf("DNS resolution failed: %w", lerr)
		}
		if len(addrs) == 0 {
			return nil, fmt.Errorf("DNS resolution failed: нет адресов для %q", host)
		}
		ip, err = netip.ParseAddr(addrs[0])
		if err != nil {
			return nil, fmt.Errorf("DNS resolution failed: %w", err)
		}
	}

	return t.tnet.DialContextTCPAddrPort(ctx, netip.AddrPortFrom(ip, uint16(port)))
}

// Close останавливает устройство и освобождает netstack.
func (t *Tunnel) Close() error {
	if t.dev != nil {
		t.dev.Close()
	}
	return nil
}

func silentLogger() *device.Logger {
	return &device.Logger{
		Verbosef: func(string, ...any) {},
		Errorf:   func(string, ...any) {},
	}
}
