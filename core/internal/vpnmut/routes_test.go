package vpnmut

import (
	"reflect"
	"strings"
	"testing"
)

// joinCmds сплющивает [][]string в один текст — удобно искать подстроки в тестах.
func joinCmds(cmds [][]string) string {
	rows := make([]string, len(cmds))
	for i, c := range cmds {
		rows[i] = strings.Join(c, " ")
	}
	return strings.Join(rows, "\n")
}

func TestBuildRouteUp(t *testing.T) {
	cmds := BuildRouteUp("222.167.208.108", "192.168.2.1", "utun3")

	if len(cmds) != 3 {
		t.Fatalf("ожидалось 3 команды, получено %d: %v", len(cmds), cmds)
	}

	// host-route сервера идёт первой и через физический шлюз.
	wantHost := []string{"/sbin/route", "-q", "-n", "add", "-inet", "222.167.208.108", "-gateway", "192.168.2.1"}
	if !reflect.DeepEqual(cmds[0], wantHost) {
		t.Errorf("host-route:\n получено %v\n хотели   %v", cmds[0], wantHost)
	}

	wantHalf0 := []string{"/sbin/route", "-q", "-n", "add", "-inet", "0.0.0.0/1", "-interface", "utun3"}
	if !reflect.DeepEqual(cmds[1], wantHalf0) {
		t.Errorf("0.0.0.0/1:\n получено %v\n хотели   %v", cmds[1], wantHalf0)
	}
	wantHalf1 := []string{"/sbin/route", "-q", "-n", "add", "-inet", "128.0.0.0/1", "-interface", "utun3"}
	if !reflect.DeepEqual(cmds[2], wantHalf1) {
		t.Errorf("128.0.0.0/1:\n получено %v\n хотели   %v", cmds[2], wantHalf1)
	}

	all := joinCmds(cmds)
	for _, sub := range []string{"0.0.0.0/1", "128.0.0.0/1", "-interface utun3", "222.167.208.108", "-gateway 192.168.2.1"} {
		if !strings.Contains(all, sub) {
			t.Errorf("в выводе up нет %q:\n%s", sub, all)
		}
	}
}

func TestBuildRouteDown(t *testing.T) {
	cmds := BuildRouteDown("222.167.208.108", "192.168.2.1", "utun3")

	if len(cmds) != 3 {
		t.Fatalf("ожидалось 3 команды, получено %d: %v", len(cmds), cmds)
	}

	// Половинки снимаются первыми, host-route — последним (обратный порядок up).
	wantHalf0 := []string{"/sbin/route", "-q", "-n", "delete", "-inet", "0.0.0.0/1", "-interface", "utun3"}
	if !reflect.DeepEqual(cmds[0], wantHalf0) {
		t.Errorf("delete 0.0.0.0/1:\n получено %v\n хотели   %v", cmds[0], wantHalf0)
	}
	wantHalf1 := []string{"/sbin/route", "-q", "-n", "delete", "-inet", "128.0.0.0/1", "-interface", "utun3"}
	if !reflect.DeepEqual(cmds[1], wantHalf1) {
		t.Errorf("delete 128.0.0.0/1:\n получено %v\n хотели   %v", cmds[1], wantHalf1)
	}
	wantHost := []string{"/sbin/route", "-q", "-n", "delete", "-inet", "222.167.208.108"}
	if !reflect.DeepEqual(cmds[2], wantHost) {
		t.Errorf("delete host-route:\n получено %v\n хотели   %v", cmds[2], wantHost)
	}
}

// TestRouteDownMirrorsUp — down зеркалит up: те же цели, но host-route сервера,
// поставленный в up первым (ДО перехвата default), в down снимается последним
// (ПОСЛЕ снятия перехвата); глагол add заменён на delete. Порядок двух половинок
// /1 между собой не важен (они независимы) и в up/down совпадает — как в §4.
func TestRouteDownMirrorsUp(t *testing.T) {
	up := BuildRouteUp("1.2.3.4", "10.0.0.1", "utun9")
	down := BuildRouteDown("1.2.3.4", "10.0.0.1", "utun9")

	if len(up) != len(down) {
		t.Fatalf("разная длина: up=%d down=%d", len(up), len(down))
	}

	// Цель команды — назначение (-inet <что-то>), без глагола и без -gateway.
	target := func(cmd []string) string {
		for i, a := range cmd {
			if a == "-inet" && i+1 < len(cmd) {
				return cmd[i+1]
			}
		}
		return ""
	}

	// host-route сервера: первый в up, последний в down.
	if got := target(up[0]); got != "1.2.3.4" {
		t.Errorf("up[0] должен быть host-route сервера, цель=%q", got)
	}
	if got := target(down[len(down)-1]); got != "1.2.3.4" {
		t.Errorf("down[last] должен быть host-route сервера, цель=%q", got)
	}

	// Множество целей совпадает.
	set := func(cmds [][]string) map[string]bool {
		m := map[string]bool{}
		for _, c := range cmds {
			m[target(c)] = true
		}
		return m
	}
	if !reflect.DeepEqual(set(up), set(down)) {
		t.Errorf("множества целей разошлись: up=%v down=%v", set(up), set(down))
	}

	// Глаголы: up — add, down — delete.
	for i, c := range up {
		if c[3] != "add" {
			t.Errorf("up[%d] глагол не add: %v", i, c)
		}
	}
	for i, c := range down {
		if c[3] != "delete" {
			t.Errorf("down[%d] глагол не delete: %v", i, c)
		}
	}
}
