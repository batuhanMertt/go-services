// Package ratelimit, Redis üzerinde atomik token bucket algoritması uygular.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/mindmons/integration-hub/internal/redisx"
)

// tokenBucketScript, tüm kova mantığını TEK atomik adımda çalıştırır.
//
// Neden Lua? "Oku → hesapla → yaz" üç ayrı Redis komutu olsaydı, iki eşzamanlı
// istek arasında yarış oluşur ve limit aşılabilirdi (TOCTOU). Redis Lua
// script'leri tek iş parçacığında atomik çalışır; INCR + EXPIRE ikilisinden
// farklı olarak burada kısmi durum (sayaç var ama TTL yok) da oluşamaz.
//
// Neden sabit pencere (fixed window) değil? Sabit pencerede pencere sınırında
// 2× burst geçebilir (100 istek 00:59'da + 100 istek 01:00'da). Token bucket
// hem düzgün akış sağlar hem de kontrollü burst'e izin verir.
//
// Zaman kaynağı olarak Redis'in TIME komutu kullanılır: birden fazla servis
// instance'ının saatleri kaymış olsa bile limit tutarlı kalır.
const tokenBucketScript = `
local key       = KEYS[1]
local rate      = tonumber(ARGV[1])
local burst     = tonumber(ARGV[2])
local requested = tonumber(ARGV[3])

local t   = redis.call('TIME')
local now = tonumber(t[1]) + (tonumber(t[2]) / 1000000)

local data   = redis.call('HMGET', key, 'tokens', 'ts')
local tokens = tonumber(data[1])
local ts     = tonumber(data[2])

if tokens == nil or ts == nil then
  tokens = burst
  ts = now
end

local delta = now - ts
if delta < 0 then delta = 0 end

tokens = math.min(burst, tokens + delta * rate)

local allowed = 0
local retry   = 0.0

if tokens >= requested then
  tokens = tokens - requested
  allowed = 1
else
  retry = (requested - tokens) / rate
end

redis.call('HSET', key, 'tokens', tokens, 'ts', now)
-- Kova dolana kadar geçecek süre + emniyet payı: atıl anahtarlar kendiliğinden silinir.
local ttl = math.ceil(burst / rate) + 2
redis.call('EXPIRE', key, ttl)

return { allowed, tostring(tokens), tostring(retry) }
`

// Rule, bir kovanın doldurma hızı ve kapasitesi.
type Rule struct {
	// Rate: saniyede eklenen token sayısı (sürdürülebilir hız).
	Rate float64
	// Burst: kovanın kapasitesi (anlık patlama toleransı).
	Burst int
}

// Valid, kuralın anlamlı olup olmadığını söyler.
func (r Rule) Valid() bool { return r.Rate > 0 && r.Burst > 0 }

// Result, tek bir limit kontrolünün sonucu.
type Result struct {
	Allowed    bool
	Remaining  float64
	RetryAfter time.Duration
	// Degraded true ise Redis erişilemedi ve fail-open/closed politikası uygulandı.
	Degraded bool
}

// Limiter, Redis tabanlı token bucket sınırlayıcı.
type Limiter struct {
	rdb    *redis.Client
	script *redis.Script
	// FailClosed true ise Redis erişilemediğinde istek REDDEDİLİR.
	//
	// Varsayılan false (fail-open) bilinçli bir tercihtir: rate limit bir
	// güvenlik duvarı değil, kaynak koruma mekanizmasıdır. Redis düştüğünde
	// tüm platformu durdurmak, aşırı yüke izin vermekten daha büyük hasardır
	// (NFR-1.2 graceful degradation). Kritik ortamlarda env ile true yapılır.
	failClosed bool
}

func New(rdb *redis.Client, failClosed bool) *Limiter {
	return &Limiter{
		rdb:        rdb,
		script:     redis.NewScript(tokenBucketScript),
		failClosed: failClosed,
	}
}

// Allow, verilen kapsam ve kimlik için bir token tüketmeye çalışır.
// scope örn. "tool", "ip", "admin_user"; id örn. tool slug'ı veya IP.
func (l *Limiter) Allow(ctx context.Context, scope, id string, rule Rule) (Result, error) {
	return l.AllowN(ctx, scope, id, rule, 1)
}

// AllowN, n token tüketmeye çalışır.
func (l *Limiter) AllowN(ctx context.Context, scope, id string, rule Rule, n int) (Result, error) {
	if !rule.Valid() {
		// Kural tanımsızsa sınırlama yok — yanlış yapılandırma trafiği kesmesin.
		return Result{Allowed: true, Remaining: 0}, nil
	}
	if l == nil || l.rdb == nil {
		return l.degraded(), nil
	}

	key := redisx.Key(redisx.NSRateLimit, scope, id)

	raw, err := l.script.Run(ctx, l.rdb, []string{key},
		rule.Rate, rule.Burst, n).Slice()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Result{}, err
		}
		return l.degraded(), fmt.Errorf("ratelimit: redis script hatası: %w", err)
	}

	if len(raw) != 3 {
		return l.degraded(), fmt.Errorf("ratelimit: beklenmeyen script yanıtı: %v", raw)
	}

	allowed := toInt64(raw[0]) == 1
	remaining := toFloat(raw[1])
	retrySec := toFloat(raw[2])

	return Result{
		Allowed:    allowed,
		Remaining:  remaining,
		RetryAfter: time.Duration(retrySec * float64(time.Second)),
	}, nil
}

func (l *Limiter) degraded() Result {
	return Result{Allowed: !l.failClosed, Degraded: true}
}

// Reset, bir kovayı temizler (testler ve admin aksiyonları için).
func (l *Limiter) Reset(ctx context.Context, scope, id string) error {
	if l == nil || l.rdb == nil {
		return nil
	}
	return l.rdb.Del(ctx, redisx.Key(redisx.NSRateLimit, scope, id)).Err()
}

func toInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case string:
		n, _ := strconv.ParseInt(t, 10, 64)
		return n
	default:
		return 0
	}
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case int64:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	case float64:
		return t
	default:
		return 0
	}
}
