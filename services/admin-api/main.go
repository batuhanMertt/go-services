// Command admin-api, platformun KONTROL DÜZLEMİDİR.
//
// Sorumluluğu tek cümleyle: ekosistem konfigürasyonunu (tool, event tipi,
// abonelik, yönlendirme kuralı, API anahtarı) yönetmek ve operasyonel veriyi
// (event, teslimat, DLQ, denetim) yöneticiye sunmak.
//
// Bu servis Kafka'ya YAZMAZ. Veri düzleminden bilinçli olarak ayrılmıştır:
// kontrol düzleminin çökmesi event akışını durdurmaz, event akışındaki bir
// tıkanıklık da yönetim panelini erişilemez kılmaz.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mindmons/integration-hub/internal/auth"
	"github.com/mindmons/integration-hub/internal/cryptox"
	"github.com/mindmons/integration-hub/internal/httpx"
	"github.com/mindmons/integration-hub/internal/logging"
	"github.com/mindmons/integration-hub/internal/metrics"
	"github.com/mindmons/integration-hub/internal/postgres"
	"github.com/mindmons/integration-hub/internal/ratelimit"
	"github.com/mindmons/integration-hub/internal/redisx"
)

func main() {
	if err := run(); err != nil {
		// Logger henüz kurulmamış olabilir; stderr'e düz yaz.
		fmt.Fprintf(os.Stderr, "admin-api başlatılamadı: %v\n", err)
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
	logger.Info("admin-api başlıyor", slog.String("env", cfg.Base.Env))

	ctx := context.Background()

	sealer, err := cryptox.NewSealer(cfg.EncryptionKey)
	if err != nil {
		return err
	}

	tokens, err := auth.NewTokenManager(cfg.JWTSecret, "integration-hub", cfg.JWTAccessTTL, cfg.JWTRefreshTTL)
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
	logger.Info("postgres bağlantısı hazır")

	rdb, err := redisx.Connect(ctx, cfg.Infra.Redis)
	if err != nil {
		// Redis olmadan da yönetim paneli çalışabilmeli: rate limit devre dışı
		// kalır ama CRUD işlemleri sürer (NFR-1.2 graceful degradation).
		logger.Warn("redis bağlantısı kurulamadı, rate limit devre dışı", slog.String("err", err.Error()))
		rdb = nil
	} else {
		defer func() { _ = rdb.Close() }()
		logger.Info("redis bağlantısı hazır")
	}

	reg := metrics.New(cfg.Base.ServiceName)
	store := NewStore(pool)

	srv := &Server{
		cfg:         cfg,
		log:         logger,
		store:       store,
		pool:        pool,
		rdb:         rdb,
		tokens:      tokens,
		sealer:      sealer,
		limiter:     ratelimit.New(rdb, cfg.RateFailClosed),
		metrics:     reg,
		argonParams: auth.DefaultArgon2Params(),
	}
	srv.registerMetrics(reg)

	if err := srv.bootstrapAdmin(ctx); err != nil {
		return err
	}

	health := httpx.NewHealth()
	health.Register("postgres", postgres.HealthCheck(pool))
	if rdb != nil {
		health.Register("redis", redisx.HealthCheck(rdb))
	}

	handler := srv.Routes(health)

	return httpx.Run(ctx, logger, cfg.Base, handler)
}

// registerMetrics, servise özel Prometheus metriklerini kaydeder.
func (s *Server) registerMetrics(reg *metrics.Registry) {
	authFailures := reg.NewCounterVec("hub_admin_auth_failures_total",
		"Başarısız kimlik doğrulama denemeleri.", "reason")
	auditWrites := reg.NewCounterVec("hub_audit_writes_total",
		"Denetim kaydı yazma sonuçları.", "result")
	replayRequests := prometheus.NewCounter(prometheus.CounterOpts{
		Name:        "hub_dlq_replay_requests_total",
		Help:        "Admin tarafından oluşturulan DLQ replay talepleri.",
		ConstLabels: reg.ConstLabels(),
	})
	reg.MustRegister(replayRequests)

	s.mAuthFailures = &prometheusCounter{inc: func(labels ...string) {
		if len(labels) == 1 {
			authFailures.WithLabelValues(labels[0]).Inc()
		}
	}}
	s.mAuditWrites = &prometheusCounter{inc: func(labels ...string) {
		if len(labels) == 1 {
			auditWrites.WithLabelValues(labels[0]).Inc()
		}
	}}
	s.mReplayRequest = &prometheusCounter{inc: func(_ ...string) {
		replayRequests.Inc()
	}}
}

// bootstrapAdmin, ilk admin kullanıcısını oluşturur.
//
// Neden migration'da değil? argon2id hash'i SQL içinde üretilemez ve şifrenin
// migration dosyasına yazılması güvenlik açığı olurdu. Bootstrap açılışta,
// environment'tan okunan şifreyle yapılır ve idempotent'tir.
func (s *Server) bootstrapAdmin(ctx context.Context) error {
	hash, err := auth.HashPassword(s.cfg.BootstrapPassword, s.argonParams)
	if err != nil {
		return fmt.Errorf("bootstrap admin şifresi hash'lenemedi: %w", err)
	}

	bctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	created, err := s.store.CreateUserIfMissing(bctx, s.cfg.BootstrapEmail, hash, "Platform Admin", "admin")
	if err != nil {
		return fmt.Errorf("bootstrap admin oluşturulamadı: %w", err)
	}

	if created {
		s.log.Warn("ilk admin kullanıcısı oluşturuldu — ilk girişten sonra şifreyi değiştirin",
			slog.String("email", s.cfg.BootstrapEmail))

		if err := s.store.WriteAudit(bctx, AuditEntry{
			ActorType: "system", Action: "admin_user.bootstrap",
			ResourceType: "admin_user", ActorLabel: "bootstrap",
			After: map[string]string{"email": s.cfg.BootstrapEmail},
		}); err != nil && !errors.Is(err, context.Canceled) {
			s.log.Warn("bootstrap denetim kaydı yazılamadı", slog.String("err", err.Error()))
		}
	} else {
		s.log.Info("admin kullanıcısı zaten mevcut", slog.String("email", s.cfg.BootstrapEmail))
	}

	return nil
}
