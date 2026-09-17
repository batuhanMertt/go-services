// Command event-ingest, platformun VERİ DÜZLEMİ GİRİŞİDİR.
//
// Sorumluluğu tek cümleyle: publisher tool'lardan gelen event'leri kimlik ve
// imza doğrulamasından geçirip kalıcı hale getirmek ve Kafka'ya güvenle
// aktarmak.
//
// Bu servis event'leri YÖNLENDİRMEZ ve TESLİM ETMEZ. Tek işi "kabul et ve
// kaybetme"dir; bu sayede en yüksek hacimli uç nokta olarak bağımsız
// ölçeklenebilir ve p95 < 50 ms hedefi (NFR-2.1) korunabilir.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"

	"github.com/mindmons/integration-hub/internal/cryptox"
	"github.com/mindmons/integration-hub/internal/ddos"
	"github.com/mindmons/integration-hub/internal/httpx"
	"github.com/mindmons/integration-hub/internal/kafkax"
	"github.com/mindmons/integration-hub/internal/logging"
	"github.com/mindmons/integration-hub/internal/metrics"
	"github.com/mindmons/integration-hub/internal/postgres"
	"github.com/mindmons/integration-hub/internal/ratelimit"
	"github.com/mindmons/integration-hub/internal/redisx"
	"github.com/mindmons/integration-hub/internal/schema"
)

// Server, event-ingest bağımlılıkları.
type Server struct {
	cfg      Config
	log      *slog.Logger
	store    *Store
	pool     *pgxpool.Pool
	rdb      *redis.Client
	keys     *KeyCache
	schemas  *schema.Registry
	limiter  *ratelimit.Limiter
	metrics  *metrics.Registry
	conns    *ddos.ConnLimiter
	detector *ddos.Detector
	guard    *ddos.Guard

	mIngested           *prometheus.CounterVec
	mValidationErrors   *prometheus.CounterVec
	mRateLimited        *prometheus.CounterVec
	mKafkaPublishErrors *prometheus.CounterVec
	mOutboxPending      prometheus.Gauge
	mOutboxPublished    prometheus.Counter
	mKeyCacheSize       prometheus.Gauge
	mSchemaCount        prometheus.Gauge
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "event-ingest başlatılamadı: %v\n", err)
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
	logger.Info("event-ingest başlıyor")

	ctx := context.Background()

	sealer, err := cryptox.NewSealer(cfg.EncryptionKey)
	if err != nil {
		return err
	}

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
		// Rate limit olmadan da event kabul edilebilmeli (NFR-1.2).
		logger.Warn("redis bağlantısı kurulamadı, rate limit devre dışı",
			slog.String("err", err.Error()))
		rdb = nil
	} else {
		defer func() { _ = rdb.Close() }()
	}

	// Kafka'nın hazır olmasını bekle: outbox poller ilk turda hata basmasın.
	if err := kafkax.WaitForBrokers(ctx, cfg.Infra.KafkaBrokers, 60*time.Second); err != nil {
		logger.Warn("kafka hazır değil, outbox biriktirmeye devam edecek",
			slog.String("err", err.Error()))
	}

	reg := metrics.New(cfg.Base.ServiceName)
	store := NewStore(pool)

	srv := &Server{
		cfg:     cfg,
		log:     logger,
		store:   store,
		pool:    pool,
		rdb:     rdb,
		keys:    NewKeyCache(store, sealer, cfg.KeyCacheTTL),
		schemas: schema.NewRegistry(),
		limiter: ratelimit.New(rdb, cfg.RateFailClosed),
		metrics: reg,
		conns:   ddos.NewConnLimiter(cfg.MaxConcurrent),
		detector: ddos.NewDetector(ddos.DetectorConfig{
			Window:    cfg.AttackWindow,
			Threshold: cfg.AttackThresholdRPS,
			Cooldown:  cfg.AttackCooldown,
		}),
		guard: ddos.NewGuard(rdb, ddos.GuardConfig{
			StrikeWindow:    cfg.BanStrikeWindow,
			StrikeThreshold: cfg.BanStrikeThreshold,
			BanTTL:          cfg.BanTTL,
		}),
	}
	srv.registerMetrics(reg)

	// Şemalar açılışta yüklenir; yüklenemezse servis AÇILMAZ — şemasız
	// çalışmak, doğrulanmamış event kabul etmek demektir.
	if err := srv.reloadSchemas(ctx); err != nil {
		return fmt.Errorf("event şemaları yüklenemedi: %w", err)
	}
	logger.Info("event şemaları yüklendi", slog.Int("count", srv.schemas.Len()))

	health := httpx.NewHealth()
	health.Register("postgres", postgres.HealthCheck(pool))
	health.Register("kafka", kafkax.HealthCheck(cfg.Infra.KafkaBrokers))
	if rdb != nil {
		health.Register("redis", redisx.HealthCheck(rdb))
	}

	router := httpx.NewRouter(logger, reg, cfg.MaxBodyBytes)
	// DDoS katmanı: eşzamanlı bağlantı tavanı + global saldırı-modu gözlemi.
	// Bilinçli olarak router seviyesinde ve TÜM uçlara (healthz dahil)
	// uygulanır — Recover/RequestLogger'dan SONRA eklendiği için chi bunları
	// dıştan içe sarar, yani conns/Observe route handler'a en yakın halka
	// olur; asıl koruma zaten Redis'e hiç gitmeyen bellek içi kontrollerdir.
	router.Use(srv.conns.Middleware)
	router.Use(ddos.Observe(srv.detector))
	httpx.MountObservability(router, reg, health)
	router.Post("/api/v1/events", srv.handleIngest)
	router.Get("/api/v1/event-types", srv.handleListKnownTypes)

	return httpx.RunWithWorkers(ctx, logger, cfg.Base, router,
		srv.runOutboxPublisher,
		srv.runOutboxMaintenance,
		srv.runSchemaRefresher,
	)
}

func (s *Server) registerMetrics(reg *metrics.Registry) {
	s.mIngested = reg.NewCounterVec("hub_events_ingested_total",
		"Kabul edilen/reddedilen event sayısı.", "tool", "event_type", "result")
	s.mValidationErrors = reg.NewCounterVec("hub_ingest_validation_errors_total",
		"Doğrulama hatası sayısı.", "reason")
	s.mRateLimited = reg.NewCounterVec("hub_rate_limit_rejected_total",
		"Rate limit nedeniyle reddedilen istekler.", "scope", "tool")
	s.mKafkaPublishErrors = reg.NewCounterVec("hub_kafka_publish_errors_total",
		"Kafka publish hataları.", "topic")
	s.mOutboxPending = reg.NewGauge("hub_outbox_pending_messages",
		"Yayınlanmayı bekleyen outbox mesajı sayısı.")
	s.mKeyCacheSize = reg.NewGauge("hub_api_key_cache_size",
		"Bellekteki API key önbellek kaydı sayısı.")
	s.mSchemaCount = reg.NewGauge("hub_event_schemas_loaded",
		"Yüklü event şeması sayısı.")

	published := prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "hub_outbox_published_total",
		Help:        "Kafka'ya yayınlanan outbox mesajı sayısı.",
		ConstLabels: reg.ConstLabels(),
	})
	reg.MustRegister(published)
	s.mOutboxPublished = published
}

// reloadSchemas, aktif event tiplerini DB'den okuyup derler.
func (s *Server) reloadSchemas(ctx context.Context) error {
	loadCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	raw, err := s.store.LoadActiveSchemas(loadCtx)
	if err != nil {
		return err
	}
	if err := s.schemas.Replace(raw); err != nil {
		return err
	}
	s.mSchemaCount.Set(float64(s.schemas.Len()))
	return nil
}

// runSchemaRefresher, admin panelinden eklenen yeni event tiplerinin
// yeniden dağıtım gerektirmeden etkinleşmesini sağlar.
//
// Neden polling? Alternatif, admin-api'nin Kafka üzerinden "şema değişti"
// yayını yapmasıydı — bu, kontrol düzlemine broker bağımlılığı eklerdi.
// Şema değişiklikleri nadirdir; 30 saniyelik gecikme kabul edilebilir.
func (s *Server) runSchemaRefresher(ctx context.Context) error {
	interval := s.cfg.SchemaRefresh
	if interval <= 0 {
		interval = 30 * time.Second
	}

	// Jitter: birden fazla instance aynı anda DB'ye gitmesin.
	jitter := time.Duration(rand.Int63n(int64(interval / 4))) //nolint:gosec
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(jitter):
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.reloadSchemas(ctx); err != nil {
				// Eski şemalarla çalışmaya devam et: yenileme hatası servisi
				// durdurmamalı.
				s.log.WarnContext(ctx, "şema yenileme başarısız, mevcut şemalarla devam ediliyor",
					slog.String("err", err.Error()))
			}
		}
	}
}

// handleListKnownTypes, publisher'ların hangi tipleri gönderebileceğini görmesi
// için basit bir keşif ucu.
func (s *Server) handleListKnownTypes(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"event_types": s.schemas.Keys(),
		"count":       s.schemas.Len(),
	})
}

// touchKeyOccasionally, anahtar kullanım zamanını ~%2 olasılıkla günceller.
// Her istekte UPDATE yazmak, ingest'in en sıcak yolunda gereksiz DB yüküdür;
// "en son ne zaman kullanıldı" bilgisi için bu doğruluk fazlasıyla yeterli.
func (s *Server) touchKeyOccasionally(keyID string) {
	if rand.Intn(50) != 0 { //nolint:gosec
		return
	}
	// İstek context'i BİLİNÇLİ olarak devralınmaz: bu güncelleme yanıt
	// döndükten sonra tamamlanır. İstek context'i kullanılsaydı, yanıt
	// yazılır yazılmaz iptal edilir ve güncelleme hiç yapılmazdı.
	go func() { //nolint:contextcheck
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.store.TouchKeyUsage(ctx, keyID); err != nil {
			s.log.Debug("anahtar kullanım zamanı güncellenemedi", slog.String("err", err.Error()))
		}
	}()
}
