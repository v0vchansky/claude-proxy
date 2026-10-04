package proxy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// startEcho поднимает локальный TCP-эхо-сервер, в который будет «дозваниваться» dialer.
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
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(c)
		}
	}()
	return ln
}

// startHTTP поднимает локальный HTTP-сервер, отвечающий "ok".
func startHTTP(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln
}

func dialProxyRaw(t *testing.T, proxyAddr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestConnectTunnelsThroughDialer(t *testing.T) {
	echo := startEcho(t)
	defer echo.Close()

	var calls atomic.Int32
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		return net.Dial("tcp", echo.Addr().String()) // имитируем «цель за туннелем»
	}

	p, addr, err := Start("127.0.0.1:0", dial, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	c := dialProxyRaw(t, addr)
	defer c.Close()

	fmt.Fprintf(c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "200") {
		t.Fatalf("ожидался 200, got %q", status)
	}
	if calls.Load() == 0 {
		t.Fatal("dialer не был вызван — прокси пошёл мимо туннеля")
	}

	// Дочитываем заголовки до пустой строки, затем проверяем туннелирование байтов.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimRight(line, "\r\n") == "" {
			break
		}
	}
	payload := "ping-through-tunnel"
	io.WriteString(c, payload)
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != payload {
		t.Fatalf("эхо не совпало: %q", buf)
	}
}

func TestConnectFailClosedWhenDialerFails(t *testing.T) {
	var calls atomic.Int32
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("Tunnel not initialized")
	}

	p, addr, err := Start("127.0.0.1:0", dial, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	c := dialProxyRaw(t, addr)
	defer c.Close()

	fmt.Fprintf(c, "CONNECT example.com:443 HTTP/1.1\r\nHost: example.com:443\r\n\r\n")
	br := bufio.NewReader(c)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	// Запрос обязан завершиться ошибкой (502), НЕ 200. Никакого прямого выхода.
	if strings.Contains(status, "200") {
		t.Fatalf("fail-closed нарушен: получили 200 при упавшем туннеле (%q)", status)
	}
	if !strings.Contains(status, "502") {
		t.Fatalf("ожидался 502, got %q", status)
	}
	if calls.Load() == 0 {
		t.Fatal("dialer не был вызван — прокси попытался выйти мимо туннеля")
	}
}

func TestPlainHTTPUsesDialer(t *testing.T) {
	target := startHTTP(t)

	var calls atomic.Int32
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		calls.Add(1)
		return net.Dial("tcp", target.Addr().String())
	}

	p, addr, err := Start("127.0.0.1:0", dial, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	proxyURL, _ := url.Parse("http://" + addr)
	tr := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	client := &http.Client{Transport: tr, Timeout: 3 * time.Second}

	resp, err := client.Get("http://target.invalid/hi")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "ok" {
		t.Fatalf("тело=%q", body)
	}
	if calls.Load() == 0 {
		t.Fatal("dialer не был вызван для plain HTTP")
	}
}

func TestBindsLoopbackOnly(t *testing.T) {
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, errors.New("unused")
	}
	p, addr, err := Start("127.0.0.1:0", dial, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	host, _, _ := net.SplitHostPort(addr)
	if host != "127.0.0.1" {
		t.Fatalf("прокси слушает не на loopback: %s", host)
	}
}

func TestStopFreesPort(t *testing.T) {
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, errors.New("unused")
	}
	p, addr, err := Start("127.0.0.1:0", dial, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.Stop()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("порт не освобождён после Stop: %v", err)
	}
	ln.Close()
}
