package cryptox

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" // 32 byte

func TestNewSealerValidatesKeyLength(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		wantErr string
	}{
		{"geçerli 32 byte", testKey, ""},
		{"çok kısa", "00112233", "32 byte"},
		{"hex değil", strings.Repeat("z", 64), "hex olmalı"},
		{"boş", "", "32 byte"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewSealer(tc.key)

			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("hata beklenmiyordu: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("hata bekleniyordu")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("hata mesajı %q içermeliydi: %v", tc.wantErr, err)
			}
		})
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := NewSealer(testKey)
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("4f3e2d1c0b9a8f7e6d5c4b3a29180716")

	sealed, err := s.Seal(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, plaintext) {
		t.Fatal("şifreli metin düz metni içeriyor — şifreleme çalışmıyor")
	}

	opened, err := s.Open(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("çözülen veri farklı: %q != %q", opened, plaintext)
	}
}

// GCM'de nonce tekrarı felakettir; her Seal çağrısı farklı çıktı vermelidir.
func TestSealUsesFreshNonce(t *testing.T) {
	s, _ := NewSealer(testKey)
	plaintext := []byte("aynı düz metin")

	a, _ := s.Seal(plaintext)
	b, _ := s.Seal(plaintext)

	if bytes.Equal(a, b) {
		t.Fatal("aynı düz metin aynı şifreli metni üretti — nonce tekrar kullanılıyor")
	}
}

func TestOpenRejectsTampered(t *testing.T) {
	s, _ := NewSealer(testKey)
	sealed, _ := s.Seal([]byte("gizli"))

	tests := []struct {
		name string
		data []byte
	}{
		{"boş", nil},
		{"çok kısa", []byte{1, 2, 3}},
		{"son byte değiştirilmiş", append(append([]byte{}, sealed[:len(sealed)-1]...), sealed[len(sealed)-1]^0xFF)},
		{"nonce değiştirilmiş", append([]byte{sealed[0] ^ 0xFF}, sealed[1:]...)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Open(tc.data); !errors.Is(err, ErrInvalidCiphertext) {
				t.Fatalf("beklenen ErrInvalidCiphertext, alınan: %v", err)
			}
		})
	}
}

func TestOpenRejectsWrongKey(t *testing.T) {
	a, _ := NewSealer(testKey)
	b, _ := NewSealer("fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")

	sealed, _ := a.Seal([]byte("gizli"))

	if _, err := b.Open(sealed); !errors.Is(err, ErrInvalidCiphertext) {
		t.Fatalf("yanlış anahtar çözebildi — bu ciddi bir hata olurdu: %v", err)
	}
}

func TestRandomHexLength(t *testing.T) {
	h, err := RandomHex(16)
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 32 {
		t.Fatalf("16 byte 32 hex karakter olmalı, alınan: %d", len(h))
	}

	other, _ := RandomHex(16)
	if h == other {
		t.Fatal("iki çağrı aynı değeri üretti — rastgelelik yok")
	}
}
