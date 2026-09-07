// Package logging, tüm servislerde tek biçimli structured (JSON) log üretir.
//
// Neden stdlib log/slog? Go 1.21+ ile gelen slog, zap/zerolog kadar hızlı
// olmasa da sıfır bağımlılıkla JSON handler, seviye ve context desteği verir.
// Bir staj projesinde bağımlılık yüzeyini küçük tutmak, mikro-optimizasyondan
// daha değerlidir (NFR-4.2 yalnızca "JSON + trace_id" istiyor).
package logging

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

type ctxKey string

const traceIDKey ctxKey = "trace_id"

// WithTraceID, context'e trace id yerleştirir.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey, traceID)
}

// TraceIDFrom, context'teki trace id'yi döndürür (yoksa boş string).
func TraceIDFrom(ctx context.Context) string {
	if v, ok := ctx.Value(traceIDKey).(string); ok {
		return v
	}
	return ""
}

// traceHandler, her log kaydına context'teki trace_id'yi otomatik ekler.
// Böylece servis kodunda tek tek "trace_id" geçmek gerekmez ve Loki'de
// bir event'in tüm yolculuğu tek etiketle izlenebilir.
type traceHandler struct{ slog.Handler }

func (h traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if tid := TraceIDFrom(ctx); tid != "" {
		r.AddAttrs(slog.String("trace_id", tid))
	}
	return h.Handler.Handle(ctx, r)
}

func (h traceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return traceHandler{h.Handler.WithAttrs(attrs)}
}

func (h traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{h.Handler.WithGroup(name)}
}

func parseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// New, servis adı ve ortam etiketleri iliştirilmiş bir logger üretir.
// Çıktı daima stdout'tur; log dosyası yönetimi container runtime'ın işidir
// (12-factor). Log rotation docker-compose'daki json-file driver'ında.
func New(serviceName, env, level string, jsonFormat bool) *slog.Logger {
	opts := &slog.HandlerOptions{Level: parseLevel(level)}

	var h slog.Handler
	if jsonFormat {
		h = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		// Yalnızca yerel geliştirmede okunabilirlik için.
		h = slog.NewTextHandler(os.Stdout, opts)
	}

	logger := slog.New(traceHandler{h}).With(
		slog.String("service", serviceName),
		slog.String("env", env),
	)
	return logger
}

// SetDefault, üretilen logger'ı global default yapar (kütüphane logları için).
func SetDefault(l *slog.Logger) { slog.SetDefault(l) }
