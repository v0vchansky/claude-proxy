package control

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

	"github.com/v0vchansky/claude-proxy/core/internal/keylock"
	"github.com/v0vchansky/claude-proxy/core/internal/logbuf"
	"github.com/v0vchansky/claude-proxy/core/internal/profile"
	"github.com/v0vchansky/claude-proxy/core/internal/wgkey"
)

// Тесты лока ключа на уровне Daemon. Сеть не трогается: лок берётся до любых
// сетевых действий, поэтому ветка «занято» завершается до netcfg/tunnel.Open.

func genKey(t *testing.T) (priv, pub string) {
	t.Helper()
	priv, pub, err := wgkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	return priv, pub
}

func newLockedDaemon(t *testing.T, dir, sock string) *Daemon {
	t.Helper()
	d := NewDaemon("127.0.0.1:0", "1.1.1.1:443", logbuf.New(50), false)
	d.SetKeyLock(dir, sock)
	t.Cleanup(d.Shutdown)
	return d
}

// testProfile — валидный по формату профиль на заведомо недоступный адрес. До
// него дело не доходит: тесты проверяют, что connect обрывается на локе.
func testProfile(t *testing.T) profile.Profile {
	_, srvPub := genKey(t)
	return profile.Profile{
		ID: "t", DisplayName: "test", Host: "127.0.0.1", Port: 9,
		ServerPublicKey: srvPub, ClientVpnAddress: "10.77.0.2/32",
	}
}

func (d *Daemon) holdsLock() bool {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	return d.keyLock != nil
}

func (d *Daemon) lockNow(priv string) error {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	return d.acquireKeyLock(priv)
}

// Второй экземпляр тем же ключом: connect падает с понятной ошибкой, туннель не
// поднят, состояние error, лок первого не тронут. Быстро — без handshake-таймаута.
func TestConnectSameKeyBusyNoTunnel(t *testing.T) {
	dir := t.TempDir()
	priv, _ := genKey(t)
	d1 := newLockedDaemon(t, dir, "/tmp/first.sock")
	d2 := newLockedDaemon(t, dir, "/tmp/second.sock")

	if err := d1.lockNow(priv); err != nil {
		t.Fatalf("первый лок: %v", err)
	}

	start := time.Now()
	st, err := d2.Connect(testProfile(t), priv)
	if err == nil {
		t.Fatal("connect тем же ключом должен падать")
	}
	if !keylock.IsBusy(err) {
		t.Fatalf("ожидалась ошибка занятости ключа, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("connect шёл до сети (%s) — лок должен проверяться первым", time.Since(start))
	}
	for _, want := range []string{"уже используется другим процессом claude-proxy-core", fmt.Sprintf("pid %d", os.Getpid()), "сокет /tmp/first.sock"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("в ошибке нет %q: %v", want, err)
		}
	}
	if st.State != StateError || st.LastError != err.Error() {
		t.Fatalf("state=%q lastError=%q, ожидалось error с текстом лока", st.State, st.LastError)
	}
	if st.ForwardMode != ForwardOff {
		t.Fatalf("forwardMode=%q, ожидалось off", st.ForwardMode)
	}
	if d2.isConnected() || d2.holdsLock() {
		t.Fatal("второй экземпляр поднял туннель или держит лок")
	}
	if !d1.holdsLock() {
		t.Fatal("неудачный connect второго отпустил лок первого")
	}

	// switch тем же ключом из второго экземпляра — тоже отказ.
	if _, err := d2.Switch(testProfile(t), priv); !keylock.IsBusy(err) {
		t.Fatalf("switch тем же ключом: ожидалась занятость, got %v", err)
	}

	// После disconnect первого ключ свободен для второго.
	d1.Disconnect()
	if d1.holdsLock() {
		t.Fatal("disconnect не отпустил лок")
	}
	if err := d2.lockNow(priv); err != nil {
		t.Fatalf("после disconnect первого второй должен взять лок: %v", err)
	}
}

// Повторный лок того же ключа в том же экземпляре (switch/reconnect) не
// самоблокируется; смена ключа отпускает старый.
func TestSameDaemonSameKeyNoSelfLock(t *testing.T) {
	dir := t.TempDir()
	privA, _ := genKey(t)
	privB, _ := genKey(t)
	d := newLockedDaemon(t, dir, "/tmp/x.sock")
	other := newLockedDaemon(t, dir, "/tmp/y.sock")

	if err := d.lockNow(privA); err != nil {
		t.Fatal(err)
	}
	if err := d.lockNow(privA); err != nil {
		t.Fatalf("повторный лок того же ключа самоблокировался: %v", err)
	}
	if err := d.lockNow(privB); err != nil {
		t.Fatalf("смена ключа: %v", err)
	}
	// Старый ключ A отпущен — его может взять другой экземпляр.
	if err := other.lockNow(privA); err != nil {
		t.Fatalf("старый ключ не отпущен при смене: %v", err)
	}
	// А B — занят нами.
	if err := other.lockNow(privB); !keylock.IsBusy(err) {
		t.Fatalf("ключ B должен быть занят, got %v", err)
	}
}

func TestDifferentKeysDoNotConflict(t *testing.T) {
	dir := t.TempDir()
	privA, _ := genKey(t)
	privB, _ := genKey(t)
	d1 := newLockedDaemon(t, dir, "")
	d2 := newLockedDaemon(t, dir, "")
	if err := d1.lockNow(privA); err != nil {
		t.Fatal(err)
	}
	if err := d2.lockNow(privB); err != nil {
		t.Fatalf("разные ключи мешают друг другу: %v", err)
	}
}

func TestInvalidPrivateKeyRejectedBeforeNetwork(t *testing.T) {
	d := newLockedDaemon(t, t.TempDir(), "")
	_, err := d.Connect(testProfile(t), "не-base64")
	if err == nil || keylock.IsBusy(err) || !strings.Contains(err.Error(), "невалидный приватный ключ") {
		t.Fatalf("ожидалась ошибка невалидного ключа, got %v", err)
	}
	if d.holdsLock() || d.isConnected() {
		t.Fatal("после ошибки ключа не должно быть ни лока, ни туннеля")
	}
}

// Каталога локов нет → создаётся при первом connect.
func TestLockDirCreated(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ClaudeProxy", "locks")
	priv, pub := genKey(t)
	d := newLockedDaemon(t, dir, "/tmp/s.sock")
	if err := d.lockNow(priv); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(keylock.PathFor(dir, pub)); err != nil {
		t.Fatalf("lock-файл не создан: %v", err)
	}
}

// Сломанный каталог локов (ошибка ФС, не занятость) не отнимает подключение.
func TestLockFSErrorFailsOpen(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("под root права не ограничивают")
	}
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(parent, 0o700)
	priv, _ := genKey(t)
	d := newLockedDaemon(t, filepath.Join(parent, "locks"), "")
	if err := d.lockNow(priv); err != nil {
		t.Fatalf("ошибка ФС лока не должна блокировать connect: %v", err)
	}
	if d.holdsLock() {
		t.Fatal("лок не мог быть взят")
	}
}

// Без SetKeyLock лок выключен (поведение по умолчанию для встраивания/тестов).
func TestNoLockDirDisabled(t *testing.T) {
	d := NewDaemon("127.0.0.1:0", "", logbuf.New(10), false)
	if err := d.lockNow("мусор"); err != nil {
		t.Fatalf("без lockDir лок выключен, got %v", err)
	}
}

// TestHelperKeyHolder — дочерний процесс-держатель лока (re-exec тестового бинаря).
func TestHelperKeyHolder(t *testing.T) {
	if os.Getenv("CTL_KEYLOCK_HELPER") != "1" {
		t.Skip("вспомогательный процесс")
	}
	d := NewDaemon("127.0.0.1:0", "", logbuf.New(10), false)
	d.SetKeyLock(os.Getenv("CTL_KEYLOCK_DIR"), "/tmp/helper.sock")
	if err := d.lockNow(os.Getenv("CTL_KEYLOCK_PRIV")); err != nil {
		fmt.Println("error:", err)
		os.Exit(3)
	}
	fmt.Println("locked")
	time.Sleep(time.Minute)
	os.Exit(0)
}

// Ключ держит другой процесс: connect отказывает с его pid; после kill -9 владельца
// ключ свободен без ручной уборки.
func TestCrossProcessConnectBusyThenFreeAfterKill9(t *testing.T) {
	dir := t.TempDir()
	priv, _ := genKey(t)

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperKeyHolder$")
	cmd.Env = append(os.Environ(), "CTL_KEYLOCK_HELPER=1", "CTL_KEYLOCK_DIR="+dir, "CTL_KEYLOCK_PRIV="+priv)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || strings.TrimSpace(line) != "locked" {
		t.Fatalf("держатель не взял лок: %q %v", line, err)
	}

	d := newLockedDaemon(t, dir, "/tmp/me.sock")
	_, err = d.Connect(testProfile(t), priv)
	if !keylock.IsBusy(err) {
		t.Fatalf("ожидалась занятость ключа чужим процессом, got %v", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("pid %d", cmd.Process.Pid)) || !strings.Contains(err.Error(), "сокет /tmp/helper.sock") {
		t.Fatalf("в ошибке нет pid/сокета держателя: %v", err)
	}
	if d.isConnected() {
		t.Fatal("туннель поднят при занятом ключе")
	}

	_ = cmd.Process.Signal(syscall.SIGKILL)
	_ = cmd.Wait()
	if err := d.lockNow(priv); err != nil {
		t.Fatalf("после kill -9 владельца ключ должен быть свободен: %v", err)
	}
}
