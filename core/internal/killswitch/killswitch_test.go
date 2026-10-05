package killswitch

import (
	"slices"
	"strings"
	"testing"
)

// sampleParams — типовой вход для рендера: utun3, endpoint 203.0.113.7:51820.
func sampleParams(allowLAN bool) Params {
	return Params{
		UtunIf:     "utun3",
		ServerIP:   "203.0.113.7",
		ServerPort: 51820,
		AllowLAN:   allowLAN,
	}
}

func TestRenderRules_FailClosedBlocksInetAndInet6(t *testing.T) {
	rules := RenderRules(sampleParams(true))

	// `block return out all` без address-family закрывает и inet, и inet6 —
	// проверяем сам fail-closed и то, что он объявлен раньше любого pass.
	if !strings.Contains(rules, "block return out all") {
		t.Fatalf("нет fail-closed `block return out all`:\n%s", rules)
	}
	blockIdx := strings.Index(rules, "block return out all")
	passIdx := strings.Index(rules, "pass out quick")
	if passIdx < blockIdx {
		t.Errorf("pass-правила объявлены раньше block (block@%d pass@%d)", blockIdx, passIdx)
	}

	// Для inet6 не должно быть НИ ОДНОГО pass — иначе v6 получил бы обход
	// (туннель v4-only, политика «нет v6, пока VPN включён»).
	for _, line := range strings.Split(rules, "\n") {
		if strings.HasPrefix(line, "pass") && strings.Contains(line, "inet6") {
			t.Errorf("найден pass для inet6 (утечка v6): %q", line)
		}
	}
}

func TestRenderRules_PassQuickSpecificity(t *testing.T) {
	rules := RenderRules(sampleParams(true))

	// Обязательные pass quick по убыванию специфичности. lo0 идёт первым и в форме
	// «оба направления, без state» (аналог set skip on lo0 внутри якоря).
	want := []string{
		"pass quick on lo0 all no state",
		"pass out quick on utun3 inet all keep state",
		"pass out quick inet proto udp from any to 203.0.113.7 port = 51820 keep state",
		"pass out quick inet to <cpx_lan> keep state",
	}
	// Все присутствуют и идут именно в этом порядке.
	prev := -1
	for _, w := range want {
		idx := strings.Index(rules, w)
		if idx < 0 {
			t.Fatalf("нет правила %q:\n%s", w, rules)
		}
		if idx < prev {
			t.Errorf("правило %q вне порядка специфичности", w)
		}
		prev = idx
	}

	// lo0 всё разрешаем (all), туннель — только inet.
	loIdx := strings.Index(rules, "pass quick on lo0 all no state")
	utunIdx := strings.Index(rules, "pass out quick on utun3 inet all")
	if loIdx < 0 || utunIdx < 0 {
		t.Fatalf("lo0/utun правила не на месте")
	}
}

// TestRenderRules_LoopbackUnfiltered фиксирует критический фикс loopback: правило
// для lo0 должно выводить петлю из фильтрации так же, как канонический
// `set skip on lo0`, но оставаясь ПРАВИЛОМ внутри суб-якоря (откатываемо флашем).
func TestRenderRules_LoopbackUnfiltered(t *testing.T) {
	for _, allowLAN := range []bool{true, false} {
		rules := RenderRules(sampleParams(allowLAN))

		// lo0-правило присутствует в обоих режимах (строгий и с LAN).
		if !strings.Contains(rules, "pass quick on lo0 all no state") {
			t.Fatalf("AllowLAN=%v: нет освобождения lo0 `pass quick on lo0 all no state`:\n%s", allowLAN, rules)
		}

		// Освобождение lo0 идёт ПОСЛЕ fail-closed block (это pass-исключение),
		// но РАНЬШЕ прочих pass — петля важнее всего и не должна зависеть от них.
		blockIdx := strings.Index(rules, "block return out all")
		loIdx := strings.Index(rules, "pass quick on lo0 all no state")
		utunIdx := strings.Index(rules, "pass out quick on utun3")
		if !(blockIdx >= 0 && loIdx > blockIdx) {
			t.Errorf("AllowLAN=%v: lo0-pass должен идти после block (block@%d lo0@%d)", allowLAN, blockIdx, loIdx)
		}
		if !(utunIdx >= 0 && loIdx < utunIdx) {
			t.Errorf("AllowLAN=%v: lo0-pass должен идти раньше utun-pass (lo0@%d utun@%d)", allowLAN, loIdx, utunIdx)
		}

		// lo0 — без направления (оба) и без state: иначе loopback на macOS рвётся.
		loLine := ""
		for _, line := range strings.Split(rules, "\n") {
			if strings.Contains(line, "on lo0") {
				loLine = line
				break
			}
		}
		if loLine == "" {
			t.Fatalf("AllowLAN=%v: строка правила lo0 не найдена:\n%s", allowLAN, rules)
		}
		if strings.Contains(loLine, "keep state") {
			t.Errorf("AllowLAN=%v: lo0-правило со stateful `keep state` — ломает петлю: %q", allowLAN, loLine)
		}
		if !strings.Contains(loLine, "no state") {
			t.Errorf("AllowLAN=%v: lo0-правило должно быть stateless `no state`: %q", allowLAN, loLine)
		}
		if strings.Contains(loLine, "pass out quick on lo0") || strings.Contains(loLine, "pass in quick on lo0") {
			t.Errorf("AllowLAN=%v: lo0-правило должно быть БЕЗ направления (оба in/out): %q", allowLAN, loLine)
		}

		// `set skip` в якоре не используем намеренно (глобальная опция main ruleset,
		// необратима флашем якоря) — его быть не должно.
		if strings.Contains(rules, "set skip") {
			t.Errorf("AllowLAN=%v: `set skip` не должен попадать в якорь (глобальная опция, необратима):\n%s", allowLAN, rules)
		}

		// Анти-утечка не ослаблена: fail-closed дефолт на месте.
		if !strings.Contains(rules, "block return out all") {
			t.Errorf("AllowLAN=%v: пропал fail-closed `block return out all`:\n%s", allowLAN, rules)
		}
	}
}

func TestRenderRules_UtunAndEndpointSubstitution(t *testing.T) {
	p := Params{UtunIf: "utun9", ServerIP: "222.167.208.108", ServerPort: 443}
	rules := RenderRules(p)

	if !strings.Contains(rules, "pass out quick on utun9 inet all keep state") {
		t.Errorf("не подставлен utun9:\n%s", rules)
	}
	if !strings.Contains(rules, "to 222.167.208.108 port = 443 keep state") {
		t.Errorf("не подставлены serverIP/port:\n%s", rules)
	}
	// Чужого utun-имени быть не должно.
	if strings.Contains(rules, "utun3") {
		t.Errorf("протекло постороннее имя интерфейса:\n%s", rules)
	}
}

func TestRenderRules_AllowLANToggle(t *testing.T) {
	with := RenderRules(sampleParams(true))
	if !strings.Contains(with, "table <cpx_lan> const {") {
		t.Errorf("AllowLAN=true: нет таблицы cpx_lan:\n%s", with)
	}
	if !strings.Contains(with, "pass out quick inet to <cpx_lan> keep state") {
		t.Errorf("AllowLAN=true: нет pass для LAN:\n%s", with)
	}
	// Дефолтный состав таблицы — из §7.
	for _, sub := range DefaultLANSubnets {
		if !strings.Contains(with, sub) {
			t.Errorf("AllowLAN=true: в таблице нет подсети %s", sub)
		}
	}

	without := RenderRules(sampleParams(false))
	if strings.Contains(without, "cpx_lan") {
		t.Errorf("AllowLAN=false: таблица/правило LAN не должны рендериться:\n%s", without)
	}
	// Остальные правила на месте и без LAN.
	if !strings.Contains(without, "block return out all") ||
		!strings.Contains(without, "pass quick on lo0 all no state") ||
		!strings.Contains(without, "pass out quick on utun3 inet all keep state") {
		t.Errorf("AllowLAN=false: базовые правила потерялись:\n%s", without)
	}
}

func TestRenderRules_CustomLANSubnets(t *testing.T) {
	p := sampleParams(true)
	p.LANSubnets = []string{"192.168.50.0/24"}
	rules := RenderRules(p)

	if !strings.Contains(rules, "table <cpx_lan> const { 192.168.50.0/24 }") {
		t.Errorf("кастомные подсети не подставлены:\n%s", rules)
	}
	// Дефолтные подсети не должны просочиться при явном переопределении.
	if strings.Contains(rules, "10.0.0.0/8") {
		t.Errorf("протекли дефолтные подсети при кастомном списке:\n%s", rules)
	}
}

func TestLoadRulesArgv(t *testing.T) {
	got := LoadRulesArgv()
	want := []string{"/sbin/pfctl", "-a", "com.apple/250.ClaudeProxyVPN", "-f", "-"}
	if !slices.Equal(got, want) {
		t.Errorf("LoadRulesArgv = %v, хотели %v", got, want)
	}
}

func TestFlushAnchorArgv(t *testing.T) {
	got := FlushAnchorArgv()
	want := []string{"/sbin/pfctl", "-a", "com.apple/250.ClaudeProxyVPN", "-F", "all"}
	if !slices.Equal(got, want) {
		t.Errorf("FlushAnchorArgv = %v, хотели %v", got, want)
	}
}

func TestEnableArgv(t *testing.T) {
	got := EnableArgv()
	want := []string{"/sbin/pfctl", "-E"}
	if !slices.Equal(got, want) {
		t.Errorf("EnableArgv = %v, хотели %v", got, want)
	}
}

func TestDisableArgv(t *testing.T) {
	got := DisableArgv("4620695902811238862")
	want := []string{"/sbin/pfctl", "-X", "4620695902811238862"}
	if !slices.Equal(got, want) {
		t.Errorf("DisableArgv = %v, хотели %v", got, want)
	}
}

func TestParseEnableToken_RealSample(t *testing.T) {
	// Образец реального stderr `pfctl -E`.
	stderr := "pf enabled\nToken : 4620695902811238862\n"
	tok, err := ParseEnableToken(stderr)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if tok != "4620695902811238862" {
		t.Errorf("токен = %q, хотели 4620695902811238862", tok)
	}
}

func TestParseEnableToken_AlreadyEnabled(t *testing.T) {
	// Когда PF уже включён, pfctl всё равно выдаёт токен.
	stderr := "pf already enabled\nToken : 123\n"
	tok, err := ParseEnableToken(stderr)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if tok != "123" {
		t.Errorf("токен = %q, хотели 123", tok)
	}
}

func TestParseEnableToken_ExtraWhitespace(t *testing.T) {
	// Терпим разный интервал вокруг двоеточия.
	stderr := "pf enabled\r\n   Token :   987654321  \r\n"
	tok, err := ParseEnableToken(stderr)
	if err != nil {
		t.Fatalf("неожиданная ошибка: %v", err)
	}
	if tok != "987654321" {
		t.Errorf("токен = %q, хотели 987654321", tok)
	}
}

func TestParseEnableToken_Negative(t *testing.T) {
	cases := map[string]string{
		"пусто":              "",
		"только pf enabled":  "pf enabled\n",
		"мусор":              "command not found: pfctl",
		"Token без числа":    "pf enabled\nToken : \n",
		"Token нечисловой":   "pf enabled\nToken : abc\n",
		"число без метки":    "pf enabled\n4620695902811238862\n",
		"Token внутри слова": "pf enabled\nMyToken : 42\n",
	}
	for name, stderr := range cases {
		t.Run(name, func(t *testing.T) {
			tok, err := ParseEnableToken(stderr)
			if err == nil {
				t.Errorf("ждали ошибку, получили токен %q", tok)
			}
			if tok != "" {
				t.Errorf("при ошибке токен должен быть пуст, получили %q", tok)
			}
		})
	}
}
