package main

import (
	"time"

	"github.com/mindmons/integration-hub/internal/config"
)

type Config struct {
	Base  config.Base
	Infra config.Infra

	ConsumerGroup string

	// Kural önbelleği
	RuleCacheTTL time.Duration
	// Redis'teki "generation" sayacının kontrol sıklığı — admin bir kuralı
	// değiştirdiğinde en fazla bu süre kadar gecikmeyle yansır.
	GenerationCheck time.Duration

	KafkaFetchMaxBytes int

	// Kafka publish edilemeyen teslimat görevlerinin retry zamanlayıcı
	// tarafından kurtarılması için verilen gecikme.
	RecoveryDelay time.Duration
}

func loadConfig() (Config, error) {
	l := config.New()

	cfg := Config{
		Base:  l.LoadBase("routing-engine", "ROUTING_HTTP_PORT", 8082),
		Infra: l.LoadInfra(),

		ConsumerGroup: l.String("KAFKA_GROUP_ROUTING", "routing-engine"),

		RuleCacheTTL:    l.Duration("ROUTING_RULE_CACHE_TTL", 60*time.Second),
		GenerationCheck: l.Duration("ROUTING_GENERATION_CHECK", 2*time.Second),

		KafkaFetchMaxBytes: l.Int("KAFKA_FETCH_MAX_BYTES", 10485760),

		RecoveryDelay: l.Duration("ROUTING_RECOVERY_DELAY", 30*time.Second),
	}

	return cfg, l.Err()
}
