package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"

	"github.com/v0vchansky/claude-proxy/core/internal/profile"
)

// request — входящая команда (JSON Lines).
type request struct {
	ID      int             `json:"id"`
	Cmd     string          `json:"cmd"`
	Profile *profile.Profile `json:"profile,omitempty"`
	PrivKey string          `json:"privateKey,omitempty"`
}

// response — ответ на команду.
type response struct {
	ID     int `json:"id"`
	OK     bool `json:"ok"`
	Result any  `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Server слушает unix-сокет и обслуживает команды для Daemon.
type Server struct {
	d        *Daemon
	sockPath string
	ln       net.Listener
}

// NewServer готовит сокет-сервер на sockPath.
func NewServer(d *Daemon, sockPath string) (*Server, error) {
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return nil, err
	}
	// Снять возможный stale-сокет от прошлого запуска.
	_ = os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = ln.Close()
		return nil, err
	}
	return &Server{d: d, sockPath: sockPath, ln: ln}, nil
}

// Serve принимает соединения до закрытия listener.
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

// Close останавливает сервер и удаляет сокет.
func (s *Server) Close() error {
	err := s.ln.Close()
	_ = os.Remove(s.sockPath)
	return err
}

func (s *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	enc := json.NewEncoder(conn)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			resp := s.dispatch(line)
			if werr := enc.Encode(resp); werr != nil {
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				// битая строка/разрыв — закрываем соединение
			}
			return
		}
	}
}

func (s *Server) dispatch(line []byte) response {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return response{OK: false, Error: "невалидный JSON запроса: " + err.Error()}
	}
	switch req.Cmd {
	case "ping":
		return response{ID: req.ID, OK: true, Result: map[string]bool{"pong": true}}
	case "status":
		return response{ID: req.ID, OK: true, Result: s.d.Status()}
	case "logs":
		return response{ID: req.ID, OK: true, Result: map[string][]string{"lines": s.d.Logs()}}
	case "disconnect":
		return response{ID: req.ID, OK: true, Result: s.d.Disconnect()}
	case "connect":
		if req.Profile == nil {
			return response{ID: req.ID, OK: false, Error: "connect: отсутствует profile"}
		}
		if req.PrivKey == "" {
			return response{ID: req.ID, OK: false, Error: "connect: отсутствует privateKey"}
		}
		st, err := s.d.Connect(*req.Profile, req.PrivKey)
		return result(req.ID, st, err)
	case "switch":
		if req.Profile == nil {
			return response{ID: req.ID, OK: false, Error: "switch: отсутствует profile"}
		}
		if req.PrivKey == "" {
			return response{ID: req.ID, OK: false, Error: "switch: отсутствует privateKey"}
		}
		st, err := s.d.Switch(*req.Profile, req.PrivKey)
		return result(req.ID, st, err)
	case "healthcheck":
		return response{ID: req.ID, OK: true, Result: s.d.Healthcheck()}
	default:
		return response{ID: req.ID, OK: false, Error: "неизвестная команда: " + req.Cmd}
	}
}

// result формирует ответ: даже при ошибке подключения возвращаем State (state=error)
// и ok=false, чтобы UI сразу увидел причину.
func result(id int, st State, err error) response {
	if err != nil {
		return response{ID: id, OK: false, Error: err.Error(), Result: st}
	}
	return response{ID: id, OK: true, Result: st}
}
