// Package vpnd — скелет root-демона полного VPN (режим `-mode vpnd`). Реальная
// VPN-логика (utun, маршруты, PF kill-switch, DNS) здесь ещё НЕ реализована — см.
// задачи 4–8 в docs/full-vpn-design.md §10. Пока это командо-агностичный Handler
// для транспорта internal/ipc с заглушками ping/logs; прочие команды честно
// отвечают «не реализовано».
//
// Протокол тот же, что у proxy-control (JSON Lines `{id,cmd,...}` →
// `{id,ok,result|error}`), но набор команд свой (§5): ping отдаёт режим и версию
// для version-handshake app↔демон.
package vpnd

import (
	"encoding/json"

	"github.com/v0vchansky/claude-proxy/core/internal/logbuf"
)

// request — входящая команда vpnd-сокета.
type request struct {
	ID  int    `json:"id"`
	Cmd string `json:"cmd"`
}

// response — ответ vpnd-сокета (формат совпадает с proxy-control).
type response struct {
	ID     int    `json:"id"`
	OK     bool   `json:"ok"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Handler обслуживает команды vpnd-режима. Версия нужна для version-handshake из
// §5: приложение сверяет её с собственной сборкой и при расхождении переустанавливает
// хелпер.
type Handler struct {
	version string
	log     *logbuf.Buffer
}

// NewHandler создаёт Handler vpnd-режима. version — версия сборки ядра (отдаётся
// в ping), log — буфер технического лога (может быть nil).
func NewHandler(version string, log *logbuf.Buffer) *Handler {
	return &Handler{version: version, log: log}
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
		// Пока лог демона пуст: реальные события появятся с VPN-логикой (§10).
		var lines []string
		if h.log != nil {
			lines = h.log.Lines()
		}
		if lines == nil {
			lines = []string{}
		}
		return response{ID: req.ID, OK: true, Result: map[string][]string{"lines": lines}}
	case "connect-full", "disconnect-full", "status-full":
		// Команды полного VPN будут реализованы в задачах 4–8 (§10).
		return response{ID: req.ID, OK: false, Error: "команда не реализована: " + req.Cmd}
	default:
		return response{ID: req.ID, OK: false, Error: "неизвестная команда: " + req.Cmd}
	}
}
