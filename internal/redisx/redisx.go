// Package redisx, Redis istemcisinin ortak kurulumunu sağlar.
//
// Redis bu platformda 5 ayrı işi görür (docs/05-redis-usage.md):
// rate limit sayaçları, dağıtık kilit, routing kural cache'i, circuit breaker
// durumu ve idempotency yanıt cache'i. Hiçbiri "tek doğruluk kaynağı" değildir
// — hepsinin arkasında Postgres vardır; bu bilinçli bir SPOF azaltmasıdır.
package redisx

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

type Config struct {
	Host     string
	Port     int
	Password string
	DB       int
	// PoolSize 0 ise go-redis varsayılanı (10 × GOMAXPROCS) kullanılır.
	PoolSize int
}

// Connect, istemciyi kurar ve PING ile doğrular.
func Connect(ctx context.Context, cfg Config) (*redis.Client, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("redis: host boş")
	}

	client := redis.NewClient(&redis.Options{
		Addr:            fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Password:        cfg.Password,
		DB:              cfg.DB,
		PoolSize:        cfg.PoolSize,
		DialTimeout:     5 * time.Second,
		ReadTimeout:     3 * time.Second,
		WriteTimeout:    3 * time.Second,
		ConnMaxIdleTime: 5 * time.Minute,
		MaxRetries:      2,
	})

	var lastErr error
	backoff := 300 * time.Millisecond

	for attempt := 1; attempt <= 10; attempt++ {
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		lastErr = client.Ping(pingCtx).Err()
		cancel()

		if lastErr == nil {
			return client, nil
		}

		select {
		case <-ctx.Done():
			_ = client.Close()
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 3*time.Second {
			backoff *= 2
		}
	}

	_ = client.Close()
	return nil, fmt.Errorf("redis: bağlantı kurulamadı: %w", lastErr)
}

// HealthCheck, /readyz için kontrol fonksiyonu döner.
func HealthCheck(client *redis.Client) func(context.Context) error {
	return func(ctx context.Context) error {
		if client == nil {
			return fmt.Errorf("redis istemcisi yok")
		}
		return client.Ping(ctx).Err()
	}
}

// Anahtar isim alanları — çakışmayı ve yanlışlıkla silmeyi önler.
const (
	NSRateLimit = "rl"      // rl:{scope}:{id}
	NSLock      = "lock"    // lock:delivery:{task_id}
	NSRules     = "rules"   // rules:{event_type}
	NSBreaker   = "breaker" // breaker:{subscription_id}
	NSIdem      = "idem"    // idem:{event_id}
	NSGuard     = "guard"   // guard:{strikes|ban}:{ip}
)

// Key, isim alanlı bir Redis anahtarı üretir.
func Key(namespace string, parts ...string) string {
	k := namespace
	for _, p := range parts {
		k += ":" + p
	}
	return k
}
