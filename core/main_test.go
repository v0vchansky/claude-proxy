package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func battleLog(home string) string {
	return filepath.Join(home, "Library", "Application Support", "ClaudeProxy", "diagnostics.log")
}

func TestResolveLogPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cases := []struct {
		name, mode, val string
		set             bool
		want            string
	}{
		{"proxy без -log — только память", "proxy", "", false, ""},
		{"proxy с -log", "proxy", "/x/core.log", true, "/x/core.log"},
		{"proxy с явным пустым -log", "proxy", "", true, ""},
		{"vpnd без -log — прежний путь", "vpnd", "", false, battleLog(home)},
		{"vpnd с -log", "vpnd", "/y/vpnd.log", true, "/y/vpnd.log"},
		{"vpnd с явным пустым -log — память", "vpnd", "", true, ""},
	}
	for _, c := range cases {
		if got := resolveLogPath(c.mode, c.val, c.set); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestNewLogEmptyIsMemoryOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	l := newLog("")
	l.Logf("строка")
	_ = l.Close()
	if _, err := os.Stat(battleLog(home)); !os.IsNotExist(err) {
		t.Fatalf("без -log журнал не должен появляться в боевом пути: %v", err)
	}
	if j := strings.Join(l.Journal(), "\n"); !strings.Contains(j, "строка") {
		t.Fatalf("память-онли журнал пуст: %q", j)
	}
}

func TestNewLogWithPathWritesFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "core.log")
	l := newLog(p)
	l.Logf("в файл")
	_ = l.Close()
	b, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(b), "в файл") {
		t.Fatalf("журнал не записан в %s: %v %q", p, err, b)
	}
}

func TestNewLogBadPathFallsBackToMemory(t *testing.T) {
	// Путь, где «каталог» — обычный файл: открыть журнал нельзя.
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l := newLog(filepath.Join(f, "core.log"))
	defer l.Close()
	if j := strings.Join(l.Journal(), "\n"); !strings.Contains(j, "память-онли") {
		t.Fatalf("ожидался откат на память с причиной в логе: %q", j)
	}
}

// runCore собирает ядро и запускает proxy-режим на временных путях (HOME подменён:
// ни боевой журнал, ни боевые локи не задеваются), ждёт строку готовности и гасит.
func runCore(t *testing.T, home string, extra ...string) {
	t.Helper()
	if testing.Short() {
		t.Skip("сборка бинаря ядра")
	}
	bin := filepath.Join(t.TempDir(), "claude-proxy-core")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	// Короткий путь сокета: TempDir на macOS длинный, а sun_path ограничен 104 байтами.
	sdir, err := os.MkdirTemp("/tmp", "cpc-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sdir) })
	sock := filepath.Join(sdir, "t.sock")
	args := append([]string{"-sock", sock, "-proxy", "127.0.0.1:0"}, extra...)
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "HOME="+home)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(out).ReadString('\n')
		done <- line
	}()
	select {
	case line := <-done:
		if !strings.Contains(line, "готов") {
			_ = cmd.Wait()
			t.Fatalf("ядро не стартовало: %q, stderr: %s", line, stderr.String())
		}
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("ядро не стартовало за 20 с")
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
}

func TestBinaryWithoutLogFlagDoesNotTouchBattleLog(t *testing.T) {
	home := t.TempDir()
	runCore(t, home)
	if _, err := os.Stat(battleLog(home)); !os.IsNotExist(err) {
		t.Fatalf("ядро без -log записало боевой журнал: %v", err)
	}
}

func TestBinaryWithLogFlagWritesThere(t *testing.T) {
	home := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "test-core.log")
	runCore(t, home, "-log", logPath)
	b, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(b), "App started (core") {
		t.Fatalf("журнал по -log не записан: %v %q", err, b)
	}
	if _, err := os.Stat(battleLog(home)); !os.IsNotExist(err) {
		t.Fatalf("с -log в боевой путь писать нельзя: %v", err)
	}
}
