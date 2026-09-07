package cryptox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

var ErrInvalidCiphertext = errors.New("şifreli metin geçersiz veya bozulmuş")

// Sealer, tool HMAC secret'larını veritabanında şifreli saklamak için
// AES-256-GCM zarf şifrelemesi uygular.
//
// Neden hash değil de şifreleme? API key'i doğrularken yalnızca "eşleşiyor mu"
// sorusunu yanıtlarız → hash yeterli. Ancak HMAC secret'ı ile imza HESAPLAMAK
// zorundayız → ham değere ihtiyaç var. Bu yüzden secret hash'lenemez,
// şifrelenir. Anahtar SECRET_ENCRYPTION_KEY ile runtime'da verilir ve DB
// yedeği çalınsa bile secret'lar kullanılamaz.
//
// GCM seçimi: AEAD modu olduğu için hem gizlilik hem bütünlük sağlar;
// CBC gibi modlarda ayrıca MAC eklemek gerekirdi (padding oracle riski).
type Sealer struct {
	aead cipher.AEAD
}

// NewSealer, 64 hex karakter (32 byte) uzunluğundaki anahtardan sealer üretir.
func NewSealer(hexKey string) (*Sealer, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("SECRET_ENCRYPTION_KEY hex olmalı: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("SECRET_ENCRYPTION_KEY 32 byte (64 hex karakter) olmalı, alınan: %d byte", len(key))
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes cipher oluşturulamadı: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm oluşturulamadı: %w", err)
	}

	return &Sealer{aead: aead}, nil
}

// Seal, düz metni şifreler. Çıktı biçimi: nonce || ciphertext || tag.
// Her çağrıda yeni rastgele nonce üretilir (GCM'de nonce tekrarı felakettir).
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("nonce üretilemedi: %w", err)
	}
	return s.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open, Seal ile üretilmiş veriyi çözer.
func (s *Sealer) Open(sealed []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(sealed) < ns+s.aead.Overhead() {
		return nil, ErrInvalidCiphertext
	}
	nonce, ct := sealed[:ns], sealed[ns:]

	plaintext, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return plaintext, nil
}

// RandomBytes, kriptografik güvenli rastgele bayt üretir.
func RandomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return nil, fmt.Errorf("rastgele bayt üretilemedi: %w", err)
	}
	return b, nil
}

// RandomHex, n bayt rastgele veriden hex string üretir.
func RandomHex(n int) (string, error) {
	b, err := RandomBytes(n)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
