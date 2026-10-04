// Package logbuf хранит короткий технический лог в кольцевом буфере в памяти.
// Секреты (ключи, payload, заголовки) сюда не попадают — за этим следит вызывающий код.
package logbuf

import (
	"fmt"
	"sync"
	"time"
)

// Buffer — потокобезопасный кольцевой буфер строк лога.
type Buffer struct {
	mu    sync.Mutex
	lines []string
	cap   int
	clock func() time.Time
}

// New создаёт буфер на capacity последних строк.
func New(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = 500
	}
	return &Buffer{cap: capacity, clock: time.Now}
}

// Logf добавляет строку с отметкой времени HH:MM:SS.
func (b *Buffer) Logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	line := b.clock().Format("15:04:05") + " " + msg
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	if len(b.lines) > b.cap {
		b.lines = b.lines[len(b.lines)-b.cap:]
	}
}

// Lines возвращает копию текущих строк лога.
func (b *Buffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}
