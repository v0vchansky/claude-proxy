// Package vpnd — root-демон полного VPN (режим `-mode vpnd`, docs/full-vpn-design.md §3, §5).
//
// Транспорт — internal/ipc (JSON Lines `{id,cmd,...}` → `{id,ok,result|error}`), тот же
// формат, что у proxy-control, но набор команд свой (§5): ping, logs, connect-full,
// disconnect-full, status-full. Handler только разбирает запрос и делегирует Manager'у
// (manager.go), который и выполняет фазовую оркестрацию utun/маршрутов/DNS/PF (§4),
// crash-recovery и watchdog (§9). Приватный ключ живёт только в памяти на время
// запроса и НЕ логируется.
package vpnd

import (
	"encoding/json"

	"github.com/v0vchansky/claude-proxy/core/internal/logbuf"
	"github.com/v0vchansky/claude-proxy/core/internal/profile"
)

// request — входящая команда vpnd-сокета. Для connect-full несёт профиль и
// приватный ключ клиента (§5); для прочих команд поля пусты.
type request struct {
	ID         int              `json:"id"`
	Cmd        string           `json:"cmd"`
	Profile    *profile.Profile `json:"profile,omitempty"`
	PrivateKey string           `json:"privateKey,omitempty"`
}

// response — ответ vpnd-сокета (формат совпадает с proxy-control).
type response struct {
	ID     int    `json:"id"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Handler обслуживает команды vpnd-режима поверх транспорта internal/ipc.
type Handler struct {
	version string
	log     *logbuf.Buffer
	mgr     *Manager
}

// NewHandler создаёт Handler vpnd-режима. version отдаётся в ping для
// version-handshake app↔демон (§5); log — журнал (может быть nil); mgr —
// оркестратор полного VPN (может быть nil только в узких тестах ping/logs).
func NewHandler(version string, log *logbuf.Buffer, mgr *Manager) *Handler {
	return &Handler{version: version, log: log, mgr: mgr}
}

// Handle — реализация ipc.Handler: разбирает строку-запрос и возвращает сырой
// JSON-ответ без завершающего `\n` (его добавит транспорт).
func (h *Handler) Handle(line []byte) []byte {
	resp := h.dispatch(line)
	b, err := json.Marshal(resp)
	if err != nil {
		b, _ = json.Marshal(response{ID: resp.ID, OK: false, Error: "internal: " + err.Error()})
	}
	return b
}

func (h *Handler) dispatch(line []byte) response {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return response{OK: false, Error: "невалидный JSON запроса: " + err.Error()}
	}
	switch req.Cmd {
	case "ping":
		// mode+version — защита от рассинхрона app/демон после обновления (§5).
		return response{ID: req.ID, OK: true, Result: map[string]string{
			"mode":    "vpnd",
			"version": h.version,
		}}
	case "logs":
		var lines []string
		if h.log != nil {
			lines = h.log.Journal()
		}
		if lines == nil {
			lines = []string{}
		}
		return response{ID: req.ID, OK: true, Result: map[string][]string{"lines": lines}}
	case "connect-full":
		if h.mgr == nil {
			return response{ID: req.ID, OK: false, Error: "connect-full: оркестратор не инициализирован"}
		}
		if req.Profile == nil {
			return response{ID: req.ID, OK: false, Error: "connect-full: отсутствует profile"}
		}
		if req.PrivateKey == "" {
			return response{ID: req.ID, OK: false, Error: "connect-full: отсутствует privateKey"}
		}
		res, err := h.mgr.Connect(*req.Profile, req.PrivateKey)
		if err != nil {
			return response{ID: req.ID, OK: false, Error: err.Error()}
		}
		return response{ID: req.ID, OK: true, Result: res}
	case "disconnect-full":
		if h.mgr == nil {
			return response{ID: req.ID, OK: false, Error: "disconnect-full: оркестратор не инициализирован"}
		}
		res, err := h.mgr.Disconnect()
		if err != nil {
			return response{ID: req.ID, OK: false, Error: err.Error()}
		}
		return response{ID: req.ID, OK: true, Result: res}
	case "status-full":
		if h.mgr == nil {
			return response{ID: req.ID, OK: false, Error: "status-full: оркестратор не инициализирован"}
		}
		return response{ID: req.ID, OK: true, Result: h.mgr.Status()}
	default:
		return response{ID: req.ID, OK: false, Error: "неизвестная команда: " + req.Cmd}
	}
}
