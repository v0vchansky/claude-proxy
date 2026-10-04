// Package wgkey — вспомогательные преобразования ключей WireGuard/AmneziaWG.
// Ключи в конфигурации WireGuard задаются в base64 (32 байта), а uapi ждёт hex.
package wgkey

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"golang.org/x/crypto/curve25519"
)

// KeyLen — длина ключа Curve25519 в байтах.
const KeyLen = 32

// Generate создаёт новую пару ключей WireGuard/AmneziaWG и возвращает их в base64.
// Приватный ключ клэмпится по правилам Curve25519 (как в WireGuard).
func Generate() (privateB64, publicB64 string, err error) {
	var priv [KeyLen]byte
	if _, err = rand.Read(priv[:]); err != nil {
		return "", "", err
	}
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv[:]),
		base64.StdEncoding.EncodeToString(pub), nil
}

// DerivePublic выводит публичный ключ из приватного (оба base64).
func DerivePublic(privateB64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(privateB64)
	if err != nil {
		return "", fmt.Errorf("невалидный base64 приватного ключа: %w", err)
	}
	if len(raw) != KeyLen {
		return "", fmt.Errorf("ожидалось %d байт, получено %d", KeyLen, len(raw))
	}
	pub, err := curve25519.X25519(raw, curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// Base64ToHex декодирует 32-байтный ключ из base64 и возвращает его hex-представление.
func Base64ToHex(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", fmt.Errorf("невалидный base64 ключ: %w", err)
	}
	if len(raw) != KeyLen {
		return "", fmt.Errorf("ожидалось %d байт ключа, получено %d", KeyLen, len(raw))
	}
	return hex.EncodeToString(raw), nil
}
