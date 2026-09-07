// Package backoff, yeniden deneme gecikmelerini hesaplar.
package backoff

import (
	"math"
	"math/rand"
	"net/http"
	"time"
)

// Policy, exponential backoff parametreleri.
type Policy struct {
	Base   time.Duration // ilk gecikme (örn. 1s)
	Max    time.Duration // üst sınır (örn. 16s)
	Jitter float64       // 0.0-1.0 arası rastgelelik oranı
}

// DefaultPolicy, FR-4.3'ün istediği 1s, 2s, 4s, 8s, 16s dizisini üretir.
func DefaultPolicy() Policy {
	return Policy{Base: time.Second, Max: 16 * time.Second, Jitter: 0.2}
}

// Delay, verilen deneme numarası için bekleme süresini hesaplar.
// attempt 1 → Base, 2 → 2×Base, 3 → 4×Base … Max ile sınırlanır.
//
// Neden jitter? Aynı anda başarısız olan yüzlerce teslimat, jitter olmadan
// tam olarak aynı saniyede yeniden denenir ve hedef sistemi ikinci kez
// çökertir ("thundering herd"). Jitter, denemeleri zamana yayar.
func (p Policy) Delay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := p.Base
	if base <= 0 {
		base = time.Second
	}
	maxDelay := p.Max
	if maxDelay <= 0 {
		maxDelay = 16 * time.Second
	}

	// 2^(attempt-1) — taşmayı önlemek için üs sınırlanır.
	exp := math.Pow(2, float64(attempt-1))
	if exp > 1e6 {
		exp = 1e6
	}

	delay := time.Duration(float64(base) * exp)
	if delay > maxDelay || delay <= 0 {
		delay = maxDelay
	}

	if p.Jitter > 0 {
		j := p.Jitter
		if j > 1 {
			j = 1
		}
		// [-j, +j] aralığında oransal sapma.
		factor := 1 + (rand.Float64()*2-1)*j //nolint:gosec // kriptografik gizlilik gerekmiyor
		delay = time.Duration(float64(delay) * factor)
		if delay < 0 {
			delay = base
		}
		if delay > maxDelay*2 {
			delay = maxDelay * 2
		}
	}

	return delay
}

// Schedule, bir politikanın ilk n gecikmesini döner (dokümantasyon/test için).
func (p Policy) Schedule(n int) []time.Duration {
	out := make([]time.Duration, 0, n)
	noJitter := Policy{Base: p.Base, Max: p.Max, Jitter: 0}
	for i := 1; i <= n; i++ {
		out = append(out, noJitter.Delay(i))
	}
	return out
}

// IsRetryable, bir HTTP durum kodunun yeniden denenmeye değer olup olmadığını
// söyler (FR-4.3).
//
// Kural: 5xx sunucu geçici olarak bozulmuştur → dene. 429 hız sınırıdır →
// bekleyip dene. Diğer 4xx'ler İSTEĞİN KENDİSİ hatalıdır (400 bozuk gövde,
// 401 yanlış imza, 404 yanlış URL) → tekrar denemek aynı sonucu verir,
// sadece kaynak yakar; doğrudan DLQ'ya gitmeli.
// 408 (Request Timeout) ve 425 (Too Early) istisnadır: geçicidirler.
func IsRetryable(statusCode int) bool {
	switch {
	case statusCode >= 500:
		return true
	case statusCode == http.StatusTooManyRequests: // 429
		return true
	case statusCode == http.StatusRequestTimeout: // 408
		return true
	case statusCode == http.StatusTooEarly: // 425
		return true
	default:
		return false
	}
}

// StatusClass, metrik etiketi için "2xx", "4xx" gibi sınıf döner.
func StatusClass(statusCode int) string {
	switch {
	case statusCode <= 0:
		return "none"
	case statusCode < 200:
		return "1xx"
	case statusCode < 300:
		return "2xx"
	case statusCode < 400:
		return "3xx"
	case statusCode < 500:
		return "4xx"
	default:
		return "5xx"
	}
}
