// Command dlq-handler, platformun KARANTİNA VE KURTARMA SERVİSİDİR.
//
// Sorumluluğu tek cümleyle: teslim edilemeyen mesajları kalıcı ve sorgulanabilir
// hale getirmek, admin talebiyle yeniden yayınlamak ve saklama politikasını
// uygulamak.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/mindmons/integration-hub/internal/httpx"
	"github.com/mindmons/integration-hub/internal/kafkax"
	"github.com/mindmons/integration-hub/internal/logging"
	"github.com/mindmons/integration-hub/internal/metrics"
	"github.com/mindmons/integration-hub/internal/postgres"
)

type Server struct {
	cfg     Config
	log     *slog.Logger
	store   *Store
	pool    *pgxpool.Pool
	metrics *metrics.Registry

	mStored        *prometheus.CounterVec
	mConsumeErrors *prometheus.CounterVec
	mPublishErrors *prometheus.CounterVec
	mPurged        *prometheus.CounterVec
	mPending       prometheus.Gauge
	mAlertActive   prometheus.Gauge
	mReplayed      prometheus.Counter
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "dlq-handler başlatılamadı: %v\n", err)
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
	logger.Info("dlq-handler başlıyor")

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

	if err := kafkax.WaitForBrokers(ctx, cfg.Infra.KafkaBrokers, 90*time.Second); err != nil {
		return fmt.Errorf("kafka hazır değil: %w", err)
	}

	reg := metrics.New(cfg.Base.ServiceName)

	srv := &Server{
		cfg:     cfg,
		log:     logger,
		store:   NewStore(pool),
		pool:    pool,
		metrics: reg,
	}
	srv.registerMetrics(reg)

	health := httpx.NewHealth()
	health.Register("postgres", postgres.HealthCheck(pool))
	health.Register("kafka", kafkax.HealthCheck(cfg.Infra.KafkaBrokers))

	router := httpx.NewRouter(logger, reg, 0)
	httpx.MountObservability(router, reg, health)
	router.Get("/internal/status", srv.handleStatus)

	return httpx.RunWithWorkers(ctx, logger, cfg.Base, router,
		srv.runConsumer,
		srv.runReplayWorker,
		srv.runRetentionJob,
		srv.runMetricsUpdater,
	)
}

func (s *Server) registerMetrics(reg *metrics.Registry) {
	s.mStored = reg.NewCounterVec("hub_dlq_stored_total",
		"Kalıcılaştırılan DLQ kayıtları.", "action")
	s.mConsumeErrors = reg.NewCounterVec("hub_kafka_consume_errors_total",
		"Kafka tüketim hataları.", "topic")
	s.mPublishErrors = reg.NewCounterVec("hub_kafka_publish_errors_total",
		"Kafka yayın hataları.", "topic")
	s.mPurged = reg.NewCounterVec("hub_retention_purged_total",
		"Retention politikasıyla silinen satır sayısı.", "table")
	s.mPending = reg.NewGauge("hub_dlq_pending_events",
		"İşlenmemiş (new veya replay_requested) DLQ kayıt sayısı.")
	s.mAlertActive = reg.NewGauge("hub_dlq_alert_active",
		"DLQ birikimi eşiği aşıldığında 1 olur.")

	replayed := prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "hub_dlq_replayed_total",
		Help:        "Yeniden yayınlanan DLQ mesajı sayısı.",
		ConstLabels: reg.ConstLabels(),
	})
	reg.MustRegister(replayed)
	s.mReplayed = replayed
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	pending, err := s.store.PendingCount(r.Context())
	if err != nil {
		httpx.Error(w, r, httpx.Internal("dlq durumu okunamadı", err))
		return
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"pending":         pending,
		"alert_threshold": s.cfg.AlertThreshold,
		"alerting":        pending > s.cfg.AlertThreshold,
		"retention": map[string]int{
			"dlq_days":      s.cfg.DLQRetentionDays,
			"event_days":    s.cfg.EventRetentionDays,
			"attempts_days": s.cfg.AttemptRetentionDays,
		},
	})
}
