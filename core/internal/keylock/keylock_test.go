package keylock

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const (
	keyA = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="
	keyB = "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB="
)

// TestHelperLockHolder — не тест, а дочерний процесс-держатель лока для тестов
// межпроцессной блокировки (паттерн re-exec тестового бинаря).
func TestHelperLockHolder(t *testing.T) {
	if os.Getenv("KEYLOCK_HELPER") != "1" {
		t.Skip("вспомогательный процесс")
	}
	l, err := Acquire(os.Getenv("KEYLOCK_DIR"), os.Getenv("KEYLOCK_KEY"), "/tmp/helper.sock")
	if err != nil {
		fmt.Println("error:", err)
		os.Exit(3)
	}
	fmt.Println("locked")
	_ = l
	time.Sleep(time.Minute) // родитель убьёт раньше
	os.Exit(0)
}

// startHolder запускает дочерний процесс, взявший лок на key в dir, и ждёт «locked».
func startHolder(t *testing.T, dir, key string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperLockHolder$")
	cmd.Env = append(os.Environ(), "KEYLOCK_HELPER=1", "KEYLOCK_DIR="+dir, "KEYLOCK_KEY="+key)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("держатель не взял лок: %q, %v", line, err)
	}
	return cmd
}

func TestAcquireCreatesMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "locks")
	l, err := Acquire(dir, keyA, "/tmp/a.sock")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer l.Release()
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		t.Fatalf("каталог локов не создан: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("права каталога %o, ожидалось 700", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(l.Path())
	want := fmt.Sprintf("pid=%d\nsock=/tmp/a.sock\n", os.Getpid())
	if string(b) != want {
		t.Fatalf("содержимое лока %q, ожидалось %q", b, want)
	}
}

func TestPathForStableAndHidesKey(t *testing.T) {
	p1, p2 := PathFor("/d", keyA), PathFor("/d", keyA)
	if p1 != p2 {
		t.Fatalf("путь нестабилен: %s vs %s", p1, p2)
	}
	if PathFor("/d", keyB) == p1 {
		t.Fatal("разные ключи дали один путь")
	}
	base := filepath.Base(p1)
	if !strings.HasPrefix(base, "tunnel-") || !strings.HasSuffix(base, ".lock") || len(base) != len("tunnel-")+16+len(".lock") {
		t.Fatalf("неожиданное имя файла %q", base)
	}
	if strings.Contains(p1, "AAAA") {
		t.Fatal("ключ попал в имя файла")
	}
}

// Второй экземпляр в том же процессе (отдельный open → отдельный flock) — занято.
func TestSecondAcquireSameKeyBusyInProcess(t *testing.T) {
	dir := t.TempDir()
	l1, err := Acquire(dir, keyA, "/tmp/first.sock")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Acquire(dir, keyA, "/tmp/second.sock")
	if !IsBusy(err) {
		t.Fatalf("ожидалась BusyError, got %v", err)
	}
	msg := err.Error()
	for _, want := range []string{"уже используется", fmt.Sprintf("pid %d", os.Getpid()), "сокет /tmp/first.sock", "ломают туннель"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("в тексте ошибки нет %q: %s", want, msg)
		}
	}
	// После Release — свободно, повторный Release безопасен.
	l1.Release()
	l1.Release()
	l2, err := Acquire(dir, keyA, "/tmp/second.sock")
	if err != nil {
		t.Fatalf("после Release лок должен быть свободен: %v", err)
	}
	l2.Release()
}

func TestDifferentKeysIndependent(t *testing.T) {
	dir := t.TempDir()
	la, err := Acquire(dir, keyA, "")
	if err != nil {
		t.Fatal(err)
	}
	defer la.Release()
	lb, err := Acquire(dir, keyB, "")
	if err != nil {
		t.Fatalf("другой ключ не должен блокироваться: %v", err)
	}
	lb.Release()
}

func TestEmptyKeyRejected(t *testing.T) {
	if _, err := Acquire(t.TempDir(), "", ""); err == nil || IsBusy(err) {
		t.Fatalf("пустой ключ: ожидалась не-Busy ошибка, got %v", err)
	}
}

func TestUnwritableDirIsNotBusy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("под root права каталога не ограничивают")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o700)
	_, err := Acquire(filepath.Join(parent, "locks"), keyA, "")
	if err == nil || IsBusy(err) {
		t.Fatalf("ожидалась ошибка ФС (не Busy), got %v", err)
	}
}

func TestReleaseNilSafe(t *testing.T) {
	var l *Lock
	l.Release()
}

// Другой процесс держит лок → занято с его pid; после kill -9 лок свободен без
// ручной уборки (ОС снимает flock со смертью процесса).
func TestCrossProcessBusyAndFreedOnKill9(t *testing.T) {
	dir := t.TempDir()
	cmd := startHolder(t, dir, keyA)

	_, err := Acquire(dir, keyA, "/tmp/me.sock")
	if !IsBusy(err) {
		t.Fatalf("ожидалась BusyError от чужого процесса, got %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("pid %d", cmd.Process.Pid)) {
		t.Fatalf("в ошибке нет pid держателя %d: %v", cmd.Process.Pid, err)
	}
	// Чужой ключ процесс-держатель не трогает.
	lb, err := Acquire(dir, keyB, "")
	if err != nil {
		t.Fatalf("другой ключ занят чужим процессом: %v", err)
	}
	lb.Release()

	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	l, err := Acquire(dir, keyA, "/tmp/me.sock")
	if err != nil {
		t.Fatalf("после kill -9 держателя лок должен освободиться: %v", err)
	}
	l.Release()
}

// Дескриптор лока не наследуется дочерними процессами (O_CLOEXEC): иначе запущенный
// ядром подпроцесс продлевал бы лок после смерти ядра.
func TestLockNotInheritedByChildren(t *testing.T) {
	dir := t.TempDir()
	l, err := Acquire(dir, keyA, "")
	if err != nil {
		t.Fatal(err)
	}
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	// Имитация смерти владельца: просто закрыть дескриптор без LOCK_UN. Если бы он
	// унаследовался sleep'ом, flock остался бы взят.
	_ = l.f.Close()
	l.f = nil
	l2, err := Acquire(dir, keyA, "")
	if err != nil {
		t.Fatalf("лок удерживается дочерним процессом: %v", err)
	}
	l2.Release()
}
