package control

// ConnState — состояние подключения (см. docs/control-protocol.md).
type ConnState string

const (
	StateDisconnected ConnState = "disconnected"
	StateConnecting   ConnState = "connecting"
	StateConnected    ConnState = "connected"
	StateSwitching    ConnState = "switching"
	StateError        ConnState = "error"
)

// ForwardMode — стратегия dial постоянного локального прокси 8118. Listener живёт
// всё время работы ядра, а режим определяет, куда идёт исходящее соединение
// (см. docs/control-protocol.md).
type ForwardMode string

const (
	// ForwardTunnel — dial через активный WG-туннель proxy-ядра. Нет туннеля → ошибка
	// (fail-closed): прямого выхода в системную сеть не появляется.
	ForwardTunnel ForwardMode = "tunnel"
	// ForwardDirect — прямой net.Dial. Безопасен ТОЛЬКО когда активен Полный VPN
	// (системный utun + PF kill-switch заворачивают/блокируют трафик). Ставит его
	// только приложение явной командой forward на время Полного VPN.
	ForwardDirect ForwardMode = "direct"
	// ForwardOff — все соединения отклоняются (fail-closed, ничего не активно).
	ForwardOff ForwardMode = "off"
)

// State — снимок состояния для UI.
type State struct {
	State              ConnState   `json:"state"`
	ProfileID          string      `json:"profileId"`
	ServerName         string      `json:"serverName"`
	ServerHost         string      `json:"serverHost"`
	ServerPort         int         `json:"serverPort"`
	LocalProxy         string      `json:"localProxy"`
	ForwardMode        ForwardMode `json:"forwardMode"`
	PingMs             int         `json:"pingMs"`
	LastCheckUnix      int64       `json:"lastCheckUnix"`
	ConnectedSinceUnix int64       `json:"connectedSinceUnix"`
	LastHandshakeUnix  int64       `json:"lastHandshakeUnix"`
	RxBytes            int64       `json:"rxBytes"`
	TxBytes            int64       `json:"txBytes"`
	LastError          string      `json:"lastError"`
}
