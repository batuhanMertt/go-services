package config

import (
	"time"

	"github.com/mindmons/integration-hub/internal/redisx"
)

// Infra, altyapı bağımlılıklarının ortak yapılandırması.
//
// DATABASE_URL her servise ayrı verilir çünkü her servis KENDİ Postgres
// rolüyle bağlanır (least privilege — docs/08-threat-model.md). Böylece
// event-ingest, delivery_attempts tablosuna yazamaz; routing-engine
// api_keys tablosunu okuyamaz.
type Infra struct {
	DatabaseURL      string
	DBMaxConns       int
	DBConnectTimeout time.Duration

	Redis redisx.Config

	KafkaBrokers []string
	Topics       Topics
}

// LoadInfra, ortak altyapı ayarlarını okur.
func (l *Loader) LoadInfra() Infra {
	return Infra{
		DatabaseURL:      l.Require("DATABASE_URL"),
		DBMaxConns:       l.Int("DB_MAX_CONNS", 10),
		DBConnectTimeout: l.Duration("DB_CONNECT_TIMEOUT", 10*time.Second),

		Redis: redisx.Config{
			Host:     l.Require("REDIS_HOST"),
			Port:     l.Int("REDIS_PORT", 6379),
			Password: l.String("REDIS_PASSWORD", ""),
			DB:       l.Int("REDIS_DB", 0),
		},

		KafkaBrokers: l.CSV("KAFKA_BROKERS", nil),
		Topics:       l.LoadTopics(),
	}
}

// RateRule, env'den okunan bir rate limit kuralı.
type RateRule struct {
	Rate  float64
	Burst int
}

// LoadRateRule, "<PREFIX>_RPS" ve "<PREFIX>_BURST" değişkenlerini okur.
func (l *Loader) LoadRateRule(prefix string, defRate float64, defBurst int) RateRule {
	return RateRule{
		Rate:  l.Float(prefix+"_RPS", defRate),
		Burst: l.Int(prefix+"_BURST", defBurst),
	}
}
