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

// State — снимок состояния для UI.
type State struct {
	State              ConnState `json:"state"`
	ProfileID          string    `json:"profileId"`
	ServerName         string    `json:"serverName"`
	ServerHost         string    `json:"serverHost"`
	ServerPort         int       `json:"serverPort"`
	LocalProxy         string    `json:"localProxy"`
	PingMs             int       `json:"pingMs"`
	LastCheckUnix      int64     `json:"lastCheckUnix"`
	ConnectedSinceUnix int64     `json:"connectedSinceUnix"`
	LastHandshakeUnix  int64     `json:"lastHandshakeUnix"`
	RxBytes            int64     `json:"rxBytes"`
	TxBytes            int64     `json:"txBytes"`
	LastError          string    `json:"lastError"`
}
