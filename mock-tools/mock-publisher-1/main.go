// Command mock-publisher-1, ekosistemdeki bir publisher tool'u simüle eder.
//
// Gerçek bir tool'un yapması gereken her adımı yapar:
//  1. Platforma kendini kaydeder ve API anahtarı alır (bir kez),
//  2. Anahtarı kalıcı olarak saklar,
//  3. Her event için UUID v4 event_id üretir (idempotency),
//  4. Gövdeyi HMAC-SHA256 ile imzalar,
//  5. POST /api/v1/events ile platforma gönderir.
//
// Bu servis, docs/07-tool-onboarding.md'nin çalıştırılabilir referansıdır.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mindmons/integration-hub/internal/config"
	"github.com/mindmons/integration-hub/internal/httpx"
	"github.com/mindmons/integration-hub/internal/logging"
	"github.com/mindmons/integration-hub/internal/metrics"
	"github.com/mindmons/integration-hub/internal/toolkit"
)

type Config struct {
	Base config.Base

	AdminAPIURL   string
	IngestURL     string
	AdminEmail    string
	AdminPassword string

	ToolSlug string
	ToolName string

	CredPath string

	AutoFire     bool
	AutoInterval time.Duration
}

func loadConfig() (Config, error) {
	l := config.New()

	cfg := Config{
		Base: l.LoadBase("mock-publisher", "MOCK_PUBLISHER_PORT", 8090),

		AdminAPIURL:   l.Require("ADMIN_API_URL"),
		IngestURL:     l.Require("INGEST_URL"),
		AdminEmail:    l.Require("ADMIN_EMAIL"),
		AdminPassword: l.Require("ADMIN_PASSWORD"),

		ToolSlug: l.String("MOCK_PUBLISHER_SLUG", "mock-publisher-1"),
		ToolName: l.String("MOCK_PUBLISHER_NAME", "Mock Publisher 1"),

		CredPath: l.String("MOCK_PUBLISHER_CRED_PATH", "/data/credentials.json"),

		AutoFire:     l.Bool("MOCK_PUBLISHER_AUTO_FIRE", false),
		AutoInterval: l.Duration("MOCK_PUBLISHER_INTERVAL", 5*time.Second),
	}

	return cfg, l.Err()
}

// Server, mock publisher durumu.
type Server struct {
	cfg    Config
	log    *slog.Logger
	client *http.Client

	mu     sync.RWMutex
	creds  *toolkit.Credentials
	recent []SendRecord

	auto         bool
	autoInterval time.Duration

	mSent    *prometheus.CounterVec
	mLatency prometheus.Histogram
}

// SendRecord, UI'da gösterilen son gönderim kaydı.
type SendRecord struct {
	At        time.Time `json:"at"`
	EventID   string    `json:"event_id"`
	EventType string    `json:"event_type"`
	Status    int       `json:"status"`
	Response  string    `json:"response"`
	Error     string    `json:"error,omitempty"`
	Duplicate bool      `json:"duplicate"`
	LatencyMS int       `json:"latency_ms"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mock-publisher başlatılamadı: %v\n", err)
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

	ctx := context.Background()
	reg := metrics.New(cfg.Base.ServiceName)

	srv := &Server{
		cfg:          cfg,
		log:          logger,
		client:       &http.Client{Timeout: 15 * time.Second},
		auto:         cfg.AutoFire,
		autoInterval: cfg.AutoInterval,
		recent:       make([]SendRecord, 0, 50),
	}
	srv.mSent = reg.NewCounterVec("mock_publisher_events_sent_total",
		"Gönderilen event sayısı.", "event_type", "result")
	srv.mLatency = prometheusHistogram(reg)

	// Onboarding arka planda yapılır: platform henüz hazır değilse servis
	// yine de ayağa kalkar ve sağlık kontrolüne yanıt verir.
	go srv.onboard(ctx)

	health := httpx.NewHealth()
	health.Register("credentials", func(_ context.Context) error {
		if srv.credentials() == nil {
			return fmt.Errorf("platforma henüz kaydolunmadı")
		}
		return nil
	})

	router := httpx.NewRouter(logger, reg, 1<<20)
	httpx.MountObservability(router, reg, health)

	router.Get("/", srv.handleUI)
	router.Get("/api/status", srv.handleStatus)
	router.Post("/api/trigger", srv.handleTrigger)
	router.Post("/api/auto", srv.handleAuto)
	router.Get("/api/recent", srv.handleRecent)

	return httpx.RunWithWorkers(ctx, logger, cfg.Base, router, srv.runAutoFire)
}

func prometheusHistogram(reg *metrics.Registry) prometheus.Histogram {
	h := prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:        "mock_publisher_send_latency_seconds",
		Help:        "Ingest isteğinin süresi.",
		Buckets:     []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5},
		ConstLabels: reg.ConstLabels(),
	})
	reg.MustRegister(h)
	return h
}

// onboard, platforma kaydolma akışını yürütür (idempotent).
func (s *Server) onboard(ctx context.Context) {
	store := toolkit.NewCredStore(s.cfg.CredPath)

	// 1) Daha önce kaydolduysak anahtarı yeniden kullan.
	if existing, err := store.Load(); err != nil {
		s.log.Warn("saklanan kimlik okunamadı", slog.String("err", err.Error()))
	} else if existing != nil {
		s.setCredentials(existing)
		s.log.Info("saklanan API anahtarı kullanılıyor",
			slog.String("key_prefix", existing.KeyPrefix))
		return
	}

	admin := toolkit.NewAdminClient(s.cfg.AdminAPIURL)

	if err := admin.WaitReady(ctx, 3*time.Minute); err != nil {
		s.log.Error("admin-api hazır olmadı, kayıt yapılamadı", slog.String("err", err.Error()))
		return
	}
	if err := admin.Login(ctx, s.cfg.AdminEmail, s.cfg.AdminPassword); err != nil {
		s.log.Error("admin login başarısız", slog.String("err", err.Error()))
		return
	}

	// 2) Tool kaydı (varsa mevcut kayıt kullanılır).
	tool, err := admin.EnsureTool(ctx, s.cfg.ToolSlug, s.cfg.ToolName, "publisher",
		"Ekosistem simülasyonu için otomatik kaydolan örnek publisher tool.")
	if err != nil {
		s.log.Error("tool kaydı başarısız", slog.String("err", err.Error()))
		return
	}

	// 3) API anahtarı üret (plaintext yalnızca burada görünür).
	creds, err := admin.IssueKey(ctx, tool.ID)
	if err != nil {
		s.log.Error("api anahtarı alınamadı", slog.String("err", err.Error()))
		return
	}
	creds.ToolSlug = tool.Slug

	// 4) Kalıcı olarak sakla.
	if err := store.Save(creds); err != nil {
		s.log.Warn("kimlik dosyası yazılamadı, yeniden başlatmada tekrar kaydolunacak",
			slog.String("err", err.Error()))
	}

	s.setCredentials(creds)
	s.log.Info("platforma kaydolundu",
		slog.String("tool", tool.Slug),
		slog.String("key_prefix", creds.KeyPrefix))
}

func (s *Server) setCredentials(c *toolkit.Credentials) {
	s.mu.Lock()
	s.creds = c
	s.mu.Unlock()
}

func (s *Server) credentials() *toolkit.Credentials {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.creds
}

// runAutoFire, periyodik otomatik event gönderimini yürütür.
func (s *Server) runAutoFire(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	last := time.Now()

	for {
		select {
		case <-ctx.Done():
			return nil

		case <-ticker.C:
			s.mu.RLock()
			enabled := s.auto
			interval := s.autoInterval
			s.mu.RUnlock()

			if !enabled || time.Since(last) < interval {
				continue
			}
			last = time.Now()

			if s.credentials() == nil {
				continue
			}
			if _, err := s.sendEvent(ctx, randomEventType(), nil); err != nil {
				s.log.Warn("otomatik event gönderilemedi", slog.String("err", err.Error()))
			}
		}
	}
}

func (s *Server) recordSend(rec SendRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Ring buffer: bellek sınırsız büyümesin.
	s.recent = append([]SendRecord{rec}, s.recent...)
	if len(s.recent) > 50 {
		s.recent = s.recent[:50]
	}
}
