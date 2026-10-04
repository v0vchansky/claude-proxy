package vpnmut

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sampleState() *State {
	return &State{
		Phase:       PhaseConnected,
		Utun:        "utun9",
		ServerIP:    "222.167.208.108",
		ServerPort:  51820,
		OrigGateway: "192.168.2.1",
		PhysIf:      "en0",
		DnsSnapshot: map[string][]string{
			"Wi-Fi":               {"empty"},
			"USB 10/100/1000 LAN": {"192.168.1.1"},
		},
		PFToken:           "4620695",
		AnchorLoaded:      true,
		ReconnectIntended: true,
		StrictKillSwitch:  false,
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "fullvpn-state.json")
	in := sampleState()

	if err := WriteState(path, in); err != nil {
		t.Fatalf("WriteState: %v", err)
	}
	out, err := ReadState(path)
	if err != nil {
		t.Fatalf("ReadState: %v", err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Errorf("round-trip разошёлся:\n in  %+v\n out %+v", in, out)
	}
}

// После успешной записи рядом не остаётся temp-файлов, а сам файл — валидный JSON
// с правами 0600.
func TestWriteStateAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fullvpn-state.json")

	if err := WriteState(path, sampleState()); err != nil {
		t.Fatalf("WriteState: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("остался temp-файл: %s", e.Name())
		}
	}
	if len(entries) != 1 {
		t.Errorf("ожидался ровно 1 файл в каталоге, получено %d: %v", len(entries), entries)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("права файла %o, ожидалось 600", perm)
	}

	// Перезапись поверх существующего тоже атомарна и не плодит temp.
	in2 := sampleState()
	in2.Phase = PhaseTearingDown
	if err := WriteState(path, in2); err != nil {
		t.Fatalf("WriteState повторно: %v", err)
	}
	entries, _ = os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("после перезаписи ожидался 1 файл, получено %d: %v", len(entries), entries)
	}
	out, err := ReadState(path)
	if err != nil {
		t.Fatalf("ReadState после перезаписи: %v", err)
	}
	if out.Phase != PhaseTearingDown {
		t.Errorf("перезапись не применилась: phase=%q", out.Phase)
	}
}

// Чтение отсутствующего файла: ReadState даёт ошибку, обёртывающую ErrNotExist.
func TestReadStateMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.json")
	_, err := ReadState(path)
	if err == nil {
		t.Fatal("ожидалась ошибка для отсутствующего файла")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("ошибка не оборачивает ErrNotExist: %v", err)
	}
}

// LoadOrClean по отсутствующему файлу возвращает чистый state без ошибки.
func TestLoadOrCleanMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.json")
	s, err := LoadOrClean(path)
	if err != nil {
		t.Fatalf("LoadOrClean: %v", err)
	}
	if s.Phase != PhaseClean {
		t.Errorf("ожидалась фаза clean, получено %q", s.Phase)
	}
}

// LoadOrClean по существующему файлу возвращает его содержимое.
func TestLoadOrCleanExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fullvpn-state.json")
	if err := WriteState(path, sampleState()); err != nil {
		t.Fatal(err)
	}
	s, err := LoadOrClean(path)
	if err != nil {
		t.Fatalf("LoadOrClean: %v", err)
	}
	if s.Phase != PhaseConnected {
		t.Errorf("ожидалась фаза connected, получено %q", s.Phase)
	}
}

// Битое содержимое — ошибка, и это НЕ ErrNotExist (чтобы recovery не спутал
// повреждённый файл с отсутствующим).
func TestReadStateCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ReadState(path)
	if err == nil {
		t.Fatal("ожидалась ошибка разбора")
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Error("битый файл не должен выглядеть как отсутствующий")
	}

	if _, err := LoadOrClean(path); err == nil {
		t.Error("LoadOrClean должен пробросить ошибку разбора, а не вернуть clean")
	}
}
