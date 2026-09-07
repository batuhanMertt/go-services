package main

import (
	"time"

	"github.com/mindmons/integration-hub/internal/config"
)

type Config struct {
	Base  config.Base
	Infra config.Infra

	ConsumerGroup string

	// Replay kuyruğu tarama aralığı ve tek turda alınacak kayıt sayısı
	ReplayInterval time.Duration
	ReplayBatch    int

	// Retention (gün)
	DLQRetentionDays     int
	EventRetentionDays   int
	AttemptRetentionDays int
	RetentionInterval    time.Duration
	// Tek turda silinecek maksimum satır — uzun kilitleri önler
	RetentionBatch int

	// Bu eşiğin üzerinde bekleyen DLQ kaydı varsa alert metriği yükselir
	AlertThreshold int
}

func loadConfig() (Config, error) {
	l := config.New()

	cfg := Config{
		Base:  l.LoadBase("dlq-handler", "DLQ_HTTP_PORT", 8084),
		Infra: l.LoadInfra(),

		ConsumerGroup: l.String("KAFKA_GROUP_DLQ", "dlq-handler"),

		ReplayInterval: l.Duration("DLQ_REPLAY_INTERVAL", 2*time.Second),
		ReplayBatch:    l.Int("DLQ_REPLAY_BATCH", 50),

		DLQRetentionDays:     l.Int("DLQ_RETENTION_DAYS", 30),
		EventRetentionDays:   l.Int("EVENT_RETENTION_DAYS", 14),
		AttemptRetentionDays: l.Int("ATTEMPT_RETENTION_DAYS", 14),
		RetentionInterval:    l.Duration("RETENTION_JOB_INTERVAL", time.Hour),
		RetentionBatch:       l.Int("RETENTION_BATCH", 5000),

		AlertThreshold: l.Int("DLQ_ALERT_THRESHOLD", 10),
	}

	return cfg, l.Err()
}
