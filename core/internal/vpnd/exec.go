package vpnd

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"time"

	"github.com/v0vchansky/claude-proxy/core/internal/fullvpn"
	"github.com/v0vchansky/claude-proxy/core/internal/netcfg"
	"github.com/v0vchansky/claude-proxy/core/internal/profile"
)

// execTimeout — потолок на одну системную команду teardown/connect. Команды
// (ifconfig/route/networksetup/pfctl) быстрые; таймаут защищает от зависшей утилиты.
const execTimeout = 15 * time.Second

// commandRunner выполняет одну системную команду. Вынесен за интерфейс, чтобы в
// тестах подменить фейком, который только записывает argv — так проверяется ПОРЯДОК
// вызовов connect и зеркальность teardown без root и без реального exec.
type commandRunner interface {
	// Run выполняет argv[0] с аргументами argv[1:], подавая stdin (если непустой),
	// и возвращает stdout, stderr и ошибку запуска/ненулевого кода возврата.
	Run(ctx context.Context, stdin string, argv []string) (stdout, stderr string, err error)
}

// execRunner — боевой commandRunner поверх os/exec (демон работает от root).
type execRunner struct{}

func (execRunner) Run(ctx context.Context, stdin string, argv []string) (string, string, error) {
	if len(argv) == 0 {
		return "", "", fmt.Errorf("vpnd: пустая команда")
	}
	cctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// tunnelHandle — минимальный контракт поднятого full-tunnel, нужный оркестратору.
// Его реализует *fullvpn.Tunnel; в тестах подменяется фейком (без root).
type tunnelHandle interface {
	DeviceName() string
	WaitHandshake(ctx context.Context) error
	Stats() (fullvpn.Stats, error)
	Close() error
}

// tunnelOpener открывает реальный туннель. Боевой вариант — realOpener (fullvpn.Open);
// в тестах подменяется фейком, не требующим root.
type tunnelOpener func(p profile.Profile, privateKeyB64 string, dns []netip.Addr, logf func(string)) (tunnelHandle, error)

// realOpener — боевой tunnelOpener: поднимает kernel utun + WG через fullvpn.
func realOpener(p profile.Profile, privateKeyB64 string, dns []netip.Addr, logf func(string)) (tunnelHandle, error) {
	t, err := fullvpn.Open(p, privateKeyB64, dns, logf)
	if err != nil {
		return nil, err
	}
	return t, nil
}

// resolveServerIP возвращает IPv4-адрес endpoint'а. Для нашего сервера host уже IP
// (быстрый путь ParseAddr); на случай имени — DNS-резолв до первого IPv4.
func resolveServerIP(host string) (string, error) {
	if _, err := netip.ParseAddr(host); err == nil {
		return host, nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("резолв %q: %w", host, err)
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", fmt.Errorf("нет IPv4-адреса для %q", host)
}

// enabledServiceNames — имена включённых сетевых сервисов (для -setdnsservers).
func enabledServiceNames(svcs []netcfg.Service) []string {
	var out []string
	for _, s := range svcs {
		if s.Enabled {
			out = append(out, s.Name)
		}
	}
	return out
}

// tunnelDNS — DNS-серверы, которые ставим на время туннеля (из профиля; дефолт 1.1.1.1/8.8.8.8).
func tunnelDNS(p profile.Profile) []string {
	if len(p.DNS) > 0 {
		return p.DNS
	}
	return []string{"1.1.1.1", "8.8.8.8"}
}
