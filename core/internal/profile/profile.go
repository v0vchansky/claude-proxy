// Package profile описывает server profile и сборку uapi-конфигурации AmneziaWG.
package profile

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/v0vchansky/claude-proxy/core/internal/wgkey"
)

// Profile — один server profile. Поля соответствуют docs/control-protocol.md.
type Profile struct {
	ID                  string   `json:"id"`
	DisplayName         string   `json:"displayName"`
	Country             string   `json:"country"`
	Provider            string   `json:"provider"`
	Host                string   `json:"host"`
	Port                int      `json:"port"`
	ServerPublicKey     string   `json:"serverPublicKey"`
	ClientVpnAddress    string   `json:"clientVpnAddress"`
	ServerVpnAddress    string   `json:"serverVpnAddress"`
	DNS                 []string `json:"dns"`
	MTU                 int      `json:"mtu"`
	PersistentKeepalive int      `json:"persistentKeepalive"`

	Jc   int `json:"jc"`
	Jmin int `json:"jmin"`
	Jmax int `json:"jmax"`
	S1   int `json:"s1"`
	S2   int `json:"s2"`
	S3   int `json:"s3"`
	S4   int `json:"s4"`

	H1 uint32 `json:"h1"`
	H2 uint32 `json:"h2"`
	H3 uint32 `json:"h3"`
	H4 uint32 `json:"h4"`
}

// Validate проверяет поля, критичные для поднятия туннеля.
func (p Profile) Validate() error {
	if p.Host == "" {
		return fmt.Errorf("host пуст")
	}
	if p.Port <= 0 || p.Port > 65535 {
		return fmt.Errorf("некорректный port: %d", p.Port)
	}
	if _, err := wgkey.Base64ToHex(p.ServerPublicKey); err != nil {
		return fmt.Errorf("serverPublicKey: %w", err)
	}
	if _, err := p.ClientAddr(); err != nil {
		return fmt.Errorf("clientVpnAddress: %w", err)
	}
	for _, d := range p.DNS {
		if _, err := netip.ParseAddr(d); err != nil {
			return fmt.Errorf("dns %q: %w", d, err)
		}
	}
	return nil
}

// ClientAddr возвращает адрес клиента в туннеле (без маски).
func (p Profile) ClientAddr() (netip.Addr, error) {
	s := p.ClientVpnAddress
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	return netip.ParseAddr(s)
}

// DNSAddrs возвращает список DNS-серверов для netstack (с дефолтами, если пусто).
func (p Profile) DNSAddrs() []netip.Addr {
	src := p.DNS
	if len(src) == 0 {
		src = []string{"1.1.1.1", "8.8.8.8"}
	}
	out := make([]netip.Addr, 0, len(src))
	for _, d := range src {
		if a, err := netip.ParseAddr(d); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// MTUOrDefault возвращает MTU или 1420.
func (p Profile) MTUOrDefault() int {
	if p.MTU <= 0 {
		return 1420
	}
	return p.MTU
}

// Endpoint возвращает host:port для uapi.
func (p Profile) Endpoint() string {
	return fmt.Sprintf("%s:%d", p.Host, p.Port)
}

// BuildUAPI собирает конфигурацию для device.IpcSet.
// privateKeyB64 — приватный ключ клиента; в возвращаемую строку он попадает как hex,
// поэтому строку нельзя логировать.
func (p Profile) BuildUAPI(privateKeyB64 string) (string, error) {
	privHex, err := wgkey.Base64ToHex(privateKeyB64)
	if err != nil {
		return "", fmt.Errorf("privateKey: %w", err)
	}
	pubHex, err := wgkey.Base64ToHex(p.ServerPublicKey)
	if err != nil {
		return "", fmt.Errorf("serverPublicKey: %w", err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", privHex)
	// Параметры обфускации AmneziaWG — на уровне устройства, до peer.
	fmt.Fprintf(&b, "jc=%d\n", p.Jc)
	fmt.Fprintf(&b, "jmin=%d\n", p.Jmin)
	fmt.Fprintf(&b, "jmax=%d\n", p.Jmax)
	fmt.Fprintf(&b, "s1=%d\n", p.S1)
	fmt.Fprintf(&b, "s2=%d\n", p.S2)
	fmt.Fprintf(&b, "h1=%d\n", p.H1)
	fmt.Fprintf(&b, "h2=%d\n", p.H2)
	fmt.Fprintf(&b, "h3=%d\n", p.H3)
	fmt.Fprintf(&b, "h4=%d\n", p.H4)
	// Peer (сервер).
	fmt.Fprintf(&b, "public_key=%s\n", pubHex)
	fmt.Fprintf(&b, "endpoint=%s\n", p.Endpoint())
	fmt.Fprintf(&b, "allowed_ip=0.0.0.0/0\n")
	if p.PersistentKeepalive > 0 {
		fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", p.PersistentKeepalive)
	}
	return b.String(), nil
}
