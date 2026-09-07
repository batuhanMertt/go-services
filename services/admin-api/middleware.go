package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/mindmons/integration-hub/internal/auth"
	"github.com/mindmons/integration-hub/internal/httpx"
	"github.com/mindmons/integration-hub/internal/ratelimit"
)

type ctxKey string

const userCtxKey ctxKey = "admin.user"

// userFrom, context'teki doğrulanmış kullanıcıyı döner.
func userFrom(ctx context.Context) *auth.Claims {
	if v, ok := ctx.Value(userCtxKey).(*auth.Claims); ok {
		return v
	}
	return nil
}

// requireAuth, `Authorization: Bearer <token>` başlığını doğrular (FR-6.1).
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if header == "" {
			s.mAuthFailures.Inc("missing_header")
			httpx.Error(w, r, httpx.Unauthorized("missing_token", "Authorization başlığı gerekli"))
			return
		}

		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			s.mAuthFailures.Inc("malformed_header")
			httpx.Error(w, r, httpx.Unauthorized("invalid_token", "Bearer şeması bekleniyor"))
			return
		}

		claims, err := s.tokens.ParseAccess(strings.TrimSpace(parts[1]))
		if err != nil {
			switch {
			case errors.Is(err, auth.ErrTokenExpired):
				s.mAuthFailures.Inc("expired")
				httpx.Error(w, r, httpx.Unauthorized("token_expired", "access token süresi dolmuş"))
			default:
				s.mAuthFailures.Inc("invalid")
				httpx.Error(w, r, httpx.Unauthorized("invalid_token", "token doğrulanamadı"))
			}
			return
		}

		ctx := context.WithValue(r.Context(), userCtxKey, claims)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireRole, rol bazlı yetkilendirme (şimdilik yalnız admin rolü kullanılıyor,
// ancak tasarım genişlemeye açık: operator/viewer rolleri şemada mevcut).
func (s *Server) requireRole(roles ...string) func(http.Handler) http.Handler {
	allowed := make(map[string]bool, len(roles))
	for _, r := range roles {
		allowed[r] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user := userFrom(r.Context())
			if user == nil || !allowed[user.Role] {
				httpx.Error(w, r, httpx.Forbidden("insufficient_role", "bu işlem için yetkiniz yok"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// rateLimitByIP, kimlik doğrulanmamış uçları IP başına sınırlar.
// Brute-force login denemelerine karşı ilk savunma hattıdır.
func (s *Server) rateLimitByIP(next http.Handler) http.Handler {
	return s.rateLimit(next, "admin_ip", func(r *http.Request) string {
		return httpx.ClientIP(r)
	})
}

// rateLimitByUser, kimliği doğrulanmış uçları kullanıcı başına sınırlar.
func (s *Server) rateLimitByUser(next http.Handler) http.Handler {
	return s.rateLimit(next, "admin_user", func(r *http.Request) string {
		if u := userFrom(r.Context()); u != nil {
			return u.UserID
		}
		return httpx.ClientIP(r)
	})
}

func (s *Server) rateLimit(next http.Handler, scope string, identify func(*http.Request) string) http.Handler {
	rule := ratelimit.Rule{Rate: s.cfg.RateAdmin.Rate, Burst: s.cfg.RateAdmin.Burst}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := identify(r)

		res, err := s.limiter.Allow(r.Context(), scope, id, rule)
		if err != nil {
			// Redis hatası: politikaya göre geç veya reddet; her durumda logla.
			s.log.WarnContext(r.Context(), "rate limit kontrolü başarısız",
				"scope", scope, "err", err.Error(), "degraded", res.Degraded)
		}

		if !res.Allowed {
			retry := int(res.RetryAfter.Seconds())
			if retry < 1 {
				retry = 1
			}
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			httpx.Error(w, r, httpx.TooManyRequests("rate_limited",
				"çok fazla istek gönderildi, lütfen bekleyin"))
			return
		}

		w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(int(res.Remaining)))
		next.ServeHTTP(w, r)
	})
}
