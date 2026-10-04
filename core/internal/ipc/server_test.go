package ipc

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// shortTempDir — t.TempDir() на macOS даёт слишком длинный путь, а sun_path
// unix-сокета ограничен ~104 байтами. Берём короткий каталог под /tmp.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ipc")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// startTestServer поднимает транспорт на временном сокете и гарантирует его
// закрытие по завершении теста.
func startTestServer(t *testing.T, h Handler) string {
	t.Helper()
	sock := filepath.Join(shortTempDir(t), "test.sock")
	srv, err := NewServer(sock, h)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

// dialWithRetry — Serve стартует в горутине, сокет может быть ещё не готов.
func dialWithRetry(t *testing.T, sock string) net.Conn {
	t.Helper()
	var lastErr error
	for i := 0; i < 50; i++ {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			return conn
		}
		lastErr = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Dial %s: %v", sock, lastErr)
	return nil
}

func TestHandlerCalledAndResponseWritten(t *testing.T) {
	var got atomic.Value // []byte
	sock := startTestServer(t, func(line []byte) []byte {
		cp := append([]byte(nil), line...)
		got.Store(cp)
		return []byte(`{"ok":true}`)
	})

	conn := dialWithRetry(t, sock)
	defer conn.Close()

	if _, err := conn.Write([]byte("hello-request\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	if line != "{\"ok\":true}\n" {
		t.Fatalf("ответ не совпал: %q", line)
	}
	// Handler получает строку без завершающего \n.
	rawv := got.Load()
	if rawv == nil {
		t.Fatal("Handler не был вызван")
	}
	if raw := string(rawv.([]byte)); raw != "hello-request" {
		t.Fatalf("Handler получил %q, ожидалось %q", raw, "hello-request")
	}
}

func TestMultipleSequentialRequests(t *testing.T) {
	var calls int32
	sock := startTestServer(t, func(line []byte) []byte {
		atomic.AddInt32(&calls, 1)
		return line // эхо
	})

	conn := dialWithRetry(t, sock)
	defer conn.Close()
	r := bufio.NewReader(conn)

	for _, want := range []string{"one", "two", "three"} {
		if _, err := conn.Write([]byte(want + "\n")); err != nil {
			t.Fatalf("Write %q: %v", want, err)
		}
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("ReadString %q: %v", want, err)
		}
		if line != want+"\n" {
			t.Fatalf("эхо: got %q, want %q", line, want+"\n")
		}
	}
	if n := atomic.LoadInt32(&calls); n != 3 {
		t.Fatalf("Handler вызван %d раз, ожидалось 3", n)
	}
}

func TestEmptyAndBrokenLinesDoNotCrash(t *testing.T) {
	var calls int32
	sock := startTestServer(t, func(line []byte) []byte {
		atomic.AddInt32(&calls, 1)
		return []byte("resp")
	})

	conn := dialWithRetry(t, sock)
	defer conn.Close()
	r := bufio.NewReader(conn)

	// Пустые строки пропускаются транспортом без вызова Handler и без ответа.
	if _, err := conn.Write([]byte("\n\n")); err != nil {
		t.Fatalf("Write empties: %v", err)
	}
	// Содержательная строка после пустых — сервер жив и отвечает.
	if _, err := conn.Write([]byte("real\n")); err != nil {
		t.Fatalf("Write real: %v", err)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	if line != "resp\n" {
		t.Fatalf("ответ: got %q, want %q", line, "resp\n")
	}
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Fatalf("Handler вызван %d раз (пустые строки не должны вызывать), ожидалось 1", n)
	}
}

func TestHandlerNilResponseSkipped(t *testing.T) {
	sock := startTestServer(t, func(line []byte) []byte {
		if string(line) == "silent" {
			return nil // ответа нет
		}
		return []byte("resp")
	})

	conn := dialWithRetry(t, sock)
	defer conn.Close()
	r := bufio.NewReader(conn)

	// На "silent" ответа быть не должно; следом "speak" должен ответить.
	if _, err := conn.Write([]byte("silent\nspeak\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	line, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	if line != "resp\n" {
		t.Fatalf("ожидался единственный ответ resp на speak, got %q", line)
	}
}

func TestStaleSocketRemoved(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "stale.sock")
	// Оставляем «протухший» файл на месте сокета (как после прошлого запуска).
	if err := os.WriteFile(sock, []byte("stale"), 0o600); err != nil {
		t.Fatalf("подготовка stale-файла: %v", err)
	}

	srv, err := NewServer(sock, func(line []byte) []byte { return []byte("ok") })
	if err != nil {
		t.Fatalf("NewServer поверх stale-сокета: %v", err)
	}
	defer srv.Close()
	go func() { _ = srv.Serve() }()

	conn := dialWithRetry(t, sock)
	defer conn.Close()
	if _, err := conn.Write([]byte("x\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("ReadString: %v", err)
	}
	if line != "ok\n" {
		t.Fatalf("got %q", line)
	}
}

func TestSocketPermissions0600(t *testing.T) {
	sock := startTestServer(t, func(line []byte) []byte { return nil })
	// Дать Serve создать/заняться сокетом.
	_ = dialWithRetry(t, sock).Close()

	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("права сокета %o, ожидалось 0600", perm)
	}
}

func TestNilHandlerRejected(t *testing.T) {
	_, err := NewServer(filepath.Join(shortTempDir(t), "x.sock"), nil)
	if err == nil {
		t.Fatal("ожидалась ошибка на nil-handler")
	}
}

func TestCloseRemovesSocket(t *testing.T) {
	sock := filepath.Join(shortTempDir(t), "c.sock")
	srv, err := NewServer(sock, func(line []byte) []byte { return nil })
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); _ = srv.Serve() }()
	_ = dialWithRetry(t, sock).Close()

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	wg.Wait()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("сокет не удалён после Close: err=%v", err)
	}
}
