package logbuf

import (
	"strings"
	"testing"
)

func TestRingBufferCapacity(t *testing.T) {
	b := New(3)
	for i := 0; i < 5; i++ {
		b.Logf("line %d", i)
	}
	lines := b.Lines()
	if len(lines) != 3 {
		t.Fatalf("ожидалось 3 строки, got %d", len(lines))
	}
	// Должны остаться последние три: 2,3,4.
	if !strings.HasSuffix(lines[0], "line 2") ||
		!strings.HasSuffix(lines[2], "line 4") {
		t.Fatalf("неверное окно: %v", lines)
	}
}

func TestTimestampPrefix(t *testing.T) {
	b := New(10)
	b.Logf("hello")
	l := b.Lines()[0]
	// Формат "HH:MM:SS hello".
	if len(l) < 9 || l[2] != ':' || l[5] != ':' {
		t.Fatalf("нет временной метки: %q", l)
	}
	if !strings.HasSuffix(l, "hello") {
		t.Fatalf("нет сообщения: %q", l)
	}
}

func TestLinesReturnsCopy(t *testing.T) {
	b := New(10)
	b.Logf("a")
	got := b.Lines()
	got[0] = "подмена"
	if b.Lines()[0] == "подмена" {
		t.Fatal("Lines вернул ссылку на внутренний срез")
	}
}
