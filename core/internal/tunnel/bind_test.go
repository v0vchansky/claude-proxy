package tunnel

import (
	"fmt"
	"net"
	"testing"

	"github.com/amnezia-vpn/amneziawg-go/conn"
)

// TestBindForInterfaceEmptyIsDefault: пустое имя = прежнее поведение (NewDefaultBind),
// без привязки к интерфейсу. Сравниваем по динамическому типу с эталонным
// NewDefaultBind — оба должны быть один и тот же тип (*conn.StdNetBind).
func TestBindForInterfaceEmptyIsDefault(t *testing.T) {
	b, err := bindForInterface("")
	if err != nil {
		t.Fatalf("bindForInterface(\"\"): %v", err)
	}
	if b == nil {
		t.Fatal("bindForInterface(\"\") вернул nil Bind")
	}
	wantType := typeName(conn.NewDefaultBind())
	if got := typeName(b); got != wantType {
		t.Errorf("тип bind при пустом имени: got %s, want как у NewDefaultBind (%s)", got, wantType)
	}
}

// TestBindForInterfaceUnknownNameErrors: несуществующий интерфейс → ошибка
// (fail-closed, не молчим про опечатку в конфиге).
func TestBindForInterfaceUnknownNameErrors(t *testing.T) {
	_, err := bindForInterface("nope-not-a-real-iface-999")
	if err == nil {
		t.Fatal("ожидалась ошибка для несуществующего интерфейса")
	}
}

// TestBindForInterfaceKnownName: для реально существующего интерфейса bind
// строится без ошибки. Имя берём динамически из системы, чтобы тест был
// кросс-платформенным (en0/lo0/lo/eth0 — как повезёт в окружении).
func TestBindForInterfaceKnownName(t *testing.T) {
	ifaces, err := net.Interfaces()
	if err != nil || len(ifaces) == 0 {
		t.Skip("нет сетевых интерфейсов в этом окружении")
	}
	name := ifaces[0].Name
	b, err := bindForInterface(name)
	if err != nil {
		t.Fatalf("bindForInterface(%q): %v", name, err)
	}
	if b == nil {
		t.Fatalf("bindForInterface(%q) вернул nil Bind", name)
	}
}

// TestBoundBindImplementsConnBind: статическая гарантия, что возвращаемое значение
// удовлетворяет интерфейсу conn.Bind (контракт device.NewDevice).
func TestBoundBindImplementsConnBind(t *testing.T) {
	var _ conn.Bind = conn.NewStdNetBindForInterface(0)
	ifaces, _ := net.Interfaces()
	if len(ifaces) > 0 {
		var _ conn.Bind = conn.NewStdNetBindForInterface(ifaces[0].Index)
	}
}

func typeName(v any) string {
	return fmt.Sprintf("%T", v)
}
