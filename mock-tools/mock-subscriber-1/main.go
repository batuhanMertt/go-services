// Command mock-subscriber-1, ekosistemdeki bir subscriber tool'u simüle eder.
//
// Yaptıkları:
//  1. Platforma subscriber olarak kaydolur,
//  2. Tanımlı tüm event tiplerine kendi webhook adresiyle abone olur,
//  3. Gelen webhook'un HMAC imzasını doğrular,
//  4. "Her N istekten birini fail et" anahtarıyla retry/DLQ akışını canlı
//     olarak test etmeyi sağlar.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mindmons/integration-hub/internal/config"
	"github.com/mindmons/integration-hub/internal/cryptox"
	"github.com/mindmons/integration-hub/internal/httpx"
	"github.com/mindmons/integration-hub/internal/logging"
	"github.com/mindmons/integration-hub/internal/metrics"
	"github.com/mindmons/integration-hub/internal/toolkit"
)

type Config struct {
	Base config.Base

	AdminAPIURL   string
	AdminEmail    string
	AdminPassword string

	ToolSlug    string
	ToolName    string
	PublicURL   string // platformun bu servise ulaşacağı adres
	SigningKey  string
	HMACWindow  time.Duration
	FailEvery   int
	FailStatus  int
	ProcessTime time.Duration
	Capacity    int
}

func loadConfig() (Config, error) {
	l := config.New()

	cfg := Config{
		Base: l.LoadBase("mock-subscriber", "MOCK_SUBSCRIBER_PORT", 8091),

		AdminAPIURL:   l.Require("ADMIN_API_URL"),
		AdminEmail:    l.Require("ADMIN_EMAIL"),
		AdminPassword: l.Require("ADMIN_PASSWORD"),

		ToolSlug:  l.String("MOCK_SUBSCRIBER_SLUG", "mock-subscriber-1"),
		ToolName:  l.String("MOCK_SUBSCRIBER_NAME", "Mock Subscriber 1"),
		PublicURL: l.Require("MOCK_SUBSCRIBER_PUBLIC_URL"),

		SigningKey: l.RequireMinLen("WEBHOOK_SIGNING_KEY", 16),
		HMACWindow: l.Duration("HMAC_TOLERANCE", 5*time.Minute),

		FailEvery:   l.Int("MOCK_SUBSCRIBER_FAIL_EVERY", 0),
		FailStatus:  l.Int("MOCK_SUBSCRIBER_FAIL_STATUS", 500),
		ProcessTime: l.Duration("MOCK_SUBSCRIBER_PROCESS_TIME", 0),
		Capacity:    l.Int("MOCK_SUBSCRIBER_CAPACITY", 200),
	}

	return cfg, l.Err()
}

// Received, alınan bir webhook kaydı.
type Received struct {
	At        time.Time         `json:"at"`
	EventID   string            `json:"event_id"`
	EventType string            `json:"event_type"`
	Attempt   string            `json:"attempt"`
	Replay    bool              `json:"replay"`
	Signature string            `json:"signature_status"`
	Outcome   string            `json:"outcome"`
	Status    int               `json:"status"`
	Headers   map[string]string `json:"headers"`
}

type Server struct {
	cfg Config
	log *slog.Logger

	mu       sync.RWMutex
	received []Received

	failEvery atomic.Int64
	counter   atomic.Int64

	registered atomic.Bool

	mReceived *prometheus.CounterVec
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mock-subscriber başlatılamadı: %v\n", err)
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
		cfg:      cfg,
		log:      logger,
		received: make([]Received, 0, cfg.Capacity),
	}
	srv.failEvery.Store(int64(cfg.FailEvery))
	srv.mReceived = reg.NewCounterVec("mock_subscriber_webhooks_total",
		"Alınan webhook sayısı.", "event_type", "outcome")

	go srv.onboard(ctx)

	health := httpx.NewHealth()
	health.Register("registration", func(_ context.Context) error {
		if !srv.registered.Load() {
			return fmt.Errorf("platforma henüz kaydolunmadı")
		}
		return nil
	})

	router := httpx.NewRouter(logger, reg, 4<<20)
	httpx.MountObservability(router, reg, health)

	router.Post("/webhook", srv.handleWebhook)
	router.Get("/", srv.handleUI)
	router.Get("/api/status", srv.handleStatus)
	router.Post("/api/failure", srv.handleFailureToggle)
	router.Get("/api/received", srv.handleReceived)

	return httpx.Run(ctx, logger, cfg.Base, router)
}

// onboard, platforma kaydolur ve tüm event tiplerine abone olur.
func (s *Server) onboard(ctx context.Context) {
	admin := toolkit.NewAdminClient(s.cfg.AdminAPIURL)

	if err := admin.WaitReady(ctx, 3*time.Minute); err != nil {
		s.log.Error("admin-api hazır olmadı", slog.String("err", err.Error()))
		return
	}
	if err := admin.Login(ctx, s.cfg.AdminEmail, s.cfg.AdminPassword); err != nil {
		s.log.Error("admin login başarısız", slog.String("err", err.Error()))
		return
	}

	tool, err := admin.EnsureTool(ctx, s.cfg.ToolSlug, s.cfg.ToolName, "subscriber",
		"Webhook alan, imza doğrulayan ve arıza simülasyonu yapabilen örnek subscriber.")
	if err != nil {
		s.log.Error("tool kaydı başarısız", slog.String("err", err.Error()))
		return
	}

	types, err := admin.ListEventTypes(ctx)
	if err != nil {
		s.log.Error("event tipleri okunamadı", slog.String("err", err.Error()))
		return
	}

	webhookURL := s.cfg.PublicURL

	for _, et := range types {
		sub, err := admin.EnsureSubscription(ctx, tool.ID, et.ID, webhookURL)
		if err != nil {
			s.log.Warn("abonelik oluşturulamadı",
				slog.String("event_type", et.Name), slog.String("err", err.Error()))
			continue
		}

		// Koşulsuz kural: bu abonelik için gelen her event teslim edilir.
		// Admin panelinden filtre eklenerek davranış değiştirilebilir.
		if err := admin.EnsureRoutingRule(ctx,
			"deliver-all-"+et.Name, sub.ID, map[string]any{}); err != nil {
			s.log.Warn("yönlendirme kuralı oluşturulamadı",
				slog.String("event_type", et.Name), slog.String("err", err.Error()))
		}

		s.log.Info("abonelik hazır",
			slog.String("event_type", et.Name), slog.String("endpoint", webhookURL))
	}

	s.registered.Store(true)
	s.log.Info("platforma kaydolundu ve abonelikler kuruldu", slog.String("tool", tool.Slug))
}

// handleWebhook, platformdan gelen webhook'u karşılar.
func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "gövde okunamadı", http.StatusBadRequest)
		return
	}

	signature := r.Header.Get("X-Hub-Signature-256")
	timestamp := r.Header.Get("X-Hub-Timestamp")

	rec := Received{
		At:        time.Now().UTC(),
		EventID:   r.Header.Get("X-Event-Id"),
		EventType: r.Header.Get("X-Event-Type"),
		Attempt:   r.Header.Get("X-Delivery-Attempt"),
		Headers:   collectHeaders(r),
	}

	// İmza doğrulaması: "bu webhook gerçekten platformdan mı geldi?"
	// Gerçek bir subscriber bu adımı ATLAMAMALIDIR — aksi halde webhook
	// adresini bilen herkes sahte event enjekte edebilir.
	if err := cryptox.VerifyRequest([]byte(s.cfg.SigningKey), timestamp, body, signature, s.cfg.HMACWindow); err != nil {
		rec.Signature = "invalid: " + err.Error()
		rec.Outcome = "rejected"
		rec.Status = http.StatusUnauthorized

		s.record(rec)
		s.mReceived.WithLabelValues(rec.EventType, "invalid_signature").Inc()
		s.log.Warn("geçersiz imzalı webhook reddedildi",
			slog.String("event_id", rec.EventID), slog.String("err", err.Error()))

		http.Error(w, "imza doğrulanamadı", http.StatusUnauthorized)
		return
	}
	rec.Signature = "valid"

	var payload struct {
		Delivery struct {
			Replay bool `json:"replay"`
		} `json:"delivery"`
	}
	_ = json.Unmarshal(body, &payload)
	rec.Replay = payload.Delivery.Replay

	// İşleme süresi simülasyonu (yavaş subscriber senaryosu).
	if s.cfg.ProcessTime > 0 {
		time.Sleep(s.cfg.ProcessTime)
	}

	// Arıza simülasyonu: her N istekten biri kasten başarısız olur.
	// Bu anahtar sayesinde exponential backoff, circuit breaker ve DLQ
	// davranışı canlı sistemde gözlemlenebilir.
	if n := s.failEvery.Load(); n > 0 {
		count := s.counter.Add(1)
		if count%n == 0 {
			rec.Outcome = "simulated_failure"
			rec.Status = s.cfg.FailStatus

			s.record(rec)
			s.mReceived.WithLabelValues(rec.EventType, "simulated_failure").Inc()
			s.log.Warn("arıza simülasyonu: istek kasten reddedildi",
				slog.String("event_id", rec.EventID),
				slog.Int("status", s.cfg.FailStatus))

			http.Error(w, "simulated failure", s.cfg.FailStatus)
			return
		}
	}

	rec.Outcome = "processed"
	rec.Status = http.StatusOK
	s.record(rec)
	s.mReceived.WithLabelValues(rec.EventType, "processed").Inc()

	s.log.Info("webhook işlendi",
		slog.String("event_id", rec.EventID),
		slog.String("event_type", rec.EventType),
		slog.String("attempt", rec.Attempt),
		slog.Bool("replay", rec.Replay))

	httpx.JSON(w, http.StatusOK, map[string]string{
		"status":   "processed",
		"event_id": rec.EventID,
	})
}

func collectHeaders(r *http.Request) map[string]string {
	interesting := []string{
		"X-Event-Id", "X-Event-Type", "X-Delivery-Id", "X-Delivery-Attempt",
		"X-Delivery-Max-Attempts", "X-Hub-Timestamp", "X-Trace-Id", "Content-Type",
	}
	out := make(map[string]string, len(interesting))
	for _, h := range interesting {
		if v := r.Header.Get(h); v != "" {
			out[h] = v
		}
	}
	// İmza kısaltılarak gösterilir: tam değer loglarda tutulmamalı.
	if sig := r.Header.Get("X-Hub-Signature-256"); sig != "" {
		if len(sig) > 20 {
			sig = sig[:20] + "…"
		}
		out["X-Hub-Signature-256"] = sig
	}
	return out
}

func (s *Server) record(rec Received) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.received = append([]Received{rec}, s.received...)
	if len(s.received) > s.cfg.Capacity {
		s.received = s.received[:s.cfg.Capacity]
	}
}
