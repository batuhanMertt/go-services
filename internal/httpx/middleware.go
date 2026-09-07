package httpx

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/google/uuid"

	"github.com/mindmons/integration-hub/internal/logging"
	"github.com/mindmons/integration-hub/internal/metrics"
)

// HeaderTraceID, servisler arası trace propagasyonunda kullanılan başlık.
const HeaderTraceID = "X-Trace-Id"

type ctxKey string

const traceCtxKey ctxKey = "httpx.trace_id"

// TraceIDFromRequest, isteğe atanmış trace id'yi döner.
func TraceIDFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}
	if v, ok := r.Context().Value(traceCtxKey).(string); ok {
		return v
	}
	return logging.TraceIDFrom(r.Context())
}

// TraceID middleware'i: gelen istekte trace id varsa onu sürdürür, yoksa
// üretir. Bu sayede publisher → ingest → routing → delivery zincirinin
// tamamı Loki'de tek bir trace_id ile aranabilir (NFR-4.2).
func TraceID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tid := r.Header.Get(HeaderTraceID)
		if tid == "" {
			tid = r.Header.Get("X-Request-Id")
		}
		if tid == "" {
			tid = uuid.NewString()
		}

		ctx := context.WithValue(r.Context(), traceCtxKey, tid)
		ctx = logging.WithTraceID(ctx, tid)
		w.Header().Set(HeaderTraceID, tid)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// statusRecorder, yanıt durum kodunu ve boyutunu yakalar.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
	}
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Flush, SSE (canlı event akışı) için gereklidir.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Recover, panic'i 500'e çevirir ve stack'i loglar; bir handler'daki hata
// tüm servisi düşürmez.
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					logger.ErrorContext(r.Context(), "panic yakalandı",
						slog.Any("panic", rec),
						slog.String("path", r.URL.Path),
						slog.String("method", r.Method),
					)
					Error(w, r, Internal("beklenmeyen bir hata oluştu", nil))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// RequestLogger, her isteği tek satır JSON olarak loglar.
// Sağlık ve metrik uçları gürültü yapmasın diye atlanır.
func RequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	skip := map[string]bool{"/healthz": true, "/readyz": true, "/metrics": true}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if skip[r.URL.Path] {
				next.ServeHTTP(w, r)
				return
			}

			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				rec.status = http.StatusOK
			}

			level := slog.LevelInfo
			switch {
			case rec.status >= 500:
				level = slog.LevelError
			case rec.status >= 400:
				level = slog.LevelWarn
			}

			logger.Log(r.Context(), level, "http istek",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rec.status),
				slog.Int("bytes", rec.bytes),
				slog.Float64("duration_ms", float64(time.Since(start).Microseconds())/1000.0),
				slog.String("remote_ip", ClientIP(r)),
			)
		})
	}
}

// Metrics, istek sayacı ve süre histogramını besler.
func Metrics(reg *metrics.Registry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}

			reg.IncInFlight()
			defer reg.DecInFlight()

			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w}
			next.ServeHTTP(rec, r)

			if rec.status == 0 {
				rec.status = http.StatusOK
			}

			// Route pattern (örn. /api/v1/tools/{id}) kullanılır; ham path
			// kullanmak yüksek kardinaliteli metrik patlamasına yol açardı.
			route := r.URL.Path
			if rc := chi.RouteContext(r.Context()); rc != nil && rc.RoutePattern() != "" {
				route = rc.RoutePattern()
			}

			reg.ObserveHTTP(r.Method, route, rec.status, time.Since(start).Seconds())
		})
	}
}

// MaxBody, istek gövdesini sınırlar (DoS ve bellek tüketimi koruması).
func MaxBody(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				Error(w, r, PayloadTooLarge("payload_too_large",
					"istek gövdesi izin verilen boyutu aşıyor"))
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders, temel tarayıcı güvenlik başlıklarını ekler.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

// CORS, whitelist tabanlı CORS uygular. `*` bilinçli olarak desteklenmez:
// kimlik bilgisi taşıyan isteklerde joker origin güvenlik açığıdır.
func CORS(allowedOrigins []string) func(http.Handler) http.Handler {
	return cors.Handler(cors.Options{
		AllowedOrigins:   allowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", HeaderTraceID, "X-Request-Id"},
		ExposedHeaders:   []string{HeaderTraceID},
		AllowCredentials: false, // Bearer token kullanıyoruz, cookie değil.
		MaxAge:           300,
	})
}

// ClientIP, ters proxy arkasındaki gerçek istemci IP'sini belirler.
// X-Forwarded-For yalnızca güvenilen proxy arkasında anlamlıdır; dev
// ortamında nginx bu başlığı set eder.
func ClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xr := r.Header.Get("X-Real-Ip"); xr != "" {
		return strings.TrimSpace(xr)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
