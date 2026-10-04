// Package health — проверка туннеля: TCP-коннект до внешней цели через туннель
// с замером RTT. Системный маршрут macOS не используется (диалер — только туннель).
package health

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

// DefaultTarget — цель проверки (TCP/443, отвечает быстро, доступна из любого egress).
const DefaultTarget = "1.1.1.1:443"

// DialFunc совпадает с tunnel.DialContext.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Check измеряет время установления TCP-соединения до target через dial.
// Возвращает ping в миллисекундах или ошибку (которую UI показывает как Last error).
func Check(ctx context.Context, dial DialFunc, target string) (int, error) {
	if target == "" {
		target = DefaultTarget
	}
	start := time.Now()
	conn, err := dial(ctx, "tcp", target)
	if err != nil {
		return -1, friendly(err)
	}
	rtt := time.Since(start)
	_ = conn.Close()
	ms := int(rtt.Milliseconds())
	if ms < 1 {
		ms = 1
	}
	return ms, nil
}

// friendly приводит низкоуровневые ошибки к формулировкам из ТЗ (раздел 10).
func friendly(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("Server unreachable")
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "Tunnel not initialized"):
		return fmt.Errorf("Tunnel not initialized")
	case strings.Contains(msg, "DNS resolution failed"):
		return fmt.Errorf("DNS resolution failed")
	case strings.Contains(msg, "timeout"):
		return fmt.Errorf("Server unreachable")
	default:
		return err
	}
}
