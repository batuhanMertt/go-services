package main

import (
	"time"

	"github.com/mindmons/integration-hub/internal/config"
)

// Config, event-ingest servisinin ayarları.
type Config struct {
	Base  config.Base
	Infra config.Infra

	EncryptionKey string
	HMACTolerance time.Duration

	MaxBodyBytes int64

	RateTool       config.RateRule
	RateIP         config.RateRule
	RateFailClosed bool

	// Outbox poller
	OutboxPollInterval time.Duration
	OutboxBatchSize    int

	// API key ve şema önbelleği tazeleme aralıkları
	KeyCacheTTL      time.Duration
	SchemaRefresh    time.Duration
	SchemaRefreshMin time.Duration
}

func loadConfig() (Config, error) {
	l := config.New()

	cfg := Config{
		Base:  l.LoadBase("event-ingest", "INGEST_HTTP_PORT", 8081),
		Infra: l.LoadInfra(),

		EncryptionKey: l.RequireMinLen("SECRET_ENCRYPTION_KEY", 64),
		HMACTolerance: l.Duration("HMAC_TOLERANCE", 5*time.Minute),

		MaxBodyBytes: int64(l.Int("INGEST_MAX_BODY_BYTES", 1048576)),

		RateTool:       l.LoadRateRule("RATE_LIMIT_TOOL", 100, 200),
		RateIP:         l.LoadRateRule("RATE_LIMIT_IP", 200, 400),
		RateFailClosed: l.Bool("RATE_LIMIT_FAIL_CLOSED", false),

		OutboxPollInterval: l.Duration("OUTBOX_POLL_INTERVAL", 500*time.Millisecond),
		OutboxBatchSize:    l.Int("OUTBOX_BATCH_SIZE", 100),

		KeyCacheTTL:   l.Duration("INGEST_KEY_CACHE_TTL", 30*time.Second),
		SchemaRefresh: l.Duration("INGEST_SCHEMA_REFRESH", 30*time.Second),
	}

	return cfg, l.Err()
}
