package wgkey

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestGenerateProducesDistinctValidKeys(t *testing.T) {
	priv, pub, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, k := range []string{priv, pub} {
		raw, err := base64.StdEncoding.DecodeString(k)
		if err != nil {
			t.Fatalf("ключ не base64: %v", err)
		}
		if len(raw) != KeyLen {
			t.Fatalf("длина ключа %d, ожидалось %d", len(raw), KeyLen)
		}
	}
	if priv == pub {
		t.Fatal("приватный и публичный ключи совпали")
	}

	priv2, _, _ := Generate()
	if priv == priv2 {
		t.Fatal("два вызова Generate дали одинаковый приватный ключ")
	}
}

func TestDerivePublicMatchesGenerate(t *testing.T) {
	priv, pub, err := Generate()
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	got, err := DerivePublic(priv)
	if err != nil {
		t.Fatalf("DerivePublic: %v", err)
	}
	if got != pub {
		t.Fatalf("DerivePublic=%s, ожидалось %s", got, pub)
	}
}

func TestDerivePublicRejectsBadInput(t *testing.T) {
	if _, err := DerivePublic("не-base64!!!"); err == nil {
		t.Fatal("ожидалась ошибка на невалидный base64")
	}
	short := base64.StdEncoding.EncodeToString([]byte("short"))
	if _, err := DerivePublic(short); err == nil {
		t.Fatal("ожидалась ошибка на ключ неправильной длины")
	}
}

func TestBase64ToHex(t *testing.T) {
	// 32 нулевых байта -> 64 нуля в hex.
	zero := base64.StdEncoding.EncodeToString(make([]byte, KeyLen))
	hexStr, err := Base64ToHex(zero)
	if err != nil {
		t.Fatalf("Base64ToHex: %v", err)
	}
	if hexStr != strings.Repeat("0", 64) {
		t.Fatalf("hex=%q", hexStr)
	}

	if _, err := Base64ToHex("@@@@"); err == nil {
		t.Fatal("ожидалась ошибка на мусор")
	}
	if _, err := Base64ToHex(base64.StdEncoding.EncodeToString([]byte{1, 2, 3})); err == nil {
		t.Fatal("ожидалась ошибка на короткий ключ")
	}
}
