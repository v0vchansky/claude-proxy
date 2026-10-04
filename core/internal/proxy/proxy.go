// Package proxy — локальный HTTP/HTTPS proxy. Поддержан HTTP CONNECT (для HTTPS,
// без MITM) и обычный HTTP forwarding. Весь исходящий трафик идёт через dialer —
// им всегда является туннель, поэтому прямого выхода в системную сеть нет (fail-closed).
package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// DialFunc — диалер наружу. В бою это tunnel.DialContext.
type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

// Proxy — запущенный локальный прокси.
type Proxy struct {
	ln     net.Listener
	dial   DialFunc
	logf   func(format string, args ...any)
	server *http.Server
	wg     sync.WaitGroup
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
}

// Start поднимает прокси на addr (ожидается 127.0.0.1:port). Возвращает Proxy
// и фактический адрес прослушивания.
func Start(addr string, dial DialFunc, logf func(format string, args ...any)) (*Proxy, string, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", fmt.Errorf("Proxy port unavailable: %w", err)
	}
	p := &Proxy{
		ln:    ln,
		dial:  dial,
		logf:  logf,
		conns: make(map[net.Conn]struct{}),
	}
	p.server = &http.Server{
		Handler:     http.HandlerFunc(p.handle),
		ConnContext: func(ctx context.Context, _ net.Conn) context.Context { return ctx },
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		_ = p.server.Serve(ln)
	}()
	return p, ln.Addr().String(), nil
}

// Stop перестаёт принимать соединения, закрывает активные сессии и освобождает порт.
func (p *Proxy) Stop() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	conns := make([]net.Conn, 0, len(p.conns))
	for c := range p.conns {
		conns = append(conns, c)
	}
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.server.Shutdown(ctx)
	// Принудительно рвём апгрейженные (CONNECT) соединения — Shutdown их не трогает.
	for _, c := range conns {
		_ = c.Close()
	}
	p.wg.Wait()
}

func (p *Proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *Proxy) untrack(c net.Conn) {
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

func (p *Proxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

// handleConnect — туннелирование HTTPS: подключаемся к цели через туннель и
// гоняем байты в обе стороны, не расшифровывая TLS.
func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	upstream, err := p.dial(ctx, "tcp", r.Host)
	cancel()
	if err != nil {
		p.logf("Proxy CONNECT %s failed: %v", r.Host, err)
		http.Error(w, "upstream dial failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		p.logf("Proxy CONNECT hijack failed: %v", err)
		return
	}
	defer client.Close()

	if !p.track(client) {
		return
	}
	defer p.untrack(client)

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		return
	}
	p.logf("Proxy CONNECT opened %s", r.Host)
	pipe(client, upstream)
}

// handleHTTP — обычный HTTP через прокси (absolute-form URL). Используется редко
// (Claude Code ходит по HTTPS/CONNECT), но поддержан для полноты и тоже через туннель.
func (p *Proxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() {
		http.Error(w, "proxy requires absolute-form request URI", http.StatusBadRequest)
		return
	}
	host := r.URL.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "80")
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	upstream, err := p.dial(ctx, "tcp", host)
	if err != nil {
		p.logf("Proxy HTTP %s failed: %v", host, err)
		http.Error(w, "upstream dial failed: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer upstream.Close()

	r.RequestURI = ""
	removeHopByHop(r.Header)
	if err := r.Write(upstream); err != nil {
		http.Error(w, "upstream write failed", http.StatusBadGateway)
		return
	}
	resp, err := http.ReadResponse(bufio.NewReader(upstream), r)
	if err != nil {
		http.Error(w, "upstream read failed", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	removeHopByHop(resp.Header)
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		// Полузакрытие, чтобы вторая сторона увидела EOF.
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	<-done
}

var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

func removeHopByHop(h http.Header) {
	for _, k := range hopByHop {
		h.Del(k)
	}
}
