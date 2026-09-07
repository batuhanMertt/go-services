// Command routing-engine, platformun KARAR MERKEZİDİR.
//
// Sorumluluğu tek cümleyle: kabul edilmiş bir event'in hangi subscriber'lara
// gideceğine abonelik ve yönlendirme kurallarına bakarak karar vermek ve her
// hedef için bağımsız bir teslimat görevi üretmek.
//
// Bu servis HTTP webhook ATMAZ. Karar verme ile teslimat bilinçli olarak
// ayrılmıştır: yavaş bir subscriber'ın karar verme hızını etkilememesi ve
// iki bileşenin bağımsız ölçeklenebilmesi için.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/mindmons/integration-hub/internal/httpx"
	"github.com/mindmons/integration-hub/internal/kafkax"
	"github.com/mindmons/integration-hub/internal/logging"
	"github.com/mindmons/integration-hub/internal/metrics"
	"github.com/mindmons/integration-hub/internal/postgres"
	"github.com/mindmons/integration-hub/internal/redisx"
)

type Server struct {
	cfg     Config
	log     *slog.Logger
	store   *Store
	cache   *RuleCache
	pool    *pgxpool.Pool
	rdb     *redis.Client
	metrics *metrics.Registry

	mRouted          *prometheus.CounterVec
	mTasks           *prometheus.CounterVec
	mRuleEvaluations *prometheus.CounterVec
	mConsumeErrors   *prometheus.CounterVec
	mPublishErrors   *prometheus.CounterVec
	mRoutingLatency  prometheus.Histogram
	mProcessDuration prometheus.Histogram
	mCacheSize       prometheus.Gauge
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "routing-engine başlatılamadı: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	logger := logging.New(cfg.Base.ServiceName, cfg.Base.Env, cfg.Base.LogLevel, cfg.Base.LogJSON)
	logging.SetDefault(logger)
	logger.Info("routing-engine başlıyor")

	ctx := context.Background()

	pool, err := postgres.Connect(ctx, postgres.Config{
		DSN:            cfg.Infra.DatabaseURL,
		MaxConns:       cfg.Infra.DBMaxConns,
		ConnectTimeout: cfg.Infra.DBConnectTimeout,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	rdb, err := redisx.Connect(ctx, cfg.Infra.Redis)
	if err != nil {
		// Kural önbelleği olmadan da yönlendirme çalışır: her event için
		// Postgres'e gidilir. Yavaşlar ama durmaz (NFR-1.2).
		logger.Warn("redis yok, kurallar doğrudan Postgres'ten okunacak",
			slog.String("err", err.Error()))
		rdb = nil
	} else {
		defer func() { _ = rdb.Close() }()
	}

	if err := kafkax.WaitForBrokers(ctx, cfg.Infra.KafkaBrokers, 90*time.Second); err != nil {
		return fmt.Errorf("kafka hazır değil: %w", err)
	}

	reg := metrics.New(cfg.Base.ServiceName)
	store := NewStore(pool)

	srv := &Server{
		cfg:     cfg,
		log:     logger,
		store:   store,
		cache:   NewRuleCache(store, rdb, logger, cfg.RuleCacheTTL, cfg.GenerationCheck),
		pool:    pool,
		rdb:     rdb,
		metrics: reg,
	}
	srv.registerMetrics(reg)

	health := httpx.NewHealth()
	health.Register("postgres", postgres.HealthCheck(pool))
	health.Register("kafka", kafkax.HealthCheck(cfg.Infra.KafkaBrokers))
	if rdb != nil {
		health.Register("redis", redisx.HealthCheck(rdb))
	}

	router := httpx.NewRouter(logger, reg, 0)
	httpx.MountObservability(router, reg, health)

	return httpx.RunWithWorkers(ctx, logger, cfg.Base, router,
		srv.runConsumer,
		srv.runMetricsUpdater,
	)
}

func (s *Server) registerMetrics(reg *metrics.Registry) {
	s.mRouted = reg.NewCounterVec("hub_routing_events_total",
		"Yönlendirilen event sayısı.", "result")
	s.mTasks = reg.NewCounterVec("hub_routing_tasks_created_total",
		"Üretilen teslimat görevi sayısı.", "result")
	s.mRuleEvaluations = reg.NewCounterVec("hub_routing_rule_evaluations_total",
		"Kural değerlendirme sonuçları.", "result")
	s.mConsumeErrors = reg.NewCounterVec("hub_kafka_consume_errors_total",
		"Kafka tüketim hataları.", "topic")
	s.mPublishErrors = reg.NewCounterVec("hub_kafka_publish_errors_total",
		"Kafka yayın hataları.", "topic")
	s.mCacheSize = reg.NewGauge("hub_routing_cache_entries",
		"Yerel kural önbelleğindeki event tipi sayısı.")

	routingLatency := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:        "hub_routing_latency_seconds",
		Help:        "Event kabulünden teslimat görevi üretimine kadar geçen süre.",
		Buckets:     []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		ConstLabels: reg.ConstLabels(),
	})
	processDuration := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:        "hub_routing_process_seconds",
		Help:        "Tek bir mesajın işlenme süresi.",
		Buckets:     []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.5, 1},
		ConstLabels: reg.ConstLabels(),
	})
	reg.MustRegister(routingLatency, processDuration)

	s.mRoutingLatency = routingLatency
	s.mProcessDuration = processDuration
}

func (s *Server) runMetricsUpdater(ctx context.Context) error {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.mCacheSize.Set(float64(s.cache.Size()))
		}
	}
}
