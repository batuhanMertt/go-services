// Package ddos, hacimsel/L7 DDoS'a karşı uygulama katmanı savunmalarını toplar:
// eşzamanlı bağlantı sınırı, saldırı-modu tespiti ve IP itibar/ban sistemi.
//
// Bu paket 200 Gbit/s'lik hacimsel (L3/L4) bir saldırıyı DURDURAMAZ — o
// koruma upstream/scrubbing sağlayıcısının işidir, çünkü paketler VPS'e
// ulaşmadan önce uplink'i doldurabilir. Buradaki katmanlar, VPS'e ulaşan
// trafiğin backend'i (DB, CPU, bellek) tüketmesini önler: L7 request flood,
// slowloris tarzı bağlantı tüketimi ve brute-force/credential-stuffing gibi
// uygulama seviyesi saldırılar bu paketin hedefidir.
package ddos

import (
	"net/http"

	"github.com/mindmons/integration-hub/internal/httpx"
)

// ConnLimiter, işlenmekte olan istek sayısını sabit bir tavanla sınırlar.
//
// Neden gerekli? Redis tabanlı rate limiter (internal/ratelimit) bile bir
// network round-trip'tir; saldırgan yeterince paralel bağlantı açarsa CPU ve
// goroutine/bellek tüketimi rate limit kontrolünden ÖNCE patlar. ConnLimiter
// tamamen bellek içi bir semafordur, hiçbir I/O yapmaz — bu yüzden zincirin
// EN BAŞINDA çalışmalıdır.
type ConnLimiter struct {
	sem chan struct{}
}

// NewConnLimiter, en fazla max isteğin eşzamanlı işlenmesine izin veren bir
// limiter oluşturur. max <= 0 ise sınırlama devre dışı kalır (yanlış
// yapılandırma trafiği durdurmasın).
func NewConnLimiter(max int) *ConnLimiter {
	if max <= 0 {
		return &ConnLimiter{}
	}
	return &ConnLimiter{sem: make(chan struct{}, max)}
}

// InFlight, şu anda işlenmekte olan istek sayısını döner (test/gözlem için).
func (c *ConnLimiter) InFlight() int {
	if c == nil || c.sem == nil {
		return 0
	}
	return len(c.sem)
}

// Capacity, izin verilen eşzamanlı istek tavanını döner (0 = sınırsız).
func (c *ConnLimiter) Capacity() int {
	if c == nil {
		return 0
	}
	return cap(c.sem)
}

// TryAcquire, kapasitede yer varsa true döner ve bir slot tutar. Çağıran,
// true döndüğünde işi bitince Release çağırmalıdır.
func (c *ConnLimiter) TryAcquire() bool {
	if c == nil || c.sem == nil {
		return true
	}
	select {
	case c.sem <- struct{}{}:
		return true
	default:
		return false
	}
}

// Release, TryAcquire ile tutulan slotu geri verir.
func (c *ConnLimiter) Release() {
	if c == nil || c.sem == nil {
		return
	}
	select {
	case <-c.sem:
	default:
	}
}

// Middleware, kapasite dolduğunda 503 + Retry-After ile isteği hemen reddeder.
// Bilinçli olarak kuyuğa ALMAZ: kuyuğa almak, bağlantıları açık tutup
// slowloris tarzı tükenmeye zemin hazırlar. Hızlı reddetmek, hem istemciye
// (yeniden dene) hem backend'e (kaynak boşalır) daha iyi hizmet eder.
func (c *ConnLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !c.TryAcquire() {
			w.Header().Set("Retry-After", "1")
			httpx.Error(w, r, httpx.TooManyRequests("overloaded",
				"sunucu şu anda çok yüklü, lütfen kısa süre sonra tekrar deneyin"))
			return
		}
		defer c.Release()
		next.ServeHTTP(w, r)
	})
}
