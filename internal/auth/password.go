// Package auth, kimlik doğrulama temellerini içerir: şifre hash'leme,
// JWT üretimi/doğrulaması ve tool API key yönetimi.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

var (
	ErrInvalidHash     = errors.New("şifre hash formatı geçersiz")
	ErrPasswordNoMatch = errors.New("şifre eşleşmiyor")
)

// Argon2Params, argon2id maliyet parametreleri.
//
// Neden argon2id, bcrypt değil? bcrypt yalnızca CPU-hard'dır; GPU/ASIC ile
// paralelleştirilebilir. argon2id ayrıca MEMORY-hard'dır: saldırganın her
// paralel denemesi için 64 MiB RAM ayırması gerekir, bu da GPU üzerinde
// kaba kuvvet saldırısını ekonomik olmaktan çıkarır. 2015 Password Hashing
// Competition kazananıdır ve OWASP'ın birinci önerisidir.
//
// Maliyet seçimi: OWASP'ın "m=64MiB, t=3, p=2" önerisi. Bu, modern bir CPU'da
// ~50-100 ms sürer — kullanıcı için fark edilmez, saldırgan için pahalıdır.
type Argon2Params struct {
	Memory      uint32 // KiB
	Iterations  uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultArgon2Params, OWASP önerisiyle uyumlu varsayılanlar.
func DefaultArgon2Params() Argon2Params {
	p := uint8(2)
	if n := runtime.NumCPU(); n < 2 {
		p = 1
	}
	return Argon2Params{
		Memory:      64 * 1024, // 64 MiB
		Iterations:  3,
		Parallelism: p,
		SaltLength:  16,
		KeyLength:   32,
	}
}

// HashPassword, şifreyi argon2id ile hash'ler ve PHC string formatında döner:
//
//	$argon2id$v=19$m=65536,t=3,p=2$<salt-b64>$<hash-b64>
//
// Parametreler hash'in içinde saklandığı için ileride maliyet artırıldığında
// eski hash'ler doğrulanmaya devam eder (kademeli geçiş mümkün).
func HashPassword(password string, params Argon2Params) (string, error) {
	if len(password) < 8 {
		return "", errors.New("şifre en az 8 karakter olmalı")
	}

	salt := make([]byte, params.SaltLength)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("salt üretilemedi: %w", err)
	}

	hash := argon2.IDKey([]byte(password), salt,
		params.Iterations, params.Memory, params.Parallelism, params.KeyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, params.Memory, params.Iterations, params.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword, PHC string'e karşı şifreyi sabit sürede doğrular.
func VerifyPassword(password, encodedHash string) error {
	params, salt, hash, err := decodeHash(encodedHash)
	if err != nil {
		return err
	}

	candidate := argon2.IDKey([]byte(password), salt,
		params.Iterations, params.Memory, params.Parallelism, params.KeyLength)

	if subtle.ConstantTimeCompare(hash, candidate) != 1 {
		return ErrPasswordNoMatch
	}
	return nil
}

func decodeHash(encoded string) (Argon2Params, []byte, []byte, error) {
	var p Argon2Params

	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return p, nil, nil, ErrInvalidHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	if version != argon2.Version {
		return p, nil, nil, fmt.Errorf("%w: desteklenmeyen argon2 sürümü %d", ErrInvalidHash, version)
	}

	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
		return p, nil, nil, ErrInvalidHash
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return p, nil, nil, ErrInvalidHash
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return p, nil, nil, ErrInvalidHash
	}

	// len() daima >= 0 ve base64 çözümlemesi sonucu birkaç on bayt; taşma yok.
	p.SaltLength = uint32(len(salt)) //nolint:gosec
	p.KeyLength = uint32(len(hash))  //nolint:gosec
	return p, salt, hash, nil
}
