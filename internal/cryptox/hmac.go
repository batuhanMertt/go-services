// Package cryptox, platformun imza ve şifreleme temellerini içerir.
package cryptox

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrSignatureMismatch = errors.New("imza doğrulanamadı")
	ErrTimestampInvalid  = errors.New("zaman damgası geçersiz")
	ErrTimestampExpired  = errors.New("zaman damgası tolerans penceresi dışında")
)

// SigningBase, imzalanacak kanonik metni üretir: "<timestamp>.<body>".
//
// Neden gövdeye zaman damgası da karıştırılıyor? Yalnızca gövde imzalansaydı,
// saldırgan geçerli bir isteği yakalayıp sonsuza dek tekrar oynatabilirdi
// (replay attack). Zaman damgası imzanın parçası olduğunda, eski bir isteğin
// imzası tolerans penceresi dışında geçersiz hale gelir.
func SigningBase(timestamp string, body []byte) []byte {
	buf := make([]byte, 0, len(timestamp)+1+len(body))
	buf = append(buf, timestamp...)
	buf = append(buf, '.')
	buf = append(buf, body...)
	return buf
}

// Sign, HMAC-SHA256 imzasını hex olarak üretir.
func Sign(secret []byte, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(SigningBase(timestamp, body))
	return hex.EncodeToString(mac.Sum(nil))
}

// SignPrefixed, outbound webhook başlığı için "sha256=<hex>" biçimi üretir.
// GitHub/Stripe gibi yaygın webhook sağlayıcılarının konvansiyonuna uyar;
// ileride algoritma değişirse (sha512) başlık biçimi kırılmaz.
func SignPrefixed(secret []byte, timestamp string, body []byte) string {
	return "sha256=" + Sign(secret, timestamp, body)
}

// Verify, gelen imzayı SABİT SÜREDE karşılaştırır (NFR-3.1).
//
// Neden subtle.ConstantTimeCompare? Sıradan `==` karşılaştırması ilk farklı
// byte'ta döner; saldırgan yanıt süresini ölçerek imzayı byte byte tahmin
// edebilir (timing attack). ConstantTimeCompare her zaman tüm baytları okur.
func Verify(secret []byte, timestamp string, body []byte, provided string) error {
	expected := Sign(secret, timestamp, body)

	// "sha256=" öneki varsa temizle: hem çıplak hex hem önekli biçim kabul edilir.
	provided = strings.TrimSpace(provided)
	if idx := strings.IndexByte(provided, '='); idx > 0 && strings.HasPrefix(provided, "sha256=") {
		provided = provided[idx+1:]
	}

	if subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) != 1 {
		return ErrSignatureMismatch
	}
	return nil
}

// CheckTimestamp, zaman damgasının tolerans penceresi içinde olduğunu doğrular.
// Unix saniye (tam sayı) veya RFC3339 kabul edilir.
func CheckTimestamp(timestamp string, tolerance time.Duration, now time.Time) error {
	timestamp = strings.TrimSpace(timestamp)
	if timestamp == "" {
		return fmt.Errorf("%w: boş", ErrTimestampInvalid)
	}

	var ts time.Time
	if secs, err := strconv.ParseInt(timestamp, 10, 64); err == nil {
		ts = time.Unix(secs, 0)
	} else {
		parsed, perr := time.Parse(time.RFC3339, timestamp)
		if perr != nil {
			return fmt.Errorf("%w: unix saniye veya RFC3339 bekleniyor", ErrTimestampInvalid)
		}
		ts = parsed
	}

	diff := now.Sub(ts)
	if diff < 0 {
		diff = -diff
	}
	if diff > tolerance {
		return fmt.Errorf("%w: %s sapma (tolerans %s)", ErrTimestampExpired, diff.Truncate(time.Second), tolerance)
	}
	return nil
}

// VerifyRequest, imza + zaman damgası doğrulamasını tek adımda yapar.
func VerifyRequest(secret []byte, timestamp string, body []byte, provided string, tolerance time.Duration) error {
	if err := CheckTimestamp(timestamp, tolerance, time.Now()); err != nil {
		return err
	}
	return Verify(secret, timestamp, body, provided)
}

// NowTimestamp, imzalamada kullanılacak geçerli zaman damgasını üretir.
func NowTimestamp() string {
	return strconv.FormatInt(time.Now().Unix(), 10)
}
