package ddos

import (
	"net/http"

	"github.com/mindmons/integration-hub/internal/httpx"
)

// BanGate, banlı IP'lerin isteklerini Redis'e tek bir EXISTS sorgusuyla
// reddeder. Zincirde ConnLimiter'dan SONRA, ama ratelimit.Limiter'dan (token
// bucket, Lua script) ÖNCE çalışmalıdır: EXISTS, Lua script'ten daha
// ucuzdur ve banlı bir IP'nin token bucket'ını meşgul etmesine gerek yok.
func BanGate(g *Guard) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := httpx.ClientIP(r)
			if g.Banned(r.Context(), ip) {
				w.Header().Set("Retry-After", "60")
				httpx.Error(w, r, httpx.Forbidden("ip_banned",
					"bu IP tekrarlayan kötü niyetli istekler nedeniyle geçici olarak engellendi"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Observe, her isteği Detector'a bildiren middleware. Zincirin EN BAŞINA
// (ConnLimiter'dan önce veya hemen sonra) konur: reddedilen istekler de
// sisteme yük bindirdiği için saldırı hacmine dahil edilmelidir.
func Observe(d *Detector) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d.Observe()
			next.ServeHTTP(w, r)
		})
	}
}
