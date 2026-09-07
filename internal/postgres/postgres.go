// Package postgres, pgxpool tabanlı bağlantı havuzu kurulumunu sağlar.
//
// ORM kullanılmama kararı (docs/03-tech-stack.md): Outbox'ta
// `FOR UPDATE SKIP LOCKED`, JSONB operatörleri, partial index'e uyan sorgular
// ve `ON CONFLICT DO NOTHING` gibi yapılar bu projenin merkezindedir. ORM bu
// sorguları ya gizler ya da raw SQL'e düşürür; bu durumda ORM'in getirdiği
// soyutlama net bir kayıptır. pgx doğrudan, tip güvenli ve prepared statement
// kullanır (SQL injection'a karşı doğal koruma).
package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config, bağlantı havuzu ayarları.
type Config struct {
	DSN string
	// MaxConns/MinConns `int` olarak alınır ve içeride güvenle sınırlandırılır.
	// pgx `int32` bekler; dönüşümü çağıranlara bırakmak, yapılandırmadan gelen
	// saçma bir değerin (negatif veya devasa) sessizce taşmasına yol açardı.
	MaxConns       int
	MinConns       int
	ConnectTimeout time.Duration
	// SearchPath, tüm sorguların hub şemasında çalışmasını sağlar.
	SearchPath string
}

// clampConns, yapılandırmadan gelen bağlantı sayısını makul aralığa çeker.
// Üst sınır Postgres'in varsayılan max_connections'ından (100-200) türetildi;
// tek bir servisin havuzu tüm sunucuyu tüketmemeli.
func clampConns(n, def int) int32 {
	const maxConns = 200

	if n <= 0 {
		n = def
	}
	switch {
	case n <= 0:
		return 4 // pgx varsayılanı da okunamadıysa güvenli taban
	case n > maxConns:
		return maxConns
	default:
		return int32(n) //nolint:gosec // yukarıdaki kontroller 1..200 aralığını garanti eder
	}
}

// Connect, havuzu kurar ve hazır olana kadar tekrar dener.
// Docker Compose'da `depends_on: service_healthy` çoğu durumu çözer ama
// Postgres'in "hazır" demesi ile bağlantı kabul etmesi arasında yarış vardır;
// bu yüzden uygulama seviyesinde de retry gerekir.
func Connect(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	if cfg.DSN == "" {
		return nil, fmt.Errorf("postgres: DSN boş")
	}

	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, fmt.Errorf("postgres: DSN çözümlenemedi: %w", err)
	}

	poolCfg.MaxConns = clampConns(cfg.MaxConns, int(poolCfg.MaxConns))
	if cfg.MinConns > 0 {
		poolCfg.MinConns = clampConns(cfg.MinConns, 0)
	}
	poolCfg.MaxConnLifetime = 30 * time.Minute
	poolCfg.MaxConnIdleTime = 5 * time.Minute
	poolCfg.HealthCheckPeriod = 30 * time.Second

	searchPath := cfg.SearchPath
	if searchPath == "" {
		searchPath = "hub, public"
	}
	if poolCfg.ConnConfig.RuntimeParams == nil {
		poolCfg.ConnConfig.RuntimeParams = map[string]string{}
	}
	poolCfg.ConnConfig.RuntimeParams["search_path"] = searchPath
	// Uzun süren sorgular havuzu tıkamasın (bulkhead niteliğinde koruma).
	poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = "30000"

	connectTimeout := cfg.ConnectTimeout
	if connectTimeout <= 0 {
		connectTimeout = 10 * time.Second
	}

	var pool *pgxpool.Pool
	var lastErr error
	backoff := 500 * time.Millisecond

	for attempt := 1; attempt <= 12; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, connectTimeout)
		pool, lastErr = pgxpool.NewWithConfig(attemptCtx, poolCfg)
		if lastErr == nil {
			lastErr = pool.Ping(attemptCtx)
		}
		cancel()

		if lastErr == nil {
			return pool, nil
		}
		if pool != nil {
			pool.Close()
			pool = nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}

	return nil, fmt.Errorf("postgres: bağlantı kurulamadı: %w", lastErr)
}

// BuildDSN, ayrık env değerlerinden bağlantı dizesi üretir.
// Servis başına farklı rol kullanıldığı için user/password parametrik.
func BuildDSN(user, password, host string, port int, dbName string) string {
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable",
		user, password, host, port, dbName)
}

// HealthCheck, /readyz için kullanılabilecek bir kontrol fonksiyonu döner.
func HealthCheck(pool *pgxpool.Pool) func(context.Context) error {
	return func(ctx context.Context) error {
		if pool == nil {
			return fmt.Errorf("postgres havuzu yok")
		}
		return pool.Ping(ctx)
	}
}
