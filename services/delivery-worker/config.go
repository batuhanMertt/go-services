package main

import (
	"time"

	"github.com/mindmons/integration-hub/internal/backoff"
	"github.com/mindmons/integration-hub/internal/breaker"
	"github.com/mindmons/integration-hub/internal/config"
)

type Config struct {
	Base  config.Base
	Infra config.Infra

	ConsumerGroup string
	// ConsumerCount, aynı consumer group'ta kaç okuyucu goroutine çalışacağı.
	// Kafka partition'ları bunlar arasında paylaştırır; partition sayısından
	// fazlası boşta bekler.
	ConsumerCount int

	// Outbound webhook
	HTTPTimeout       time.Duration
	WebhookSigningKey string

	// Retry
	Backoff    backoff.Policy
	MaxRetries int

	// Bulkhead
	MaxConcurrency   int
	MaxPerSubscriber int
	DeferDelay       time.Duration

	// Circuit breaker
	Breaker breaker.Config

	// Gecikmeli retry zamanlayıcısı
	SchedulerInterval time.Duration
	SchedulerBatch    int

	// Dağıtık kilit
	LockTTL time.Duration

	// Yanıt gövdesinden loglanacak maksimum karakter
	ResponseSnippetLen int
}

func loadConfig() (Config, error) {
	l := config.New()

	cfg := Config{
		Base:  l.LoadBase("delivery-worker", "DELIVERY_HTTP_PORT", 8083),
		Infra: l.LoadInfra(),

		ConsumerGroup: l.String("KAFKA_GROUP_DELIVERY", "delivery-worker"),
		ConsumerCount: l.Int("DELIVERY_CONSUMER_COUNT", 4),

		HTTPTimeout:       l.Duration("DELIVERY_HTTP_TIMEOUT", 10*time.Second),
		WebhookSigningKey: l.RequireMinLen("WEBHOOK_SIGNING_KEY", 16),

		Backoff: backoff.Policy{
			Base:   l.Duration("DELIVERY_BACKOFF_BASE", time.Second),
			Max:    l.Duration("DELIVERY_BACKOFF_MAX", 16*time.Second),
			Jitter: l.Float("DELIVERY_BACKOFF_JITTER", 0.2),
		},
		MaxRetries: l.Int("DELIVERY_MAX_RETRIES", 5),

		MaxConcurrency:   l.Int("DELIVERY_MAX_CONCURRENCY", 64),
		MaxPerSubscriber: l.Int("DELIVERY_MAX_PER_SUBSCRIBER", 8),
		DeferDelay:       l.Duration("DELIVERY_DEFER_DELAY", 2*time.Second),

		Breaker: breaker.Config{
			FailureThreshold: l.Int("BREAKER_FAILURE_THRESHOLD", 5),
			OpenDuration:     l.Duration("BREAKER_OPEN_DURATION", 30*time.Second),
			SuccessThreshold: l.Int("BREAKER_SUCCESS_THRESHOLD", 1),
		},

		SchedulerInterval: l.Duration("RETRY_SCHEDULER_INTERVAL", time.Second),
		SchedulerBatch:    l.Int("RETRY_SCHEDULER_BATCH", 100),

		LockTTL: l.Duration("DELIVERY_LOCK_TTL", 30*time.Second),

		ResponseSnippetLen: l.Int("DELIVERY_RESPONSE_SNIPPET_LEN", 512),
	}

	return cfg, l.Err()
}
