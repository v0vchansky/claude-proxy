// Package keylock — эксклюзивная межпроцессная блокировка «один клиентский ключ —
// один туннель». Два процесса claude-proxy-core, подключённые одним ключом (один и
// тот же WG-peer на сервере), заставляют сервер перекидывать сессию между двумя
// endpoint'ами: потери пакетов, ретрансмиты, DNS-таймауты. Лок не даёт поднять
// второй туннель тем же ключом, пока первый жив.
//
// Механизм — flock(LOCK_EX|LOCK_NB) на файл <dir>/tunnel-<sha256(pubkey)[:16]>.lock.
// Блокировку держит открытый дескриптор: при смерти процесса (в том числе kill -9)
// ОС освобождает её сама, stale-локов не бывает. Файл не удаляется — удаление
// lock-файла под чужим flock открывает гонку «два владельца разных inode».
// Внутри файла — pid и путь control-сокета владельца, только для текста ошибки.
package keylock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// BusyError — ключ уже занят другим владельцем (другой процесс или другой экземпляр
// в этом же процессе). PID/Sock — то, что владелец записал в lock-файл; пустые, если
// прочитать не удалось (владелец ещё не успел записать).
type BusyError struct {
	Path string
	PID  int
	Sock string
}

func (e *BusyError) Error() string {
	pid := "?"
	if e.PID > 0 {
		pid = strconv.Itoa(e.PID)
	}
	sock := e.Sock
	if sock == "" {
		sock = "?"
	}
	return fmt.Sprintf("Этот ключ уже используется другим процессом claude-proxy-core (pid %s, сокет %s) — два подключения одним ключом ломают туннель", pid, sock)
}

// IsBusy сообщает, что err — занятость ключа (а не ошибка ввода-вывода лока).
func IsBusy(err error) bool {
	var b *BusyError
	return errors.As(err, &b)
}

// Lock — взятая блокировка. Release идемпотентен.
type Lock struct {
	f      *os.File
	path   string
	pubKey string
}

// PubKey — публичный ключ, на который взята блокировка.
func (l *Lock) PubKey() string { return l.pubKey }

// Path — путь lock-файла.
func (l *Lock) Path() string { return l.path }

// PathFor возвращает путь lock-файла для публичного ключа (base64) в каталоге dir.
// В имени — первые 16 hex-символов sha256: сам ключ в имя файла не попадает.
func PathFor(dir, pubKey string) string {
	sum := sha256.Sum256([]byte(pubKey))
	return filepath.Join(dir, "tunnel-"+hex.EncodeToString(sum[:])[:16]+".lock")
}

// Acquire берёт неблокирующую эксклюзивную блокировку на ключ pubKey. Каталог dir
// создаётся при отсутствии (0700). sock — путь control-сокета владельца, пишется в
// файл для диагностики. Занято → *BusyError; иные ошибки — проблемы файловой системы.
func Acquire(dir, pubKey, sock string) (*Lock, error) {
	if pubKey == "" {
		return nil, fmt.Errorf("keylock: пустой публичный ключ")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("keylock: каталог %s: %w", dir, err)
	}
	path := PathFor(dir, pubKey)
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("keylock: открыть %s: %w", path, err)
	}
	if err := flockNB(f); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			pid, owner := readOwner(path)
			return nil, &BusyError{Path: path, PID: pid, Sock: owner}
		}
		return nil, fmt.Errorf("keylock: flock %s: %w", path, err)
	}
	// Лок наш — записываем владельца. Ошибка записи не отменяет лок: защита
	// держится на flock, а содержимое — только для текста ошибки у второго.
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(fmt.Sprintf("pid=%d\nsock=%s\n", os.Getpid(), sock)), 0)
	return &Lock{f: f, path: path, pubKey: pubKey}, nil
}

// Release снимает блокировку и закрывает файл. Повторный вызов и nil — no-op.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = l.f.Truncate(0)
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// flockNB — flock(LOCK_EX|LOCK_NB) с повтором на EINTR.
func flockNB(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err != syscall.EINTR {
			return err
		}
	}
}

// readOwner разбирает pid/sock из lock-файла. Ошибки игнорируются (вернётся 0/"").
func readOwner(path string) (int, string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, ""
	}
	var pid int
	var sock string
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "pid":
			pid, _ = strconv.Atoi(v)
		case "sock":
			sock = v
		}
	}
	return pid, sock
}
