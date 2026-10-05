package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Тесты гоняют РЕАЛЬНЫЙ bash-код (scriptLib), который уходит на сервер, на временном
// фейковом awg0.conf — без awg, root и сети. Живой интерфейс эмулируется переменной
// LIVE_ALLOWED (формат "awg show awg0 allowed-ips").

const (
	keyOld     = "OLDPROXYKEYaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa+/="
	keyOldFull = "OLDFULLKEYbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb+/="
	keyNew     = "NEWPROXYKEYcccccccccccccccccccccccccccccccc+/="
	keyNewFull = "NEWFULLKEYddddddddddddddddddddddddddddddddd+/="
)

const ifaceOnly = `[Interface]
Address = 10.77.0.1/24
ListenPort = 51820
PrivateKey = SERVERPRIVxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx=

Jc = 5
Jmin = 50
Jmax = 1000
S1 = 64
S2 = 128
S3 = 0
S4 = 0

H1 = 1000001
H2 = 1000002
H3 = 1000003
H4 = 1000004
`

func peer(key, allowed string) string {
	return "\n[Peer]\nPublicKey = " + key + "\nAllowedIPs = " + allowed + "\n"
}

type allocIn struct {
	conf             string
	pub, pubFull     string
	pref, prefFull   string
	live             string
	upsert           bool // после выделения — записать peer'ы в конфиг, как шаг 8 скрипта
	normalize        bool // прогнать conf_normalize_s34
	wantErr          bool
	wantErrSubstring string
}

type allocOut struct {
	vars    map[string]string
	conf    string // конфиг после прогона
	stdout  string
	stderr  string
	exitErr error
}

// runLib исполняет scriptLib + обвязку через bash и возвращает переменные alloc_addrs.
func runLib(t *testing.T, in allocIn) allocOut {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash не найден")
	}
	dir := t.TempDir()
	conf := filepath.Join(dir, "awg0.conf")
	if err := os.WriteFile(conf, []byte(in.conf), 0o600); err != nil {
		t.Fatal(err)
	}
	harness := "set -euo pipefail\n" + scriptLib + `
alloc_addrs "$CONF"
echo "OUT_CLIENT_IP=$CLIENT_IP"
echo "OUT_CLIENT_IP_FULL=$CLIENT_IP_FULL"
echo "OUT_SRV_IP=$SRV_IP"
echo "OUT_SRV_NET=$SRV_NET"
echo "OUT_SRV_PREFIX=$SRV_PREFIX"
echo "OUT_REUSED=$REUSED"
echo "OUT_REUSED_FULL=$REUSED_FULL"
echo "OUT_INLIVE=$INLIVE"
echo "OUT_INLIVE_FULL=$INLIVE_FULL"
if [ "${DO_NORMALIZE:-0}" = 1 ]; then
  if conf_normalize_s34 "$CONF"; then echo OUT_IFACE_CHANGED=1; else echo OUT_IFACE_CHANGED=0; fi
fi
if [ "${DO_UPSERT:-0}" = 1 ]; then
  if conf_upsert_peer "$CONF" "$CLIENT_PUB" "$CLIENT_IP"; then echo OUT_CH=1; else echo OUT_CH=0; fi
  if [ -n "$CLIENT_PUB_FULL" ]; then
    if conf_upsert_peer "$CONF" "$CLIENT_PUB_FULL" "$CLIENT_IP_FULL"; then echo OUT_CH_FULL=1; else echo OUT_CH_FULL=0; fi
  fi
fi
`
	cmd := exec.Command("bash", "-c", harness)
	env := append(os.Environ(),
		"CONF="+conf,
		"CLIENT_PUB="+in.pub,
		"CLIENT_PUB_FULL="+in.pubFull,
		"CLIENT_VPN="+in.pref,
		"CLIENT_VPN_FULL="+in.prefFull,
		"LIVE_ALLOWED="+in.live,
	)
	if in.upsert {
		env = append(env, "DO_UPSERT=1")
	}
	if in.normalize {
		env = append(env, "DO_NORMALIZE=1")
	}
	cmd.Env = env
	var so, se strings.Builder
	cmd.Stdout, cmd.Stderr = &so, &se
	runErr := cmd.Run()
	after, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	out := allocOut{vars: map[string]string{}, conf: string(after), stdout: so.String(), stderr: se.String(), exitErr: runErr}
	for _, line := range strings.Split(so.String(), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && strings.HasPrefix(k, "OUT_") {
			out.vars[strings.TrimPrefix(k, "OUT_")] = v
		}
	}
	if in.wantErr {
		if runErr == nil {
			t.Fatalf("ожидалась ошибка, получено:\n%s", so.String())
		}
		if in.wantErrSubstring != "" && !strings.Contains(se.String(), in.wantErrSubstring) {
			t.Fatalf("stderr %q не содержит %q", se.String(), in.wantErrSubstring)
		}
	} else if runErr != nil {
		t.Fatalf("bash упал: %v\nstdout:\n%s\nstderr:\n%s", runErr, so.String(), se.String())
	}
	return out
}

func expectAddrs(t *testing.T, o allocOut, proxy, full string) {
	t.Helper()
	if o.vars["CLIENT_IP"] != proxy || o.vars["CLIENT_IP_FULL"] != full {
		t.Fatalf("адреса: прокси %q, Полный VPN %q; ждали %q / %q", o.vars["CLIENT_IP"], o.vars["CLIENT_IP_FULL"], proxy, full)
	}
}

// Пустой конфиг (только [Interface]) → .2/.3, peer'ы дописаны в конец, [Interface] не тронут.
func TestAllocEmptyConf(t *testing.T) {
	o := runLib(t, allocIn{conf: ifaceOnly, pub: keyNew, pubFull: keyNewFull,
		pref: "10.77.0.2", prefFull: "10.77.0.3", upsert: true})
	expectAddrs(t, o, "10.77.0.2", "10.77.0.3")
	if o.vars["SRV_IP"] != "10.77.0.1" || o.vars["SRV_NET"] != "10.77.0.0" || o.vars["SRV_PREFIX"] != "24" {
		t.Errorf("подсеть: %v", o.vars)
	}
	want := ifaceOnly + peer(keyNew, "10.77.0.2/32") + peer(keyNewFull, "10.77.0.3/32")
	if o.conf != want {
		t.Errorf("конфиг:\n%s\nждали:\n%s", o.conf, want)
	}
	if o.vars["CH"] != "1" || o.vars["CH_FULL"] != "1" {
		t.Errorf("ожидалось изменение конфига: %v", o.vars)
	}
}

// Главный баг: старый клиент на .2/.3, новый ключ с дефолтными запрошенными .2/.3 →
// новому .4/.5, старые peer'ы байт в байт на месте.
func TestAllocNewClientDoesNotStealOldAddresses(t *testing.T) {
	conf := ifaceOnly + peer(keyOld, "10.77.0.2/32") + peer(keyOldFull, "10.77.0.3/32")
	o := runLib(t, allocIn{conf: conf, pub: keyNew, pubFull: keyNewFull,
		pref: "10.77.0.2", prefFull: "10.77.0.3", upsert: true})
	expectAddrs(t, o, "10.77.0.4", "10.77.0.5")
	if o.vars["REUSED"] != "0" || o.vars["REUSED_FULL"] != "0" {
		t.Errorf("новый ключ не должен считаться переиспользованным: %v", o.vars)
	}
	if !strings.HasPrefix(o.conf, conf) {
		t.Errorf("старая часть конфига изменена:\n%s", o.conf)
	}
	want := conf + peer(keyNew, "10.77.0.4/32") + peer(keyNewFull, "10.77.0.5/32")
	if o.conf != want {
		t.Errorf("конфиг:\n%s\nждали:\n%s", o.conf, want)
	}
}

// Повторный provision того же клиента — те же адреса, конфиг байт в байт прежний.
func TestAllocSameKeyIdempotent(t *testing.T) {
	conf := ifaceOnly + peer(keyOld, "10.77.0.2/32") + peer(keyNew, "10.77.0.7/32") + peer(keyNewFull, "10.77.0.9/32")
	o := runLib(t, allocIn{conf: conf, pub: keyNew, pubFull: keyNewFull,
		pref: "10.77.0.2", prefFull: "10.77.0.3", upsert: true, normalize: true})
	expectAddrs(t, o, "10.77.0.7", "10.77.0.9")
	if o.vars["REUSED"] != "1" || o.vars["REUSED_FULL"] != "1" {
		t.Errorf("ожидалось переиспользование: %v", o.vars)
	}
	if o.conf != conf {
		t.Errorf("конфиг изменён при повторном provision:\n%s", o.conf)
	}
	if o.vars["CH"] != "0" || o.vars["CH_FULL"] != "0" || o.vars["IFACE_CHANGED"] != "0" {
		t.Errorf("ожидалось «без изменений»: %v", o.vars)
	}
}

// Повторный прогон после первого (полный цикл дважды) — второй ничего не меняет.
func TestAllocTwiceIsStable(t *testing.T) {
	first := runLib(t, allocIn{conf: ifaceOnly + peer(keyOld, "10.77.0.2/32"), pub: keyNew, pubFull: keyNewFull,
		pref: "10.77.0.2", prefFull: "10.77.0.3", upsert: true})
	expectAddrs(t, first, "10.77.0.3", "10.77.0.4")
	second := runLib(t, allocIn{conf: first.conf, pub: keyNew, pubFull: keyNewFull,
		pref: "10.77.0.2", prefFull: "10.77.0.3", upsert: true})
	expectAddrs(t, second, "10.77.0.3", "10.77.0.4")
	if second.conf != first.conf {
		t.Errorf("второй прогон изменил конфиг:\n%s", second.conf)
	}
}

// Дырки: заняты .2 и .4 → .3/.5.
func TestAllocFillsHoles(t *testing.T) {
	conf := ifaceOnly + peer(keyOld, "10.77.0.2/32") + peer(keyOldFull, "10.77.0.4/32")
	o := runLib(t, allocIn{conf: conf, pub: keyNew, pubFull: keyNewFull, pref: "10.77.0.2", prefFull: "10.77.0.3"})
	expectAddrs(t, o, "10.77.0.3", "10.77.0.5")
}

// Запрошенный адрес свободен → он; занят → наименьший свободный.
func TestAllocPreferredAddress(t *testing.T) {
	conf := ifaceOnly + peer(keyOld, "10.77.0.2/32")
	o := runLib(t, allocIn{conf: conf, pub: keyNew, pubFull: keyNewFull, pref: "10.77.0.50", prefFull: "10.77.0.60"})
	expectAddrs(t, o, "10.77.0.50", "10.77.0.60")

	o = runLib(t, allocIn{conf: conf, pub: keyNew, pubFull: keyNewFull, pref: "10.77.0.2/32", prefFull: "10.77.0.50"})
	expectAddrs(t, o, "10.77.0.3", "10.77.0.50")

	// Запрошенный = адрес сервера / вне подсети / одинаковый для обоих → не берётся.
	o = runLib(t, allocIn{conf: conf, pub: keyNew, pubFull: keyNewFull, pref: "10.77.0.1", prefFull: "192.168.1.5"})
	expectAddrs(t, o, "10.77.0.3", "10.77.0.4")
	o = runLib(t, allocIn{conf: conf, pub: keyNew, pubFull: keyNewFull, pref: "10.77.0.9", prefFull: "10.77.0.9"})
	expectAddrs(t, o, "10.77.0.9", "10.77.0.3")
	// Мусор в запрошенном адресе — просто игнорируется.
	o = runLib(t, allocIn{conf: conf, pub: keyNew, pubFull: keyNewFull, pref: "abc", prefFull: "10.77.0.999"})
	expectAddrs(t, o, "10.77.0.3", "10.77.0.4")
}

// Нестандартная база (10.8.0.1/24) и не-/24 подсеть — адреса из неё, не из дефолтной 10.77.
func TestAllocNonDefaultSubnet(t *testing.T) {
	conf := strings.Replace(ifaceOnly, "Address = 10.77.0.1/24", "Address = 10.8.0.1/24", 1) + peer(keyOld, "10.8.0.2/32")
	o := runLib(t, allocIn{conf: conf, pub: keyNew, pubFull: keyNewFull, pref: "10.77.0.2", prefFull: "10.77.0.3"})
	expectAddrs(t, o, "10.8.0.3", "10.8.0.4")
	if o.vars["SRV_IP"] != "10.8.0.1" {
		t.Errorf("SRV_IP=%q", o.vars["SRV_IP"])
	}

	// /16 с сервером не на .1 и занятым хвостом третьего октета: переход через .255 → .1.0
	// (внутри /16 это обычные хосты).
	conf16 := strings.Replace(ifaceOnly, "Address = 10.77.0.1/24", "Address = 172.16.0.254/16, fd00::1/64", 1) +
		peer(keyOld, "172.16.0.1/32, 172.16.0.2/31") + peer(keyOldFull, "172.16.0.4/30, 172.16.0.8/29, 172.16.0.16/28, 172.16.0.32/27, 172.16.0.64/26, 172.16.0.128/26, 172.16.0.192/27, 172.16.0.224/28, 172.16.0.240/29, 172.16.0.248/30, 172.16.0.252/31")
	o = runLib(t, allocIn{conf: conf16, pub: keyNew, pubFull: keyNewFull})
	expectAddrs(t, o, "172.16.0.255", "172.16.1.0")
	if o.vars["SRV_NET"] != "172.16.0.0" || o.vars["SRV_PREFIX"] != "16" {
		t.Errorf("подсеть: %v", o.vars)
	}

	// /28: сеть .16, хосты .17–.30, сервер .17.
	conf28 := strings.Replace(ifaceOnly, "Address = 10.77.0.1/24", "Address = 192.168.5.17/28", 1)
	o = runLib(t, allocIn{conf: conf28, pub: keyNew, pubFull: keyNewFull, pref: "10.77.0.2"})
	expectAddrs(t, o, "192.168.5.18", "192.168.5.19")

	// Address без маски — исторически /24.
	confNoMask := strings.Replace(ifaceOnly, "Address = 10.77.0.1/24", "Address = 10.9.9.1", 1)
	o = runLib(t, allocIn{conf: confNoMask, pub: keyNew})
	expectAddrs(t, o, "10.9.9.2", "")
}

// Подсеть заполнена → ошибка, конфиг не тронут.
func TestAllocSubnetFull(t *testing.T) {
	// /29: сеть .0, broadcast .7, сервер .1, хосты .2–.6.
	conf := strings.Replace(ifaceOnly, "Address = 10.77.0.1/24", "Address = 10.77.0.1/29", 1) +
		peer(keyOld, "10.77.0.2/32") + peer(keyOldFull, "10.77.0.3/32") +
		peer("K3", "10.77.0.4/32") + peer("K4", "10.77.0.5/32")
	o := runLib(t, allocIn{conf: conf, pub: keyNew, pubFull: keyNewFull, upsert: true,
		wantErr: true, wantErrSubstring: "заполнена"})
	if o.conf != conf {
		t.Errorf("конфиг изменён при ошибке:\n%s", o.conf)
	}
	// Один свободный (.6) — прокси получил бы его, но Полному VPN места нет: тоже ошибка.
	if !strings.Contains(o.stderr, "Полного VPN") {
		t.Errorf("ожидалась ошибка про Полный VPN: %q", o.stderr)
	}
	// Без второго ключа хватает .6.
	o = runLib(t, allocIn{conf: conf, pub: keyNew})
	expectAddrs(t, o, "10.77.0.6", "")

	// Совсем нет места даже для прокси.
	confFull := conf + peer("K5", "10.77.0.6/32")
	o = runLib(t, allocIn{conf: confFull, pub: keyNew, wantErr: true, wantErrSubstring: "для прокси"})
	if o.conf != confFull {
		t.Error("конфиг изменён при ошибке")
	}
	// Слишком маленькая подсеть (/31, /32).
	conf32 := strings.Replace(ifaceOnly, "Address = 10.77.0.1/24", "Address = 10.77.0.1/32", 1)
	runLib(t, allocIn{conf: conf32, pub: keyNew, wantErr: true, wantErrSubstring: "слишком мала"})
}

// Нет ключа Полного VPN → выделяется один адрес, CLIENT_IP_FULL пуст, peer один.
func TestAllocWithoutFullKey(t *testing.T) {
	o := runLib(t, allocIn{conf: ifaceOnly, pub: keyNew, pref: "10.77.0.2", prefFull: "10.77.0.3", upsert: true})
	expectAddrs(t, o, "10.77.0.2", "")
	if strings.Count(o.conf, "[Peer]") != 1 {
		t.Errorf("ожидался один peer:\n%s", o.conf)
	}
	if _, ok := o.vars["CH_FULL"]; ok {
		t.Error("второй peer не должен записываться")
	}
}

// Живой интерфейс: AllowedIPs из "awg show awg0 allowed-ips" тоже заняты, даже если их
// нет в конфиге; свой ключ, живущий только в интерфейсе, переиспользуется и дописывается.
func TestAllocConsidersLivePeers(t *testing.T) {
	live := keyOld + "\t10.77.0.2/32\n" + "LIVEONLYKEY=\t10.77.0.3/32 10.77.0.4/32\n" + "NOIPKEY=\t(none)\n"
	o := runLib(t, allocIn{conf: ifaceOnly + peer(keyOld, "10.77.0.2/32"), pub: keyNew, pubFull: keyNewFull, live: live})
	expectAddrs(t, o, "10.77.0.5", "10.77.0.6")
	if o.vars["INLIVE"] != "0" {
		t.Errorf("INLIVE: %v", o.vars)
	}

	live2 := keyNew + "\t10.77.0.20/32\n"
	o = runLib(t, allocIn{conf: ifaceOnly, pub: keyNew, pubFull: keyNewFull, live: live2, upsert: true})
	expectAddrs(t, o, "10.77.0.20", "10.77.0.2")
	if o.vars["REUSED"] != "1" || o.vars["INLIVE"] != "1" || o.vars["INLIVE_FULL"] != "0" {
		t.Errorf("флаги: %v", o.vars)
	}
	if !strings.Contains(o.conf, peer(keyNew, "10.77.0.20/32")) {
		t.Errorf("живой peer не сохранён в конфиг:\n%s", o.conf)
	}
}

// Ключ есть в конфиге, но без адреса (битая запись) → адрес выделяется, секция заменяется,
// дубликата peer'а нет; соседние peer'ы не тронуты.
func TestUpsertReplacesBrokenPeer(t *testing.T) {
	conf := ifaceOnly + peer(keyOld, "10.77.0.2/32") + "\n[Peer]\nPublicKey = " + keyNew + "\n" + peer(keyOldFull, "10.77.0.3/32")
	o := runLib(t, allocIn{conf: conf, pub: keyNew, upsert: true})
	expectAddrs(t, o, "10.77.0.4", "")
	if strings.Count(o.conf, keyNew) != 1 {
		t.Errorf("дубликат peer'а:\n%s", o.conf)
	}
	for _, p := range []string{peer(keyOld, "10.77.0.2/32"), peer(keyOldFull, "10.77.0.3/32"), peer(keyNew, "10.77.0.4/32")} {
		if !strings.Contains(o.conf, p) {
			t.Errorf("нет %q в:\n%s", p, o.conf)
		}
	}
}

// Формат конфига: регистр ключей, пробелы, комментарии, несколько AllowedIPs, IPv6.
func TestAllocToleratesConfFormatting(t *testing.T) {
	conf := ifaceOnly + "\n# старый клиент\n[peer]\npublickey=" + keyOld + "\n  allowedips =10.77.0.2/32,fd00::2/128\nAllowedIPs = 10.77.0.3/32\n" +
		"\n[Peer]\nAllowedIPs = 10.77.0.4/32\nPublicKey = " + keyNew + "\n"
	o := runLib(t, allocIn{conf: conf, pub: keyNew, pubFull: keyNewFull, upsert: true})
	// keyNew уже есть (AllowedIPs раньше PublicKey) → .4 переиспользуется; Полному VPN — .5.
	expectAddrs(t, o, "10.77.0.4", "10.77.0.5")
	if o.vars["CH"] != "0" {
		t.Error("существующий peer не должен переписываться")
	}
}

// Негатив: одинаковые ключи, нет Address, пустой ключ.
func TestAllocInvalidInput(t *testing.T) {
	runLib(t, allocIn{conf: ifaceOnly, pub: keyNew, pubFull: keyNew, wantErr: true, wantErrSubstring: "совпадают"})
	noAddr := strings.Replace(ifaceOnly, "Address = 10.77.0.1/24\n", "", 1)
	o := runLib(t, allocIn{conf: noAddr, pub: keyNew, upsert: true, wantErr: true, wantErrSubstring: "нет IPv4 Address"})
	if o.conf != noAddr {
		t.Error("конфиг изменён при ошибке")
	}
	runLib(t, allocIn{conf: ifaceOnly, pub: "", wantErr: true, wantErrSubstring: "пустой"})
}

// S3/S4: ненулевые приводятся к 0 (только в [Interface]), нули — файл не трогается.
func TestNormalizeS34(t *testing.T) {
	conf := strings.Replace(strings.Replace(ifaceOnly, "S3 = 0", "S3 = 20", 1), "S4 = 0", "S4 = 30", 1)
	o := runLib(t, allocIn{conf: conf, pub: keyNew, normalize: true})
	if o.vars["IFACE_CHANGED"] != "1" || o.conf != ifaceOnly {
		t.Errorf("нормализация: %v\n%s", o.vars, o.conf)
	}
	o = runLib(t, allocIn{conf: ifaceOnly, pub: keyNew, normalize: true})
	if o.vars["IFACE_CHANGED"] != "0" || o.conf != ifaceOnly {
		t.Errorf("нули не должны менять конфиг: %v", o.vars)
	}
}

// Собранный скрипт синтаксически корректен (bash -n) и проходит shellcheck, если он есть.
func TestBuiltScriptSyntax(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash не найден")
	}
	s := buildScript(Params{}.withDefaults(), "PROXYPUB==", "FULLPUB==")
	f := filepath.Join(t.TempDir(), "provision.sh")
	if err := os.WriteFile(f, []byte("#!/bin/bash\n"+s), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("bash", "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("bash -n: %v\n%s", err, out)
	}
	if _, err := exec.LookPath("shellcheck"); err != nil {
		t.Log("shellcheck не установлен — пропуск")
		return
	}
	// SC1091: /etc/os-release не виден shellcheck'у; SC2034: часть переменных — для вывода.
	if out, err := exec.Command("shellcheck", "-S", "warning", "-e", "SC1091,SC2034", f).CombinedOutput(); err != nil {
		t.Fatalf("shellcheck: %v\n%s", err, out)
	}
}
