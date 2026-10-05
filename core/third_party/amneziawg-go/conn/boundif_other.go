//go:build !darwin

/* SPDX-License-Identifier: MIT
 *
 * Заглушка привязки к интерфейсу для не-darwin: IP_BOUND_IF — механизм macOS.
 */

package conn

// NewStdNetBindForInterface на не-darwin возвращает обычный Bind без привязки к
// интерфейсу: IP_BOUND_IF — механизм macOS, а приложение таргетится на macOS
// (utun/pfctl/networksetup). Заглушка нужна лишь ради кросс-компиляции —
// в бою эта ветка не исполняется.
func NewStdNetBindForInterface(ifIndex int) Bind {
	_ = ifIndex
	return NewStdNetBind()
}
