// Command claude-proxy-core — ядро Claude Proxy. Два режима (см. docs/full-vpn-design.md §3):
//
//   - proxy (по умолчанию): userspace AmneziaWG + netstack + локальный HTTP CONNECT
//     proxy, управляемое по unix-сокету из menu bar приложения. Без root.
//   - vpnd: скелет root-демона полного VPN (пока ping/logs-заглушки, реальная
//     VPN-логика ещё не реализована — задачи 4–8 из §10).
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
	"github.com/v0vchansky/claude-proxy/core/internal/ipc"
	"github.com/v0vchansky/claude-proxy/core/internal/logbuf"
	"github.com/v0vchansky/claude-proxy/core/internal/vpnd"
	"github.com/v0vchansky/claude-proxy/core/internal/wgkey"
)

// coreVersion — версия сборки ядра. Отдаётся vpnd-демоном в ping для
// version-handshake app↔демон (см. docs/full-vpn-design.md §5). Proxy-протокол
// версию не отдаёт — его формат зафиксирован и не меняется.
const coreVersion = "0.1.0"

func defaultSockPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/tmp/claude-proxy.sock"
	}
	return filepath.Join(home, "Library", "Application Support", "ClaudeProxy", "control.sock")
}

func main() {
	var (
		mode      = flag.String("mode", "proxy", "режим ядра: proxy (userspace proxy, по умолчанию) или vpnd (root VPN-демон)")
		sockPath  = flag.String("sock", defaultSockPath(), "путь к control unix-сокету")
		proxyAddr = flag.String("proxy", "127.0.0.1:8118", "адрес local HTTP proxy (только proxy-режим)")
		health    = flag.String("health", "1.1.1.1:443", "цель health check (host:port) через туннель (только proxy-режим)")
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

	switch *mode {
	case "proxy":
		runProxy(*sockPath, *proxyAddr, *health, *verbose)
	case "vpnd":
		runVpnd(*sockPath)
	default:
		fmt.Fprintf(os.Stderr, "неизвестный режим -mode %q (ожидалось proxy или vpnd)\n", *mode)
		os.Exit(2)
	}
}

// runProxy — текущий proxy-режим без изменений: proxy-демон + control-сокет.
func runProxy(sockPath, proxyAddr, healthTarget string, verbose bool) {
	log := logbuf.New(500)
	log.Logf("App started (core)")

	daemon := control.NewDaemon(proxyAddr, healthTarget, log, verbose)

	srv, err := control.NewServer(daemon, sockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "не удалось открыть control-сокет %s: %v\n", sockPath, err)
		os.Exit(1)
	}
	log.Logf("Control socket: %s", sockPath)
	fmt.Printf("claude-proxy-core готов: control=%s proxy=%s\n", sockPath, proxyAddr)

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

// runVpnd — скелет vpnd-режима: только ipc-транспорт + vpnd-Handler, БЕЗ запуска
// proxy/туннеля. Реальная VPN-логика появится в задачах 4–8 (§10).
func runVpnd(sockPath string) {
	log := logbuf.New(500)
	log.Logf("App started (vpnd)")

	handler := vpnd.NewHandler(coreVersion, log)
	srv, err := ipc.NewServer(sockPath, handler.Handle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "не удалось открыть vpnd-сокет %s: %v\n", sockPath, err)
		os.Exit(1)
	}
	log.Logf("VPN control socket: %s", sockPath)
	fmt.Printf("claude-proxy-core готов: mode=vpnd control=%s version=%s\n", sockPath, coreVersion)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Logf("Shutting down")
		_ = srv.Close()
		os.Exit(0)
	}()

	if err := srv.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "vpnd server остановлен: %v\n", err)
		os.Exit(1)
	}
}
