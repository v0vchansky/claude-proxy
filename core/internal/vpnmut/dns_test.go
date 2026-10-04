package vpnmut

import (
	"reflect"
	"testing"
)

func TestBuildDNSSet(t *testing.T) {
	cmds := BuildDNSSet([]string{"Wi-Fi", "USB 10/100/1000 LAN"}, []string{"1.1.1.1", "8.8.8.8"})

	want := [][]string{
		{"/usr/sbin/networksetup", "-setdnsservers", "Wi-Fi", "1.1.1.1", "8.8.8.8"},
		{"/usr/sbin/networksetup", "-setdnsservers", "USB 10/100/1000 LAN", "1.1.1.1", "8.8.8.8"},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("BuildDNSSet:\n получено %v\n хотели   %v", cmds, want)
	}
}

// Краевой: set без DNS-серверов — должно подставиться слово empty, а не битая
// команда без адресов.
func TestBuildDNSSetEmpty(t *testing.T) {
	cmds := BuildDNSSet([]string{"Wi-Fi"}, nil)
	want := [][]string{{"/usr/sbin/networksetup", "-setdnsservers", "Wi-Fi", "empty"}}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("BuildDNSSet(empty):\n получено %v\n хотели   %v", cmds, want)
	}
}

func TestBuildDNSSetNoServices(t *testing.T) {
	cmds := BuildDNSSet(nil, []string{"1.1.1.1"})
	if len(cmds) != 0 {
		t.Errorf("без сервисов ожидался пустой вывод, получено %v", cmds)
	}
}

func TestBuildDNSRestore(t *testing.T) {
	orig := map[string][]string{
		"Wi-Fi":               {"192.168.1.1"},
		"USB 10/100/1000 LAN": {"10.0.0.1", "10.0.0.2"},
	}
	cmds := BuildDNSRestore(orig)

	// Детерминированный порядок — сортировка по имени сервиса.
	want := [][]string{
		{"/usr/sbin/networksetup", "-setdnsservers", "USB 10/100/1000 LAN", "10.0.0.1", "10.0.0.2"},
		{"/usr/sbin/networksetup", "-setdnsservers", "Wi-Fi", "192.168.1.1"},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("BuildDNSRestore:\n получено %v\n хотели   %v", cmds, want)
	}
}

// Пустой список исходных серверов → слово empty (сервис, у которого DNS не было).
func TestBuildDNSRestoreEmpty(t *testing.T) {
	orig := map[string][]string{"Wi-Fi": {}}
	cmds := BuildDNSRestore(orig)
	want := [][]string{{"/usr/sbin/networksetup", "-setdnsservers", "Wi-Fi", "empty"}}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("restore пустого:\n получено %v\n хотели   %v", cmds, want)
	}
}

// Снимок, где «нет DNS» уже закодировано как ["empty"] (формат state §9),
// проходит насквозь и даёт ту же команду.
func TestBuildDNSRestoreLiteralEmpty(t *testing.T) {
	orig := map[string][]string{"Wi-Fi": {"empty"}}
	cmds := BuildDNSRestore(orig)
	want := [][]string{{"/usr/sbin/networksetup", "-setdnsservers", "Wi-Fi", "empty"}}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("restore ['empty']:\n получено %v\n хотели   %v", cmds, want)
	}
}

// Несколько сервисов, часть с серверами, часть пустые — все попадают в вывод
// в отсортированном порядке, пустые дают empty. Это же покрывает сценарий
// «сервис исчез между снимком и restore»: генерация команд независима по ключам,
// одна запись (включая пустую) не ломает остальные.
func TestBuildDNSRestoreMixedServices(t *testing.T) {
	orig := map[string][]string{
		"Wi-Fi":              {"1.1.1.1"},
		"Ethernet":           {},        // DNS не было
		"Thunderbolt Bridge": {"empty"}, // литеральный empty из state
	}
	cmds := BuildDNSRestore(orig)

	want := [][]string{
		{"/usr/sbin/networksetup", "-setdnsservers", "Ethernet", "empty"},
		{"/usr/sbin/networksetup", "-setdnsservers", "Thunderbolt Bridge", "empty"},
		{"/usr/sbin/networksetup", "-setdnsservers", "Wi-Fi", "1.1.1.1"},
	}
	if !reflect.DeepEqual(cmds, want) {
		t.Errorf("restore смешанного:\n получено %v\n хотели   %v", cmds, want)
	}
}

func TestBuildDNSRestoreEmptyMap(t *testing.T) {
	if cmds := BuildDNSRestore(nil); len(cmds) != 0 {
		t.Errorf("пустой снимок → пустой вывод, получено %v", cmds)
	}
}
