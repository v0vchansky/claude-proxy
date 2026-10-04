package control

import (
	"encoding/json"
	"testing"

	"github.com/v0vchansky/claude-proxy/core/internal/logbuf"
)

func newTestServer() *Server {
	d := NewDaemon("127.0.0.1:8118", "1.1.1.1:443", logbuf.New(10), false)
	return &Server{d: d}
}

// TestPingByteForByte фиксирует, что proxy-ping не изменился после перевода на
// транспорт ipc: ответ ровно {"id":1,"ok":true,"result":{"pong":true}} без \n.
func TestPingByteForByte(t *testing.T) {
	s := newTestServer()
	got := s.handle([]byte(`{"id":1,"cmd":"ping"}`))
	const want = `{"id":1,"ok":true,"result":{"pong":true}}`
	if string(got) != want {
		t.Fatalf("ping:\n got %q\nwant %q", got, want)
	}
}

// TestStatusShape — status отдаёт прежний State-JSON (state=disconnected до connect).
func TestStatusShape(t *testing.T) {
	s := newTestServer()
	got := s.handle([]byte(`{"id":2,"cmd":"status"}`))

	var resp struct {
		ID     int             `json:"id"`
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(got, &resp); err != nil {
		t.Fatalf("status не парсится: %v (%s)", err, got)
	}
	if resp.ID != 2 || !resp.OK {
		t.Fatalf("status: id=%d ok=%v", resp.ID, resp.OK)
	}
	var st State
	if err := json.Unmarshal(resp.Result, &st); err != nil {
		t.Fatalf("result не State: %v", err)
	}
	if st.State != StateDisconnected {
		t.Fatalf("state=%q, ожидалось disconnected", st.State)
	}
	if st.LocalProxy != "127.0.0.1:8118" {
		t.Fatalf("localProxy=%q", st.LocalProxy)
	}
	if st.PingMs != -1 {
		t.Fatalf("pingMs=%d, ожидалось -1", st.PingMs)
	}
}

func TestUnknownCommand(t *testing.T) {
	s := newTestServer()
	got := s.handle([]byte(`{"id":5,"cmd":"frobnicate"}`))
	var resp response
	if err := json.Unmarshal(got, &resp); err != nil {
		t.Fatalf("не парсится: %v", err)
	}
	if resp.OK || resp.ID != 5 || resp.Error == "" {
		t.Fatalf("неизвестная команда: %+v", resp)
	}
}

func TestConnectMissingParams(t *testing.T) {
	s := newTestServer()
	// connect без profile → ok:false с понятной ошибкой, туннель не трогается.
	got := s.handle([]byte(`{"id":6,"cmd":"connect","privateKey":"x"}`))
	var resp response
	if err := json.Unmarshal(got, &resp); err != nil {
		t.Fatalf("не парсится: %v", err)
	}
	if resp.OK || resp.Error == "" {
		t.Fatalf("ожидалась ошибка отсутствия profile: %+v", resp)
	}
}

func TestBrokenJSONNoID(t *testing.T) {
	s := newTestServer()
	got := s.handle([]byte(`{bad`))
	var resp response
	if err := json.Unmarshal(got, &resp); err != nil {
		t.Fatalf("не парсится: %v", err)
	}
	if resp.OK || resp.Error == "" {
		t.Fatalf("ожидалась ошибка невалидного JSON: %+v", resp)
	}
}
