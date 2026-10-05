package vpnd

import (
	"context"
	"errors"
	"net/netip"

	"github.com/v0vchansky/claude-proxy/core/internal/profile"
	"path/filepath"
	"strings"
	"testing"

	"github.com/v0vchansky/claude-proxy/core/internal/keylock"
	"github.com/v0vchansky/claude-proxy/core/internal/netcfg"
	"github.com/v0vchansky/claude-proxy/core/internal/wgkey"
)

// Лок ключа Полного VPN: второй vpnd-экземпляр тем же ключом не поднимает туннель
// и не трогает систему; после disconnect первого — может.

func lockedManager(t *testing.T, dir, sock string, tun *fakeTunnel) *Manager {
	t.Helper()
	m := newTestManager(t, tun)
	m.SetKeyLock(dir, sock)
	return m
}

func TestVpndSecondInstanceSameKeyBusy(t *testing.T) {
	dir := t.TempDir()
	priv, _, err := wgkey.Generate()
	if err != nil {
		t.Fatal(err)
	}
	m1 := lockedManager(t, dir, "/var/run/first.sock", nil)
	opened := false
	m2 := lockedManager(t, dir, "/var/run/second.sock", nil)
	inner := m2.open
	m2.open = func(p0 profile.Profile, k string, d []netip.Addr, l func(string)) (tunnelHandle, error) {
		opened = true
		return inner(p0, k, d, l)
	}

	if _, err := m1.Connect(testProfile(), priv); err != nil {
		t.Fatalf("первый connect: %v", err)
	}
	_, err = m2.Connect(testProfile(), priv)
	if !keylock.IsBusy(err) || !strings.Contains(err.Error(), "сокет /var/run/first.sock") {
		t.Fatalf("ожидалась занятость ключа первым демоном, got %v", err)
	}
	if opened || len(m2.run.(*fakeRunner).calls) != 0 {
		t.Fatal("второй экземпляр открыл туннель или трогал систему при занятом ключе")
	}
	if m2.keyLock != nil {
		t.Fatal("второй экземпляр держит лок")
	}

	if _, err := m1.Disconnect(); err != nil {
		t.Fatal(err)
	}
	if m1.keyLock != nil {
		t.Fatal("disconnect-full не отпустил лок")
	}
	if _, err := m2.Connect(testProfile(), priv); err != nil {
		t.Fatalf("после disconnect первого второй должен подключиться: %v", err)
	}
	if m2.keyLock == nil {
		t.Fatal("успешный connect должен держать лок")
	}
}

// Неуспешный connect (сбой системного шага → rollback) отпускает лок.
func TestVpndFailedConnectReleasesLock(t *testing.T) {
	dir := t.TempDir()
	priv, _, _ := wgkey.Generate()
	m := lockedManager(t, dir, "", nil)
	fr := newFakeRunner()
	fr.failAt = 3
	m.run = fr
	if _, err := m.Connect(testProfile(), priv); err == nil {
		t.Fatal("ожидалась ошибка connect")
	}
	if m.keyLock != nil {
		t.Fatal("лок остался после неуспешного connect")
	}
	other := lockedManager(t, dir, "", nil)
	if _, err := other.Connect(testProfile(), priv); err != nil {
		t.Fatalf("ключ должен быть свободен после отката: %v", err)
	}
}

// Неуспешный connect на preflight (до rollback-веток) тоже отпускает лок.
func TestVpndPreflightFailureReleasesLock(t *testing.T) {
	dir := t.TempDir()
	priv, _, _ := wgkey.Generate()
	m := lockedManager(t, dir, "", nil)
	m.capture = func(context.Context) (*netcfg.Snapshot, error) {
		return nil, errors.New("снимок сети недоступен")
	}
	if _, err := m.Connect(testProfile(), priv); err == nil {
		t.Fatal("ожидалась ошибка preflight")
	}
	if m.keyLock != nil {
		t.Fatal("лок остался после ошибки preflight")
	}
}

func TestVpndInvalidKeyRejected(t *testing.T) {
	m := lockedManager(t, filepath.Join(t.TempDir(), "locks"), "", nil)
	if _, err := m.Connect(testProfile(), "priv"); err == nil || !strings.Contains(err.Error(), "невалидный приватный ключ") {
		t.Fatalf("ожидалась ошибка ключа, got %v", err)
	}
	if len(m.run.(*fakeRunner).calls) != 0 {
		t.Fatal("при невалидном ключе система тронута")
	}
}

func TestVpndDifferentKeysIndependent(t *testing.T) {
	dir := t.TempDir()
	privA, _, _ := wgkey.Generate()
	privB, _, _ := wgkey.Generate()
	m1 := lockedManager(t, dir, "", nil)
	m2 := lockedManager(t, dir, "", nil)
	if _, err := m1.Connect(testProfile(), privA); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.Connect(testProfile(), privB); err != nil {
		t.Fatalf("разные ключи мешают: %v", err)
	}
}
