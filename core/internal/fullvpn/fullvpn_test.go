package fullvpn

import "testing"

// TestParseStats проверяет разбор ответа IpcGet без поднятия реального устройства
// (разбор счётчиков root не требует — живой подъём utun проверяется вручную).
func TestParseStats(t *testing.T) {
	raw := "private_key=0000\n" +
		"public_key=aaaa\n" +
		"rx_bytes=1234\n" +
		"tx_bytes=5678\n" +
		"last_handshake_time_sec=1700000000\n" +
		"last_handshake_time_nsec=42\n"

	s := parseStats(raw)
	if s.RxBytes != 1234 {
		t.Errorf("RxBytes=%d, ожидалось 1234", s.RxBytes)
	}
	if s.TxBytes != 5678 {
		t.Errorf("TxBytes=%d, ожидалось 5678", s.TxBytes)
	}
	if s.LastHandshakeUnix != 1700000000 {
		t.Errorf("LastHandshakeUnix=%d, ожидалось 1700000000", s.LastHandshakeUnix)
	}
}

// TestParseStatsEmpty: пустой/битый ввод не паникует и даёт нулевые счётчики
// (handshake == 0 → WaitHandshake ещё ждёт).
func TestParseStatsEmpty(t *testing.T) {
	for _, raw := range []string{"", "мусор\nбез=\n=значения\n", "errno=1\n"} {
		s := parseStats(raw)
		if s.RxBytes != 0 || s.TxBytes != 0 || s.LastHandshakeUnix != 0 {
			t.Errorf("для %q ожидались нули, got %+v", raw, s)
		}
	}
}
