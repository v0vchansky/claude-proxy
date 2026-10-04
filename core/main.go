// Command claude-proxy-core — ядро Claude Proxy: userspace AmneziaWG + netstack +
// локальный HTTP CONNECT proxy, управляемое по unix-сокету из menu bar приложения.
//
// Конфигурацию и ключи ядро не хранит на диске: приложение передаёт активный
// профиль и приватный ключ клиента в команде connect/switch (см. docs/control-protocol.md).
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/v0vchansky/claude-proxy/core/internal/control"
	"github.com/v0vchansky/claude-proxy/core/internal/logbuf"
	"github.com/v0vchansky/claude-proxy/core/internal/wgkey"
)

func defaultSockPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/claude-proxy.sock"
	}
	return filepath.Join(home, "Library", "Application Support", "ClaudeProxy", "control.sock")
}

func main() {
	var (
		sockPath  = flag.String("sock", defaultSockPath(), "путь к control unix-сокету")
		proxyAddr = flag.String("proxy", "127.0.0.1:8118", "адрес local HTTP proxy")
		health    = flag.String("health", "1.1.1.1:443", "цель health check (host:port) через туннель")
		verbose   = flag.Bool("verbose", false, "verbose-логи устройства AmneziaWG")
		genkey    = flag.Bool("genkey", false, "сгенерировать клиентскую пару ключей и выйти")
	)
	flag.Parse()

	if *genkey {
		priv, pub, err := wgkey.Generate()
		if err != nil {
			fmt.Fprintf(os.Stderr, "genkey: %v\n", err)
			os.Exit(1)
		}
		// Приватный ключ печатается только по явному запросу genkey; в лог не идёт.
		fmt.Printf("private=%s\npublic=%s\n", priv, pub)
		return
	}

	log := logbuf.New(500)
	log.Logf("App started (core)")

	daemon := control.NewDaemon(*proxyAddr, *health, log, *verbose)

	srv, err := control.NewServer(daemon, *sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "не удалось открыть control-сокет %s: %v\n", *sockPath, err)
		os.Exit(1)
	}
	log.Logf("Control socket: %s", *sockPath)
	fmt.Printf("claude-proxy-core готов: control=%s proxy=%s\n", *sockPath, *proxyAddr)

	// Корректное завершение по сигналу.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Logf("Shutting down")
		daemon.Shutdown()
		_ = srv.Close()
		os.Exit(0)
	}()

	if err := srv.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "control server остановлен: %v\n", err)
		daemon.Shutdown()
		os.Exit(1)
	}
}
