// Package logbuf хранит короткий технический лог. В памяти — кольцевой буфер
// последних строк (для быстрой выдачи), на диске — персистентный журнал с
// ретеншеном по времени (хранить только за последние N дней).
//
// Секреты (ключи, payload, заголовки) сюда не попадают — за этим следит вызывающий код.
//
// Формат строки:
//   - в памяти: "HH:MM:SS сообщение" (коротко, для совместимости);
//   - в файле:  "2006-01-02 15:04:05 сообщение" (полная дата — иначе ретеншен по
//     дате невозможен).
//
// Ретеншен реализован «прополкой» файла (не переписываем файл на каждую строку —
// дорого): прополка идёт на старте (NewWithFile), по тикеру (раз в час) и каждые
// sweepEveryN записей. Прополка читает файл, отбрасывает строки со временем старше
// now-retention и атомарно переписывает файл (temp + rename). Строки без
// распознаваемого таймстемпа СОХРАНЯЮТСЯ (чтобы не терять диагностику при смене
// формата/ручном редактировании) — см. keepLine.
package logbuf

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Форматы меток времени: memLayout — в памяти, fileLayout — в файле.
const (
	memLayout  = "15:04:05"
	fileLayout = "2006-01-02 15:04:05"
)

// sweepInterval — период фоновой прополки файла.
// sweepEveryN — дополнительно прополка каждые N записей (ограничивает рост файла
// между тикерами при всплеске логов).
const (
	sweepInterval = time.Hour
	sweepEveryN   = 2000
)

// Buffer — потокобезопасный лог: кольцевой буфер в памяти + опциональный
// персистентный файл с ретеншеном. Нулевой filePath → только память.
type Buffer struct {
	mu    sync.Mutex
	lines []string
	cap   int
	clock func() time.Time

	// Поля персистентности (активны только при filePath != "").
	filePath  string
	retention time.Duration
	file      *os.File // append-дескриптор; nil в memory-only режиме или при сбое I/O
	sinceSwp  int      // записей с прошлой прополки

	stop   chan struct{}  // закрывается в Close → останавливает тикер
	wg     sync.WaitGroup // ждёт завершения тикер-горутины
	closed bool
}

// New создаёт буфер на capacity последних строк (только память).
func New(capacity int) *Buffer {
	if capacity <= 0 {
		capacity = 500
	}
	return &Buffer{cap: capacity, clock: time.Now}
}

// NewWithFile создаёт буфер с персистентным журналом в filePath и ретеншеном
// retention. Если filePath пустой — ведёт себя как New(capacity) (только память),
// ошибки не возвращает.
//
// Директория создаётся при необходимости (0700), файл открывается на дозапись
// (0600). На старте выполняется прополка (отсечение строк старше now-retention) и
// запускается фоновый тикер прополки. Ошибку возвращает только если не удалось
// подготовить файл/директорию — вызывающий код тогда деградирует на New.
func NewWithFile(capacity int, filePath string, retention time.Duration) (*Buffer, error) {
	b := New(capacity)
	if filePath == "" {
		return b, nil
	}
	if retention <= 0 {
		retention = 72 * time.Hour
	}
	b.filePath = filePath
	b.retention = retention

	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("logbuf: создать директорию журнала %s: %w", dir, err)
	}
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("logbuf: открыть журнал %s: %w", filePath, err)
	}
	b.file = f

	// Прополка на старте: отрезаем всё, что старше ретеншена, до первых записей.
	b.mu.Lock()
	b.sweepLocked()
	b.mu.Unlock()

	// Фоновый тикер прополки.
	b.stop = make(chan struct{})
	b.wg.Add(1)
	go b.sweepLoop()
	return b, nil
}

// Logf добавляет строку в память (метка HH:MM:SS) и, если включён файл,
// дописывает её в журнал с полной датой. Ошибки файловой записи глушатся
// (логировать их некуда) и не прерывают работу.
func (b *Buffer) Logf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	now := b.clock()

	b.mu.Lock()
	defer b.mu.Unlock()

	// Память: кольцевой буфер.
	b.lines = append(b.lines, now.Format(memLayout)+" "+msg)
	if len(b.lines) > b.cap {
		b.lines = b.lines[len(b.lines)-b.cap:]
	}

	// Файл: полная дата + сообщение.
	if b.file != nil {
		_, _ = b.file.WriteString(now.Format(fileLayout) + " " + msg + "\n")
		b.sinceSwp++
		if b.sinceSwp >= sweepEveryN {
			b.sweepLocked()
		}
	}
}

// Lines возвращает копию кольцевого буфера памяти (последние cap строк).
func (b *Buffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

// Journal возвращает строки персистентного журнала за последние retention
// (содержимое файла после прополки). В memory-only режиме (файла нет) отдаёт
// Lines() — чтобы Copy diagnostics всегда что-то показывал.
func (b *Buffer) Journal() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file == nil {
		out := make([]string, len(b.lines))
		copy(out, b.lines)
		return out
	}
	data, err := os.ReadFile(b.filePath)
	if err != nil {
		// Файл мог исчезнуть — тихо падаем обратно на память.
		out := make([]string, len(b.lines))
		copy(out, b.lines)
		return out
	}
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}

// Close останавливает фоновую прополку и закрывает файл журнала. Безопасно
// вызывать повторно и на memory-only буфере.
func (b *Buffer) Close() error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	stop := b.stop
	b.mu.Unlock()

	if stop != nil {
		close(stop)
		b.wg.Wait()
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if b.file != nil {
		err := b.file.Close()
		b.file = nil
		return err
	}
	return nil
}

// sweepLoop — фоновый тикер прополки до Close.
func (b *Buffer) sweepLoop() {
	defer b.wg.Done()
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
			b.mu.Lock()
			b.sweepLocked()
			b.mu.Unlock()
		}
	}
}

// sweepLocked читает файл, отбрасывает строки старше now-retention и атомарно
// переписывает файл (temp + rename). Вызывается под b.mu. Любая ошибка I/O
// глушится — журнал продолжает работать на текущем дескрипторе.
//
// Чтобы rename не оставил open-дескриптор висеть на старом (уже отвязанном)
// inode, перед переписыванием закрываем файл, а после — заново открываем на
// дозапись. Поскольку и запись, и прополка идут под b.mu, конкурентные Logf не
// теряют данные и не пишут мимо файла.
func (b *Buffer) sweepLocked() {
	if b.file == nil {
		return
	}
	b.sinceSwp = 0

	data, err := os.ReadFile(b.filePath)
	if err != nil {
		return
	}

	cutoff := b.clock().Add(-b.retention)
	var kept bytes.Buffer
	changed := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if keepLine(line, cutoff) {
			kept.WriteString(line)
			kept.WriteByte('\n')
		} else {
			changed = true
		}
	}
	if err := sc.Err(); err != nil {
		return // не рискуем переписывать файл по частично прочитанным данным
	}
	if !changed {
		return // нечего отбрасывать — файл не трогаем
	}

	// Закрываем текущий дескриптор перед атомарной подменой.
	if b.file != nil {
		_ = b.file.Close()
		b.file = nil
	}

	tmp, err := os.CreateTemp(filepath.Dir(b.filePath), ".diagnostics-*.tmp")
	if err != nil {
		b.reopenLocked()
		return
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		b.reopenLocked()
		return
	}
	if _, err := tmp.Write(kept.Bytes()); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		b.reopenLocked()
		return
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		b.reopenLocked()
		return
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		b.reopenLocked()
		return
	}
	if err := os.Rename(tmpName, b.filePath); err != nil {
		_ = os.Remove(tmpName)
		b.reopenLocked()
		return
	}
	b.reopenLocked()
}

// reopenLocked заново открывает файл журнала на дозапись после прополки.
// Вызывается под b.mu. При сбое оставляет b.file == nil (деградация на память).
func (b *Buffer) reopenLocked() {
	if b.file != nil || b.filePath == "" {
		return
	}
	f, err := os.OpenFile(b.filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		b.file = nil
		return
	}
	b.file = f
}

// keepLine решает, оставить ли строку при прополке. Строка остаётся, если её
// таймстемп НЕ старше cutoff. Строка без распознаваемого таймстемпа СОХРАНЯЕТСЯ
// (намеренно: не теряем диагностику при смене формата/ручной правке файла).
func keepLine(line string, cutoff time.Time) bool {
	if len(line) < len(fileLayout) {
		return true // слишком короткая для таймстемпа — сохраняем
	}
	ts, err := time.ParseInLocation(fileLayout, line[:len(fileLayout)], time.Local)
	if err != nil {
		return true // не парсится — сохраняем
	}
	return !ts.Before(cutoff)
}
