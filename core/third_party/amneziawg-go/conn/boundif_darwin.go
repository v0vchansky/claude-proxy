//go:build darwin

/* SPDX-License-Identifier: MIT
 *
 * Привязка UDP-транспорта WG к физическому интерфейсу на macOS через IP_BOUND_IF.
 */

package conn

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// NewStdNetBindForInterface создаёт StdNetBind, чьи UDP-сокеты принудительно
// привязаны к интерфейсу с индексом ifIndex через IP_BOUND_IF (IPv4) и
// IPV6_BOUND_IF (IPv6).
//
// Зачем: на macOS исходящие датаграммы сокета с выставленным IP_BOUND_IF идут
// ЧЕРЕЗ указанный интерфейс, минуя таблицу маршрутов. Это позволяет держать
// WG-транспорт прокси всегда на физическом интерфейсе (en0/en1/…), даже когда
// сторонний/системный Полный VPN перехватывает default маршрутами 0.0.0.0/1 и
// 128.0.0.0/1 на свой utun. Без привязки датаграммы ушли бы в utun (петля
// WG-в-WG) и/или были бы срезаны. ifIndex получают через net.InterfaceByName.
//
// ifIndex <= 0 трактуется как «без привязки»: возвращается обычный NewStdNetBind.
func NewStdNetBindForInterface(ifIndex int) Bind {
	if ifIndex <= 0 {
		return NewStdNetBind()
	}
	b := NewStdNetBind().(*StdNetBind)
	b.extraControl = func(network, address string, c syscall.RawConn) error {
		var operr error
		if err := c.Control(func(fd uintptr) {
			switch network {
			case "udp6":
				operr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, ifIndex)
			default: // udp4 и прочие IPv4-сокеты
				operr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifIndex)
			}
		}); err != nil {
			return err
		}
		return operr
	}
	return b
}
