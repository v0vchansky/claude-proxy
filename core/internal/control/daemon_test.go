package control

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/v0vchansky/claude-proxy/core/internal/logbuf"
)

// newRunningDaemon поднимает демон с постоянным listener 8118 на случайном порту
// (127.0.0.1:0) и гарантирует его остановку в конце теста.
func newRunningDaemon(t *testing.T) *Daemon {
	t.Helper()
	d := NewDaemon("127.0.0.1:0", "1.1.1.1:443", logbuf.New(50), false)
	if err := d.StartProxy(); err != nil {
		t.Fatalf("StartProxy: %v", err)
	}
	t.Cleanup(d.Shutdown)
	return d
}

// startEcho — локальный TCP-эхо, имитирует «цель» для direct-dial.
func startEcho(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) { defer c.Close(); _, _ = io.Copy(c, c) }(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// TestListenerSurvivesDisconnect — порт 8118 слушает и после disconnect, а режим
// переходит в off (fail-closed). Это суть «варианта А».
func TestListenerSurvivesDisconnect(t *testing.T) {
	d := newRunningDaemon(t)
	addr := d.Status().LocalProxy
	if addr == "127.0.0.1:0" || addr == "" {
		t.Fatalf("localProxy должен быть реальным адресом, got %q", addr)
	}

	// Дисконнект без туннеля не должен уронить listener.
	st := d.Disconnect()
	if st.ForwardMode != ForwardOff {
		t.Fatalf("после disconnect forwardMode=%q, ожидалось off", st.ForwardMode)
	}
	if st.LocalProxy != addr {
		t.Fatalf("localProxy пропал после disconnect: %q", st.LocalProxy)
	}

	// Порт всё ещё принимает соединения.
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatalf("listener не слушает после disconnect: %v", err)
	}
	_ = c.Close()

	// И занять этот порт заново нельзя — listener держит его.
	if ln, err := net.Listen("tcp", addr); err == nil {
		_ = ln.Close()
		t.Fatalf("порт %s свободен — listener не живёт после disconnect", addr)
	}
}

// TestDialOffFailClosed — в режиме off (дефолт) dial обязан падать: наружу никто
// не ходит, пока нет ни туннеля, ни явного direct.
func TestDialOffFailClosed(t *testing.T) {
	d := newRunningDaemon(t)
	if d.Status().ForwardMode != ForwardOff {
		t.Fatalf("дефолтный режим должен быть off, got %q", d.Status().ForwardMode)
	}
	if _, err := d.dial(context.Background(), "tcp", "example.com:443"); err == nil {
		t.Fatal("dial в режиме off обязан вернуть ошибку (fail-closed)")
	}
}

// TestDialTunnelWithoutTunnelFailClosed — режим tunnel без поднятого туннеля:
// ошибка «Tunnel not initialized», прямого выхода нет.
func TestDialTunnelWithoutTunnelFailClosed(t *testing.T) {
	d := newRunningDaemon(t)
	if _, err := d.Forward(ForwardTunnel); err != nil {
		t.Fatalf("forward tunnel: %v", err)
	}
	_, err := d.dial(context.Background(), "tcp", "example.com:443")
	if err == nil {
		t.Fatal("tunnel без туннеля обязан падать (fail-closed)")
	}
	if !strings.Contains(err.Error(), "Tunnel not initialized") {
		t.Fatalf("ожидалась ошибка «Tunnel not initialized», got %v", err)
	}
}

// TestDialDirectUsesNetDial — режим direct идёт обычным net.Dial до цели.
func TestDialDirectUsesNetDial(t *testing.T) {
	echo := startEcho(t)
	d := newRunningDaemon(t)
	if _, err := d.Forward(ForwardDirect); err != nil {
		t.Fatalf("forward direct: %v", err)
	}

	conn, err := d.dial(context.Background(), "tcp", echo.Addr().String())
	if err != nil {
		t.Fatalf("direct dial должен дойти до цели: %v", err)
	}
	defer conn.Close()

	payload := "direct-ping"
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != payload {
		t.Fatalf("эхо через direct не совпало: %q", buf)
	}
}

// TestForwardTransitions — forward переключает режим и отражает его в Status;
// неизвестный режим отклоняется; disconnect уводит в off.
func TestForwardTransitions(t *testing.T) {
	d := newRunningDaemon(t)

	for _, m := range []ForwardMode{ForwardDirect, ForwardTunnel, ForwardOff} {
		st, err := d.Forward(m)
		if err != nil {
			t.Fatalf("forward %s: %v", m, err)
		}
		if st.ForwardMode != m {
			t.Fatalf("после forward %s Status.ForwardMode=%q", m, st.ForwardMode)
		}
	}

	// Неизвестный режим — ошибка, режим не меняется.
	if _, err := d.Forward("bogus"); err == nil {
		t.Fatal("forward с неизвестным режимом обязан вернуть ошибку")
	}
	if d.Status().ForwardMode != ForwardOff {
		t.Fatalf("после отклонённого forward режим должен остаться off, got %q", d.Status().ForwardMode)
	}

	// Disconnect → off даже если был direct.
	if _, err := d.Forward(ForwardDirect); err != nil {
		t.Fatal(err)
	}
	if d.Disconnect().ForwardMode != ForwardOff {
		t.Fatal("disconnect обязан перевести режим в off")
	}
}

// TestProxyEndToEndOffThenDirect — через реальный постоянный listener: в off режиме
// CONNECT отклоняется (502, fail-closed), а в direct — туннелируется до цели (200).
// Доказывает: listener один и тот же, меняется только dial-стратегия.
func TestProxyEndToEndOffThenDirect(t *testing.T) {
	echo := startEcho(t)
	d := newRunningDaemon(t)
	addr := d.Status().LocalProxy

	// off → 502, но соединение с listener устанавливается (порт жив).
	status := doConnect(t, addr, "example.com:443")
	if strings.Contains(status, "200") {
		t.Fatalf("fail-closed нарушен: 200 в режиме off (%q)", status)
	}
	if !strings.Contains(status, "502") {
		t.Fatalf("в off ожидался 502, got %q", status)
	}

	// direct → 200 и реальное туннелирование байтов до echo.
	if _, err := d.Forward(ForwardDirect); err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", echo.Addr().String(), echo.Addr().String())
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "200") {
		t.Fatalf("в direct ожидался 200, got %q", line)
	}
	for {
		h, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimRight(h, "\r\n") == "" {
			break
		}
	}
	payload := "through-direct"
	io.WriteString(c, payload)
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != payload {
		t.Fatalf("эхо через direct-прокси не совпало: %q", buf)
	}
}

// doConnect шлёт один CONNECT на прокси и возвращает строку статуса ответа.
func doConnect(t *testing.T, proxyAddr, target string) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer c.Close()
	fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	return line
}
