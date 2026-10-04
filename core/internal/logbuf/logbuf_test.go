package logbuf

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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

// --- Персистентный журнал с ретеншеном ---

// hasFullDatePrefix проверяет префикс "2006-01-02 15:04:05 ".
func hasFullDatePrefix(line string) bool {
	if len(line) < len(fileLayout)+1 {
		return false
	}
	if _, err := time.ParseInLocation(fileLayout, line[:len(fileLayout)], time.Local); err != nil {
		return false
	}
	return line[len(fileLayout)] == ' '
}

func TestLogfWritesFullDateToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostics.log")
	b, err := NewWithFile(500, path, 72*time.Hour)
	if err != nil {
		t.Fatalf("NewWithFile: %v", err)
	}
	defer b.Close()

	b.Logf("hello %s", "world")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	line := strings.TrimRight(string(data), "\n")
	if !hasFullDatePrefix(line) {
		t.Fatalf("в файле нет полной даты: %q", line)
	}
	if !strings.HasSuffix(line, "hello world") {
		t.Fatalf("нет сообщения в файле: %q", line)
	}
	// Journal читает ту же строку.
	j := b.Journal()
	if len(j) != 1 || !strings.HasSuffix(j[0], "hello world") {
		t.Fatalf("Journal не вернул строку файла: %v", j)
	}
	// В памяти — короткая метка HH:MM:SS.
	mem := b.Lines()
	if len(mem) != 1 || mem[0][2] != ':' || mem[0][5] != ':' {
		t.Fatalf("память не в формате HH:MM:SS: %v", mem)
	}
}

func TestFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "diagnostics.log")
	b, err := NewWithFile(500, path, 72*time.Hour)
	if err != nil {
		t.Fatalf("NewWithFile: %v", err)
	}
	defer b.Close()
	b.Logf("x")

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("права файла %v, ожидалось 0600", fi.Mode().Perm())
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("права директории %v, ожидалось 0700", di.Mode().Perm())
	}
}

func TestRetentionDropsOldKeepsFresh(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostics.log")
	old := time.Now().Add(-100 * time.Hour).Format(fileLayout) // старше 72h
	fresh := time.Now().Add(-1 * time.Hour).Format(fileLayout) // свежее
	content := old + " OLD line\n" + fresh + " FRESH line\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	// Старт NewWithFile прополет файл.
	b, err := NewWithFile(500, path, 72*time.Hour)
	if err != nil {
		t.Fatalf("NewWithFile: %v", err)
	}
	defer b.Close()

	j := b.Journal()
	if len(j) != 1 {
		t.Fatalf("ожидалась 1 строка после прополки, got %d: %v", len(j), j)
	}
	if !strings.HasSuffix(j[0], "FRESH line") {
		t.Fatalf("осталась не та строка: %v", j)
	}
}

func TestRetentionKeepsUnparseableLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostics.log")
	old := time.Now().Add(-100 * time.Hour).Format(fileLayout)
	fresh := time.Now().Add(-1 * time.Hour).Format(fileLayout)
	content := old + " OLD line\n" +
		"строка без таймстемпа\n" +
		fresh + " FRESH line\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	b, err := NewWithFile(500, path, 72*time.Hour)
	if err != nil {
		t.Fatalf("NewWithFile: %v", err)
	}
	defer b.Close()

	j := b.Journal()
	// Старая отброшена; без таймстемпа — сохранена; свежая — сохранена.
	if len(j) != 2 {
		t.Fatalf("ожидалось 2 строки, got %d: %v", len(j), j)
	}
	joined := strings.Join(j, "\n")
	if strings.Contains(joined, "OLD line") {
		t.Fatalf("старая строка не отброшена: %v", j)
	}
	if !strings.Contains(joined, "без таймстемпа") {
		t.Fatalf("строка без таймстемпа потеряна: %v", j)
	}
	if !strings.Contains(joined, "FRESH line") {
		t.Fatalf("свежая строка потеряна: %v", j)
	}
}

func TestSweepAtomicNoCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostics.log")
	old := time.Now().Add(-100 * time.Hour).Format(fileLayout)
	fresh := time.Now().Add(-1 * time.Hour).Format(fileLayout)
	if err := os.WriteFile(path, []byte(old+" OLD\n"+fresh+" FRESH\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	b, err := NewWithFile(500, path, 72*time.Hour)
	if err != nil {
		t.Fatalf("NewWithFile: %v", err)
	}
	defer b.Close()

	// После прополки временных файлов не остаётся, только сам журнал.
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(ents) != 1 || ents[0].Name() != "diagnostics.log" {
		var names []string
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Fatalf("в директории лишние файлы: %v", names)
	}
	// Дозапись после прополки идёт в тот же файл.
	b.Logf("after sweep")
	j := b.Journal()
	if len(j) == 0 || !strings.HasSuffix(j[len(j)-1], "after sweep") {
		t.Fatalf("запись после прополки потеряна: %v", j)
	}
}

func TestConcurrentLogfSweepJournal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "diagnostics.log")
	// retention большой → прополка ничего не отбрасывает, все строки должны дожить.
	b, err := NewWithFile(500, path, time.Hour)
	if err != nil {
		t.Fatalf("NewWithFile: %v", err)
	}
	defer b.Close()

	const writers, perWriter = 8, 200
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				b.Logf("w%d-%d", n, j)
			}
		}(w)
	}
	// Параллельные ручные прополки.
	for s := 0; s < 4; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				b.mu.Lock()
				b.sweepLocked()
				b.mu.Unlock()
			}
		}()
	}
	// Параллельные чтения журнала.
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				_ = b.Journal()
			}
		}()
	}
	wg.Wait()

	j := b.Journal()
	if len(j) != writers*perWriter {
		t.Fatalf("ожидалось %d строк, got %d (гонка потеряла данные?)", writers*perWriter, len(j))
	}
}

func TestEmptyPathMemoryOnly(t *testing.T) {
	b, err := NewWithFile(10, "", 72*time.Hour)
	if err != nil {
		t.Fatalf("NewWithFile(\"\"): %v", err)
	}
	defer b.Close()
	if b.file != nil {
		t.Fatal("ожидался memory-only режим (file == nil)")
	}
	b.Logf("mem %d", 1)
	// Journal в memory-only отдаёт то же, что Lines.
	j := b.Journal()
	l := b.Lines()
	if len(j) != 1 || len(l) != 1 || j[0] != l[0] {
		t.Fatalf("Journal != Lines в memory-only: %v vs %v", j, l)
	}
}

func TestCloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	b, err := NewWithFile(10, filepath.Join(dir, "d.log"), 72*time.Hour)
	if err != nil {
		t.Fatalf("NewWithFile: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close#1: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close#2: %v", err)
	}
}
