package vpnd

import (
	"encoding/json"
	"testing"

	"github.com/v0vchansky/claude-proxy/core/internal/logbuf"
)

// decode разбирает сырой ответ Handler в удобную структуру.
func decode(t *testing.T, raw []byte) response {
	t.Helper()
	var r response
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("ответ не парсится как JSON: %v (%s)", err, raw)
	}
	return r
}

func TestPingReturnsModeAndVersion(t *testing.T) {
	h := NewHandler("1.2.3", logbuf.New(10), nil)
	raw := h.Handle([]byte(`{"id":7,"cmd":"ping"}`))

	// Ответ не должен содержать завершающего \n — его добавляет транспорт.
	if n := len(raw); n > 0 && raw[n-1] == '\n' {
		t.Fatalf("Handler вернул ответ с завершающим \\n: %q", raw)
	}

	r := decode(t, raw)
	if r.ID != 7 || !r.OK {
		t.Fatalf("ping: id=%d ok=%v, ожидалось id=7 ok=true", r.ID, r.OK)
	}
	res, ok := r.Result.(map[string]any)
	if !ok {
		t.Fatalf("result не объект: %#v", r.Result)
	}
	if res["mode"] != "vpnd" {
		t.Fatalf("mode=%v, ожидалось vpnd", res["mode"])
	}
	if res["version"] != "1.2.3" {
		t.Fatalf("version=%v, ожидалось 1.2.3", res["version"])
	}
}

func TestLogsReturnsLines(t *testing.T) {
	log := logbuf.New(10)
	log.Logf("событие")
	h := NewHandler("1.0.0", log, nil)

	r := decode(t, h.Handle([]byte(`{"id":1,"cmd":"logs"}`)))
	if !r.OK {
		t.Fatalf("logs ok=false: %q", r.Error)
	}
	res, ok := r.Result.(map[string]any)
	if !ok {
		t.Fatalf("result не объект: %#v", r.Result)
	}
	lines, ok := res["lines"].([]any)
	if !ok {
		t.Fatalf("lines не массив: %#v", res["lines"])
	}
	if len(lines) != 1 {
		t.Fatalf("ожидалась 1 строка лога, got %d", len(lines))
	}
}

func TestLogsEmptyWithoutBuffer(t *testing.T) {
	h := NewHandler("1.0.0", nil, nil)
	r := decode(t, h.Handle([]byte(`{"id":2,"cmd":"logs"}`)))
	if !r.OK {
		t.Fatalf("logs ok=false: %q", r.Error)
	}
	res := r.Result.(map[string]any)
	lines, ok := res["lines"].([]any)
	if !ok {
		t.Fatalf("lines не массив: %#v", res["lines"])
	}
	if len(lines) != 0 {
		t.Fatalf("без буфера lines должен быть пустым, got %d", len(lines))
	}
}

// TestCommandsWithoutManager: команды полного VPN без оркестратора отвечают
// ошибкой (а не паникуют).
func TestCommandsWithoutManager(t *testing.T) {
	h := NewHandler("1.0.0", nil, nil)
	for _, cmd := range []string{"connect-full", "disconnect-full", "status-full"} {
		r := decode(t, h.Handle([]byte(`{"id":3,"cmd":"`+cmd+`"}`)))
		if r.OK {
			t.Fatalf("%s без mgr: ожидалось ok=false", cmd)
		}
		if r.ID != 3 {
			t.Fatalf("%s: id=%d, ожидалось 3", cmd, r.ID)
		}
		if r.Error == "" {
			t.Fatalf("%s: пустой текст ошибки", cmd)
		}
	}
}

func TestConnectFullRequiresProfileAndKey(t *testing.T) {
	h := NewHandler("1.0.0", nil, newTestManager(t, nil))

	// Нет profile.
	r := decode(t, h.Handle([]byte(`{"id":4,"cmd":"connect-full","privateKey":"k"}`)))
	if r.OK || r.Error == "" {
		t.Fatalf("connect-full без profile: ожидалась ошибка, got %+v", r)
	}
	// Нет privateKey.
	r = decode(t, h.Handle([]byte(`{"id":5,"cmd":"connect-full","profile":{"host":"1.2.3.4","port":51820}}`)))
	if r.OK || r.Error == "" {
		t.Fatalf("connect-full без privateKey: ожидалась ошибка, got %+v", r)
	}
}

func TestUnknownCommand(t *testing.T) {
	h := NewHandler("1.0.0", nil, nil)
	r := decode(t, h.Handle([]byte(`{"id":9,"cmd":"frobnicate"}`)))
	if r.OK {
		t.Fatal("неизвестная команда: ожидалось ok=false")
	}
	if r.ID != 9 {
		t.Fatalf("id=%d, ожидалось 9", r.ID)
	}
	if r.Error == "" {
		t.Fatal("пустой текст ошибки для неизвестной команды")
	}
}

func TestBrokenJSON(t *testing.T) {
	h := NewHandler("1.0.0", nil, nil)
	r := decode(t, h.Handle([]byte(`{"id":1,"cmd":`)))
	if r.OK {
		t.Fatal("битый JSON: ожидалось ok=false")
	}
	if r.Error == "" {
		t.Fatal("битый JSON: пустой текст ошибки")
	}
}
