// Command delivery-worker, platformun TESLİMAT KOLUDUR.
//
// Sorumluluğu tek cümleyle: teslimat görevlerini alıp hedef subscriber'ın
// webhook adresine imzalı HTTP POST atmak ve başarısızlıkları retry / DLQ
// politikasına göre yönetmek.
//
// Dış dünyayla konuşan tek servis budur; bu yüzden circuit breaker, bulkhead,
// timeout ve dağıtık kilit gibi tüm dayanıklılık desenleri burada toplanır.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
	"github.com/segmentio/kafka-go"

	"github.com/mindmons/integration-hub/internal/breaker"
	"github.com/mindmons/integration-hub/internal/httpx"
	"github.com/mindmons/integration-hub/internal/kafkax"
	"github.com/mindmons/integration-hub/internal/lock"
	"github.com/mindmons/integration-hub/internal/logging"
	"github.com/mindmons/integration-hub/internal/metrics"
	"github.com/mindmons/integration-hub/internal/postgres"
	"github.com/mindmons/integration-hub/internal/redisx"
)

type Server struct {
	cfg       Config
	log       *slog.Logger
	store     *Store
	pool      *pgxpool.Pool
	rdb       *redis.Client
	locker    *lock.Locker
	breakers  *breaker.Group
	bulkhead  *Bulkhead
	client    *http.Client
	dlqWriter *kafka.Writer
	metrics   *metrics.Registry

	mAttempts        *prometheus.CounterVec
	mRetries         *prometheus.CounterVec
	mDLQ             *prometheus.CounterVec
	mSkipped         *prometheus.CounterVec
	mConsumeErrors   *prometheus.CounterVec
	mPublishErrors   *prometheus.CounterVec
	mDeliveryLatency *prometheus.HistogramVec
	mBreakerState    *prometheus.GaugeVec
	mE2ELatency      prometheus.Histogram
	mPendingTasks    prometheus.Gauge
	mInFlight        prometheus.Gauge
	mRescheduled     prometheus.Counter
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "delivery-worker başlatılamadı: %v\n", err)
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
	logger.Info("delivery-worker başlıyor")

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
		// Kilit olmadan mükerrer gönderim riski artar ama teslimat durmaz.
		logger.Warn("redis yok, dağıtık kilit devre dışı (fail-open)",
			slog.String("err", err.Error()))
		rdb = nil
	} else {
		defer func() { _ = rdb.Close() }()
	}

	if err := kafkax.WaitForBrokers(ctx, cfg.Infra.KafkaBrokers, 90*time.Second); err != nil {
		return fmt.Errorf("kafka hazır değil: %w", err)
	}

	reg := metrics.New(cfg.Base.ServiceName)

	dlqWriter := kafkax.NewWriter(cfg.Infra.KafkaBrokers, cfg.Infra.Topics.DLQ)
	defer func() { _ = dlqWriter.Close() }()

	srv := &Server{
		cfg:       cfg,
		log:       logger,
		store:     NewStore(pool),
		pool:      pool,
		rdb:       rdb,
		locker:    lock.New(rdb),
		breakers:  breaker.NewGroup(cfg.Breaker),
		bulkhead:  NewBulkhead(cfg.MaxConcurrency, cfg.MaxPerSubscriber),
		client:    newHTTPClient(cfg),
		dlqWriter: dlqWriter,
		metrics:   reg,
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
	router.Get("/internal/breakers", srv.handleBreakerStatus)

	workers := []func(context.Context) error{
		srv.runRetryScheduler,
		srv.runMetricsUpdater,
	}
	for i := 0; i < cfg.ConsumerCount; i++ {
		workers = append(workers, srv.runConsumer(i))
	}

	return httpx.RunWithWorkers(ctx, logger, cfg.Base, router, workers...)
}

// newHTTPClient, outbound webhook istemcisini kurar.
//
// Kaynak yönetimi kritiktir: varsayılan http.Client'ın timeout'u YOKTUR ve
// tek bir yanıt vermeyen subscriber goroutine'i sonsuza dek tutabilir.
// Bağlantı havuzu sınırları da açıkça verilir; aksi halde yüzlerce
// subscriber'a açılan bağlantılar dosya tanıtıcılarını tüketir.
func newHTTPClient(cfg Config) *http.Client {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          cfg.MaxConcurrency * 2,
		MaxIdleConnsPerHost:   cfg.MaxPerSubscriber,
		MaxConnsPerHost:       cfg.MaxPerSubscriber * 2,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ForceAttemptHTTP2:     true,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   cfg.HTTPTimeout,
		// Webhook'larda yönlendirme takip EDİLMEZ: 302 ile isteğin başka bir
		// adrese (ör. iç ağa) yönlendirilmesi SSRF vektörüdür.
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (s *Server) registerMetrics(reg *metrics.Registry) {
	s.mAttempts = reg.NewCounterVec("hub_delivery_attempts_total",
		"Webhook teslimat denemeleri.", "outcome", "status_class", "subscriber")
	s.mRetries = reg.NewCounterVec("hub_delivery_retries_total",
		"Planlanan yeniden denemeler.", "subscriber")
	s.mDLQ = reg.NewCounterVec("hub_dlq_events_total",
		"DLQ'ya gönderilen teslimatlar.", "subscriber")
	s.mSkipped = reg.NewCounterVec("hub_delivery_skipped_total",
		"İşlenmeden atlanan mesajlar.", "reason")
	s.mConsumeErrors = reg.NewCounterVec("hub_kafka_consume_errors_total",
		"Kafka tüketim hataları.", "topic")
	s.mPublishErrors = reg.NewCounterVec("hub_kafka_publish_errors_total",
		"Kafka yayın hataları.", "topic")
	s.mDeliveryLatency = reg.NewHistogramVec("hub_delivery_latency_seconds",
		"Webhook HTTP istek süresi.",
		[]float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		"subscriber")
	s.mBreakerState = reg.NewGaugeVec("hub_circuit_breaker_state",
		"Circuit breaker durumu (0=kapalı, 1=açık, 2=yarı açık).", "subscriber")
	s.mPendingTasks = reg.NewGauge("hub_delivery_pending_tasks",
		"Bekleyen veya işlenmekte olan teslimat görevi sayısı.")
	s.mInFlight = reg.NewGauge("hub_delivery_in_flight",
		"Şu anda gönderilmekte olan webhook sayısı.")

	e2e := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:        "hub_delivery_e2e_latency_seconds",
		Help:        "Event kabulünden başarılı teslimata kadar geçen toplam süre.",
		Buckets:     []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120},
		ConstLabels: reg.ConstLabels(),
	})
	rescheduled := prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "hub_delivery_rescheduled_total",
		Help:        "Zamanlayıcı tarafından yeniden yayınlanan görev sayısı.",
		ConstLabels: reg.ConstLabels(),
	})
	reg.MustRegister(e2e, rescheduled)

	s.mE2ELatency = e2e
	s.mRescheduled = rescheduled
}

// handleBreakerStatus, hata ayıklama için circuit breaker durumlarını döner.
func (s *Server) handleBreakerStatus(w http.ResponseWriter, _ *http.Request) {
	snapshot := s.breakers.Snapshot()

	out := make(map[string]string, len(snapshot))
	for k, v := range snapshot {
		out[k] = v.String()
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"breakers":  out,
		"in_flight": s.bulkhead.InFlight(),
	})
}
