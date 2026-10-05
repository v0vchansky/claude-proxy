package control

import (
	"encoding/json"

	"github.com/v0vchansky/claude-proxy/core/internal/ipc"
	"github.com/v0vchansky/claude-proxy/core/internal/profile"
	"github.com/v0vchansky/claude-proxy/core/internal/provision"
)

// request — входящая команда (JSON Lines).
type request struct {
	ID                  int                  `json:"id"`
	Cmd                 string               `json:"cmd"`
	Mode                string               `json:"mode,omitempty"`
	Profile             *profile.Profile     `json:"profile,omitempty"`
	PrivKey             string               `json:"privateKey,omitempty"`
	SSH                 *provision.SSHConfig `json:"ssh,omitempty"`
	Provision           *provision.Params    `json:"provision,omitempty"`
	ClientPublicKey     string               `json:"clientPublicKey,omitempty"`
	ClientPublicKeyFull string               `json:"clientPublicKeyFull,omitempty"`
}

// response — ответ на команду.
type response struct {
	ID     int    `json:"id"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Server обслуживает proxy-протокол поверх транспорта ipc. Транспорт даёт сокет,
// accept-loop и построчное чтение/запись; Server предоставляет ему Handler с
// JSON-диспетчем команд proxy-режима (ping/status/connect/disconnect/switch/
// healthcheck/forward/logs/provision). Внешнее поведение протокола неизменно.
type Server struct {
	d   *Daemon
	ipc *ipc.Server
}

// NewServer готовит сокет-сервер proxy-режима на sockPath.
func NewServer(d *Daemon, sockPath string) (*Server, error) {
	s := &Server{d: d}
	ipcSrv, err := ipc.NewServer(sockPath, s.handle)
	if err != nil {
		return nil, err
	}
	s.ipc = ipcSrv
	return s, nil
}

// Serve принимает соединения до закрытия.
func (s *Server) Serve() error { return s.ipc.Serve() }

// Close останавливает сервер и удаляет сокет.
func (s *Server) Close() error { return s.ipc.Close() }

// handle — Handler транспорта: разбирает строку, диспетчит и кодирует ответ.
// Транспорт добавит завершающий `\n` сам, поэтому здесь json.Marshal без него —
// байт-в-байт тот же вывод, что давал прежний json.Encoder.Encode.
func (s *Server) handle(line []byte) []byte {
	resp := s.dispatch(line)
	b, err := json.Marshal(resp)
	if err != nil {
		// Практически недостижимо (response состоит из сериализуемых полей),
		// но ответ всё равно должен уйти валидным JSON.
		b, _ = json.Marshal(response{ID: resp.ID, OK: false, Error: "internal: " + err.Error()})
	}
	return b
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
	case "forward":
		st, err := s.d.Forward(ForwardMode(req.Mode))
		return result(req.ID, st, err)
	case "provision":
		if req.SSH == nil {
			return response{ID: req.ID, OK: false, Error: "provision: отсутствует ssh"}
		}
		if req.ClientPublicKey == "" {
			return response{ID: req.ID, OK: false, Error: "provision: отсутствует clientPublicKey"}
		}
		params := provision.Params{}
		if req.Provision != nil {
			params = *req.Provision
		}
		res, err := s.d.Provision(*req.SSH, params, req.ClientPublicKey, req.ClientPublicKeyFull)
		if err != nil {
			return response{ID: req.ID, OK: false, Error: err.Error(), Result: res}
		}
		return response{ID: req.ID, OK: true, Result: res}
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
