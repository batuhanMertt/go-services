package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/mindmons/integration-hub/internal/cryptox"
)

var (
	ErrAPIKeyMalformed = errors.New("api key biçimi geçersiz")
	ErrAPIKeyNoMatch   = errors.New("api key eşleşmiyor")
)

// APIKeyPrefixLen, prefix bölümündeki bayt sayısı (hex'te 2 katı karakter).
const apiKeyPrefixBytes = 6  // → 12 hex karakter
const apiKeySecretBytes = 24 // → 48 hex karakter, ~192 bit entropi

// GeneratedAPIKey, yeni üretilen bir anahtarın tüm parçalarını taşır.
type GeneratedAPIKey struct {
	// Plaintext YALNIZCA üretim anında istemciye döner, asla saklanmaz.
	Plaintext string
	// Prefix, DB'de düz metin saklanır ve lookup anahtarıdır.
	Prefix string
	// Hash, DB'de saklanan SHA-256 özeti.
	Hash []byte
	// HMACSecret, tool'un event imzalarken kullanacağı ham secret.
	// Yalnızca üretim anında döner; DB'de AES-GCM ile şifreli saklanır.
	HMACSecret string
}

// GenerateAPIKey, "ih_<prefix>_<secret>" biçiminde bir anahtar üretir.
//
// Neden prefix ayrı? Anahtar hash'lenerek saklandığı için "bu isteği hangi
// tool gönderdi" sorusunu DB'de aramanın yolu kalmaz — tüm satırları tek tek
// hash'leyip karşılaştırmak gerekirdi (O(n)). Prefix, hash'i açığa çıkarmayan
// ama tekil satırı bulmayı sağlayan bir indeks anahtarıdır (O(1) lookup).
// Aynı desen Stripe, GitHub ve AWS'de kullanılır.
//
// Neden anahtar için SHA-256, şifre için argon2? API key doğrulaması HER
// ingest isteğinde çalışır; argon2 (~80 ms) burada saniyede 12 isteklik bir
// tavan yaratırdı. Anahtar 192 bit rastgele üretildiği için sözlük saldırısına
// kapalıdır — yavaş hash'in koruduğu risk (zayıf parola) burada yoktur.
func GenerateAPIKey() (GeneratedAPIKey, error) {
	prefixHex, err := cryptox.RandomHex(apiKeyPrefixBytes)
	if err != nil {
		return GeneratedAPIKey{}, err
	}
	secretHex, err := cryptox.RandomHex(apiKeySecretBytes)
	if err != nil {
		return GeneratedAPIKey{}, err
	}
	hmacSecret, err := cryptox.RandomHex(32)
	if err != nil {
		return GeneratedAPIKey{}, err
	}

	prefix := "ih_" + prefixHex
	plaintext := prefix + "_" + secretHex

	return GeneratedAPIKey{
		Plaintext:  plaintext,
		Prefix:     prefix,
		Hash:       HashAPIKey(plaintext),
		HMACSecret: hmacSecret,
	}, nil
}

// HashAPIKey, tam anahtarın SHA-256 özetini döner.
func HashAPIKey(plaintext string) []byte {
	sum := sha256.Sum256([]byte(plaintext))
	return sum[:]
}

// ExtractPrefix, tam anahtardan lookup prefix'ini çıkarır.
func ExtractPrefix(plaintext string) (string, error) {
	plaintext = strings.TrimSpace(plaintext)
	parts := strings.Split(plaintext, "_")
	if len(parts) != 3 || parts[0] != "ih" {
		return "", fmt.Errorf("%w: ih_<prefix>_<secret> bekleniyor", ErrAPIKeyMalformed)
	}
	if len(parts[1]) != apiKeyPrefixBytes*2 || len(parts[2]) != apiKeySecretBytes*2 {
		return "", fmt.Errorf("%w: bölüm uzunlukları hatalı", ErrAPIKeyMalformed)
	}
	if _, err := hex.DecodeString(parts[1] + parts[2]); err != nil {
		return "", fmt.Errorf("%w: hex olmayan karakter", ErrAPIKeyMalformed)
	}
	return parts[0] + "_" + parts[1], nil
}

// VerifyAPIKey, verilen anahtarı saklanan hash'e karşı sabit sürede doğrular.
func VerifyAPIKey(plaintext string, storedHash []byte) error {
	candidate := HashAPIKey(plaintext)
	if subtle.ConstantTimeCompare(candidate, storedHash) != 1 {
		return ErrAPIKeyNoMatch
	}
	return nil
}
