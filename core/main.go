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
	"time"

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

// logRetention — сколько хранить персистентный журнал диагностики. 72h = 3 дня.
const logRetention = 72 * time.Hour

func appSupportDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "Application Support", "ClaudeProxy"), nil
}

func defaultSockPath() string {
	dir, err := appSupportDir()
	if err != nil {
		return "/tmp/claude-proxy.sock"
	}
	return filepath.Join(dir, "control.sock")
}

// legacyLogPath — исторический путь журнала <Application Support>/ClaudeProxy/
// diagnostics.log домашнего каталога текущего пользователя. Для proxy-режима он
// больше не подставляется сам: приложение передаёт его явно через -log, а ручной
// (тестовый) запуск без -log пишет только в память и не мешает боевой журнал.
// Для vpnd остаётся умолчанием: уже установленный LaunchDaemon-plist -log не
// передаёт (под root это /var/root/..., с боевым журналом пользователя не пересекается).
func legacyLogPath() (string, error) {
	dir, err := appSupportDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "diagnostics.log"), nil
}

// resolveLogPath выбирает путь журнала: явный -log (в том числе явная пустая
// строка = только память) побеждает; иначе proxy → только память, vpnd → legacy.
func resolveLogPath(mode, flagValue string, flagSet bool) string {
	if flagSet {
		return flagValue
	}
	if mode == "vpnd" {
		if p, err := legacyLogPath(); err == nil {
			return p
		}
	}
	return ""
}

// newLog создаёт журнал диагностики. path пуст — только память (кольцевой буфер);
// иначе персистентный файл с ретеншеном logRetention. При ошибке файла деградирует
// на память-онли и фиксирует причину в самом логе — процесс не падает.
func newLog(path string) *logbuf.Buffer {
	if path == "" {
		l := logbuf.New(500)
		l.Logf("Diagnostics log: память-онли (-log не задан)")
		return l
	}
	l, err := logbuf.NewWithFile(500, path, logRetention)
	if err != nil {
		l = logbuf.New(500)
		l.Logf("Diagnostics log: память-онли (файл %s недоступен: %v)", path, err)
		return l
	}
	return l
}

// proxyLockDir — каталог локов клиентских ключей proxy-режима. Общий для всех
// процессов пользователя независимо от -sock: в этом и смысл — тестовое ядро на
// своём сокете не должно подключиться боевым ключом параллельно с приложением.
func proxyLockDir() string {
	dir, err := appSupportDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "claude-proxy-locks")
	}
	return filepath.Join(dir, "locks")
}

func main() {
	var (
		mode      = flag.String("mode", "proxy", "режим ядра: proxy (userspace proxy, по умолчанию) или vpnd (root VPN-демон)")
		sockPath  = flag.String("sock", defaultSockPath(), "путь к control unix-сокету")
		proxyAddr = flag.String("proxy", "127.0.0.1:8118", "адрес local HTTP proxy (только proxy-режим)")
		health    = flag.String("health", "1.1.1.1:443", "цель health check (host:port) через туннель (только proxy-режим)")
		verbose   = flag.Bool("verbose", false, "verbose-логи устройства AmneziaWG")
		genkey    = flag.Bool("genkey", false, "сгенерировать клиентскую пару ключей и выйти")
		statePath = flag.String("state", "/var/lib/claude-proxy/vpnd-state.json", "путь к state-файлу фаз full-VPN (только vpnd-режим)")
		strict    = flag.Bool("strict", false, "строгий kill-switch: без Allow-LAN и с сохранением PF при крахе (только vpnd-режим)")
		sockUID   = flag.Int("sock-uid", -1, "uid, которому отдать vpnd-сокет; -1 — не менять владельца")
		logPath   = flag.String("log", "", "путь персистентного журнала диагностики; пусто — только память (proxy); для vpnd по умолчанию прежний путь в Application Support root")
	)
	flag.Parse()

	logSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "log" {
			logSet = true
		}
	})
	logFile := resolveLogPath(*mode, *logPath, logSet)

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
		runProxy(*sockPath, *proxyAddr, *health, *verbose, logFile)
	case "vpnd":
		runVpnd(*sockPath, *statePath, *strict, *sockUID, logFile)
	default:
		fmt.Fprintf(os.Stderr, "неизвестный режим -mode %q (ожидалось proxy или vpnd)\n", *mode)
		os.Exit(2)
	}
}

// runProxy — текущий proxy-режим без изменений: proxy-демон + control-сокет.
func runProxy(sockPath, proxyAddr, healthTarget string, verbose bool, logFile string) {
	log := newLog(logFile)
	log.Logf("App started (core, pid=%d)", os.Getpid())

	daemon := control.NewDaemon(proxyAddr, healthTarget, log, verbose)
	// Один клиентский ключ — один туннель на машине (см. keylock): лок общий для
	// всех процессов пользователя, независимо от -sock.
	daemon.SetKeyLock(proxyLockDir(), sockPath)

	// Listener 8118 поднимается один раз и живёт до завершения процесса (вариант А):
	// порт доступен всегда, а connect/disconnect/forward лишь переключают dial-режим.
	if err := daemon.StartProxy(); err != nil {
		fmt.Fprintf(os.Stderr, "не удалось поднять local proxy %s: %v\n", proxyAddr, err)
		os.Exit(1)
	}

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
		_ = log.Close()
		os.Exit(0)
	}()

	if err := srv.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "control server остановлен: %v\n", err)
		daemon.Shutdown()
		os.Exit(1)
	}
}

// runVpnd — root-демон полного VPN: ipc-транспорт + vpnd-Handler поверх оркестратора
// Manager (utun/маршруты/DNS/PF, §4). На старте выполняется crash-recovery по
// stale state (§9). Реальные системные изменения требуют root.
func runVpnd(sockPath, statePath string, strict bool, sockUID int, logFile string) {
	log := newLog(logFile)
	log.Logf("App started (vpnd, pid=%d)", os.Getpid())

	mgr := vpnd.NewManager(statePath, strict, log)
	// Лок ключа Полного VPN рядом со state (root-only каталог /var/lib/claude-proxy).
	mgr.SetKeyLock(filepath.Join(filepath.Dir(statePath), "locks"), sockPath)
	// Crash-recovery до приёма команд: если прошлый сеанс не был чисто завершён
	// (демон/машина падали), вернуть сеть в исходное состояние (§9).
	mgr.Recover()

	handler := vpnd.NewHandler(coreVersion, log, mgr)
	srv, err := ipc.NewServer(sockPath, handler.Handle)
	if err != nil {
		fmt.Fprintf(os.Stderr, "не удалось открыть vpnd-сокет %s: %v\n", sockPath, err)
		os.Exit(1)
	}
	if sockUID >= 0 {
		if err := os.Chown(sockPath, sockUID, -1); err != nil {
			log.Logf("не удалось сменить владельца сокета на uid=%d: %v", sockUID, err)
		} else {
			log.Logf("vpnd-сокет отдан uid=%d", sockUID)
		}
	}
	log.Logf("VPN control socket: %s (state=%s, strict=%v)", sockPath, statePath, strict)
	fmt.Printf("claude-proxy-core готов: mode=vpnd control=%s version=%s\n", sockPath, coreVersion)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Logf("Shutting down")
		_ = srv.Close()
		_ = log.Close()
		os.Exit(0)
	}()

	if err := srv.Serve(); err != nil {
		fmt.Fprintf(os.Stderr, "vpnd server остановлен: %v\n", err)
		os.Exit(1)
	}
}
