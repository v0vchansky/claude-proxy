// Package ipc — командо-агностичный транспорт control-сокета: unix-сокет +
// accept-loop + построчное чтение/запись в формате JSON Lines (один объект на
// строку, разделитель `\n`). Транспорт ничего не знает о командах: он отдаёт
// каждую входящую строку в Handler и пишет обратно его сырой ответ, добавляя
// завершающий `\n` сам. Вся JSON-специфика остаётся у вызывающего (proxy-control,
// vpnd и т.п.), поэтому один и тот же транспорт переиспользуется обоими режимами
// ядра (см. docs/full-vpn-design.md §5).
package ipc

import (
	"bufio"
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
)

// Handler обрабатывает одну строку-запрос и возвращает сырой JSON-ответ БЕЗ
// завершающего `\n` — перевод строки добавит транспорт. Возврат nil означает
// «ответа нет» (строку молча пропускаем, соединение остаётся открытым).
type Handler func(line []byte) []byte

// Server слушает unix-сокет и обслуживает соединения построчно.
type Server struct {
	sockPath string
	handler  Handler
	ln       net.Listener
}

// NewServer поднимает unix-сокет на sockPath с правами 0600. Родительский каталог
// создаётся при необходимости (0700), возможный stale-сокет от прошлого запуска
// снимается перед Listen.
func NewServer(sockPath string, h Handler) (*Server, error) {
	if h == nil {
		return nil, errors.New("ipc: nil handler")
	}
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return nil, err
	}
	// Снять возможный stale-сокет от прошлого запуска, иначе Listen упадёт
	// с "address already in use".
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = ln.Close()
		_ = os.Remove(sockPath)
		return nil, err
	}
	return &Server{sockPath: sockPath, handler: h, ln: ln}, nil
}

// Serve принимает соединения до закрытия listener. Возврат nil — штатное
// закрытие через Close.
func (s *Server) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleConn(conn)
	}
}

// Close останавливает приём соединений и удаляет сокет-файл.
func (s *Server) Close() error {
	err := s.ln.Close()
	_ = os.Remove(s.sockPath)
	return err
}

// handleConn читает запросы построчно до EOF/ошибки и пишет ответы.
// Битая или пустая строка не роняет соединение: пустую пропускаем, а на
// содержательную строку отвечает (в т.ч. ошибкой) сам Handler.
func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadBytes('\n')
		// Отбросить завершающие \r\n — Handler получает «чистую» строку.
		trimmed := bytes.TrimRight(line, "\r\n")
		if len(trimmed) > 0 {
			if resp := s.handler(trimmed); resp != nil {
				if _, werr := conn.Write(append(resp, '\n')); werr != nil {
					return
				}
			}
		}
		if err != nil {
			// io.EOF или разрыв соединения — выходим штатно.
			return
		}
	}
}
