package vpnmut

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Phase — фаза жизненного цикла full-tunnel VPN (§9). Enum кодирует, какие слои
// системы уже изменены, и этого достаточно для идемпотентного teardown'а и
// crash-recovery: каждая следующая фаза означает, что все предыдущие слои тоже
// установлены (utun → маршруты → DNS → PF → connected).
type Phase string

const (
	// PhaseClean — система в исходном состоянии, менять нечего.
	PhaseClean Phase = "clean"
	// PhasePreparing — снимок сети снят, state записан, изменений системы ещё нет.
	PhasePreparing Phase = "preparing"
	// PhaseRouted — маршруты фазы 3 установлены (half-routes + host-route).
	PhaseRouted Phase = "routed"
	// PhaseDNSSet — DNS подменены (фаза 4); dnsSnapshot обязателен для restore.
	PhaseDNSSet Phase = "dns-set"
	// PhasePFSet — kill-switch PF загружен (фаза 5); pfToken сохранён.
	PhasePFSet Phase = "pf-set"
	// PhaseConnected — рабочий режим (фаза 6).
	PhaseConnected Phase = "connected"
	// PhaseTearingDown — идёт штатный teardown.
	PhaseTearingDown Phase = "tearing-down"
)

// State — содержимое state-файла /var/db/claude-proxy/fullvpn-state.json
// (root 0600, запись атомарно через rename). Поля и их имена в JSON соответствуют
// образцу §9.
//
// Инвариант: любое изменение системы пишется в state ДО его выполнения, а запуск
// демона начинается с выверки state. DnsSnapshot критичен: из всех слоёв только
// DNS переживает ребут (networksetup пишет в preferences), поэтому снимок исходных
// DNS — единственный способ вернуть их после краха+ребута.
type State struct {
	Phase       Phase  `json:"phase"`
	Utun        string `json:"utun"`
	ServerIP    string `json:"serverIP"`
	ServerPort  int    `json:"serverPort"`
	OrigGateway string `json:"origGateway"`
	PhysIf      string `json:"physIf"`
	// DnsSnapshot — сервис → исходные DNS-серверы. Пустой список (или ["empty"])
	// означает «DNS не было»; BuildDNSRestore вернёт для такого сервиса `empty`.
	DnsSnapshot       map[string][]string `json:"dnsSnapshot"`
	PFToken           string              `json:"pfToken"`
	AnchorLoaded      bool                `json:"anchorLoaded"`
	ReconnectIntended bool                `json:"reconnectIntended"`
	StrictKillSwitch  bool                `json:"strictKillSwitch"`
}

// stateFileMode — права state-файла: только владелец (root) читает/пишет.
const stateFileMode os.FileMode = 0o600

// stateDirMode — права каталога state-файла.
const stateDirMode os.FileMode = 0o700

// WriteState атомарно записывает state по пути path. Сначала пишет во временный
// файл в том же каталоге, синкает, выставляет права и переименовывает поверх
// целевого — rename на одной ФС атомарен, поэтому читатель видит либо старую,
// либо новую версию, но не обрезанную. Каталог создаётся при необходимости.
//
// При ошибке временный файл удаляется, чтобы не копить мусор (инвариант теста:
// после успешной записи рядом нет temp-файлов).
func WriteState(path string, s *State) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, stateDirMode); err != nil {
		return fmt.Errorf("vpnmut: создать каталог state %q: %w", dir, err)
	}

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("vpnmut: сериализовать state: %w", err)
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("vpnmut: создать temp для state: %w", err)
	}
	tmpName := tmp.Name()

	// При любой ошибке ниже — закрыть и убрать temp; при успехе removeErr по
	// переименованному имени вернёт ErrNotExist, что безвредно.
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("vpnmut: записать temp state: %w", err)
	}
	if err := tmp.Chmod(stateFileMode); err != nil {
		cleanup()
		return fmt.Errorf("vpnmut: chmod temp state: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("vpnmut: fsync temp state: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("vpnmut: закрыть temp state: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("vpnmut: переименовать temp state в %q: %w", path, err)
	}
	return nil
}

// ReadState читает и разбирает state-файл. Отсутствующий файл — не паника:
// возвращается ошибка, которая оборачивает os.ErrNotExist, так что вызывающий
// отличит «файла нет» (errors.Is(err, os.ErrNotExist)) от битого содержимого.
// Для сценария «нет файла → считать систему чистой» есть LoadOrClean.
func ReadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		// os.ReadFile уже оборачивает ErrNotExist — пробрасываем как есть,
		// чтобы работал errors.Is.
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("vpnmut: разобрать state %q: %w", path, err)
	}
	return &s, nil
}

// LoadOrClean читает state, а при отсутствии файла возвращает «чистый» state
// (Phase == PhaseClean) без ошибки — разумный zero для старта демона на машине,
// где VPN ещё ни разу не поднимался. Прочие ошибки (битый JSON, нет прав)
// пробрасываются.
func LoadOrClean(path string) (*State, error) {
	s, err := ReadState(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{Phase: PhaseClean}, nil
		}
		return nil, err
	}
	return s, nil
}
