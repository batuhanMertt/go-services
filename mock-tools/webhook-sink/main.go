// Command webhook-sink, webhook.site benzeri bir hata ayıklama aracıdır.
//
// Platformun ürettiği HER webhook'u yakalar; başlıkları, gövdeyi, imza
// doğrulama sonucunu ve zamanı UI'da gösterir. Filtreleme ve arama
// destekler.
//
// Neden değerli? Bir entegrasyon sorununda "platform ne gönderdi?" sorusunun
// cevabına saniyeler içinde ulaşmak, log kazımaktan çok daha hızlıdır.
// Ayrıca yeni bir subscriber geliştiren dış ekip, kendi kodunu yazmadan önce
// gerçek payload'ı burada görebilir.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
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

	ToolSlug  string
	PublicURL string

	SigningKey string
	HMACWindow time.Duration

	Capacity      int
	AutoSubscribe bool
}

func loadConfig() (Config, error) {
	l := config.New()

	cfg := Config{
		Base: l.LoadBase("webhook-sink", "WEBHOOK_SINK_PORT", 8092),

		AdminAPIURL:   l.Require("ADMIN_API_URL"),
		AdminEmail:    l.Require("ADMIN_EMAIL"),
		AdminPassword: l.Require("ADMIN_PASSWORD"),

		ToolSlug:  l.String("WEBHOOK_SINK_SLUG", "webhook-sink"),
		PublicURL: l.Require("WEBHOOK_SINK_PUBLIC_URL"),

		SigningKey: l.RequireMinLen("WEBHOOK_SIGNING_KEY", 16),
		HMACWindow: l.Duration("HMAC_TOLERANCE", 5*time.Minute),

		Capacity:      l.Int("WEBHOOK_SINK_CAPACITY", 500),
		AutoSubscribe: l.Bool("WEBHOOK_SINK_AUTO_SUBSCRIBE", true),
	}

	return cfg, l.Err()
}

// Capture, yakalanan tek bir webhook.
type Capture struct {
	ID        string            `json:"id"`
	At        time.Time         `json:"at"`
	Method    string            `json:"method"`
	Path      string            `json:"path"`
	SourceIP  string            `json:"source_ip"`
	EventID   string            `json:"event_id"`
	EventType string            `json:"event_type"`
	Attempt   string            `json:"attempt"`
	TraceID   string            `json:"trace_id"`
	Signature string            `json:"signature_status"`
	Headers   map[string]string `json:"headers"`
	Body      json.RawMessage   `json:"body"`
	BodyText  string            `json:"body_text"`
	SizeBytes int               `json:"size_bytes"`
}

type Server struct {
	cfg Config
	log *slog.Logger

	mu         sync.RWMutex
	captures   []Capture
	registered atomic.Bool

	mCaptured *prometheus.CounterVec
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "webhook-sink başlatılamadı: %v\n", err)
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
		captures: make([]Capture, 0, cfg.Capacity),
	}
	srv.mCaptured = reg.NewCounterVec("webhook_sink_captured_total",
		"Yakalanan webhook sayısı.", "event_type", "signature")

	if cfg.AutoSubscribe {
		go srv.onboard(ctx)
	} else {
		srv.registered.Store(true)
	}

	health := httpx.NewHealth()

	router := httpx.NewRouter(logger, reg, 8<<20)
	httpx.MountObservability(router, reg, health)

	router.Post("/webhook", srv.handleCapture)
	// Herhangi bir alt yol da kabul edilir: farklı subscriber'lar farklı
	// yollar kullanabilsin diye (webhook.site davranışı).
	router.Post("/webhook/*", srv.handleCapture)

	router.Get("/", srv.handleUI)
	router.Get("/api/captures", srv.handleList)
	router.Get("/api/captures/{id}", srv.handleGet)
	router.Delete("/api/captures", srv.handleClear)
	router.Get("/api/status", srv.handleStatus)

	return httpx.Run(ctx, logger, cfg.Base, router)
}

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

	tool, err := admin.EnsureTool(ctx, s.cfg.ToolSlug, "Webhook Sink", "subscriber",
		"Tüm event akışını yakalayan hata ayıklama aracı.")
	if err != nil {
		s.log.Error("tool kaydı başarısız", slog.String("err", err.Error()))
		return
	}

	types, err := admin.ListEventTypes(ctx)
	if err != nil {
		s.log.Error("event tipleri okunamadı", slog.String("err", err.Error()))
		return
	}

	for _, et := range types {
		sub, err := admin.EnsureSubscription(ctx, tool.ID, et.ID, s.cfg.PublicURL)
		if err != nil {
			s.log.Warn("abonelik oluşturulamadı",
				slog.String("event_type", et.Name), slog.String("err", err.Error()))
			continue
		}
		if err := admin.EnsureRoutingRule(ctx, "sink-all-"+et.Name, sub.ID, map[string]any{}); err != nil {
			s.log.Warn("kural oluşturulamadı", slog.String("err", err.Error()))
		}
	}

	s.registered.Store(true)
	s.log.Info("webhook sink tüm event tiplerine abone oldu", slog.String("url", s.cfg.PublicURL))
}

// handleCapture, gelen webhook'u kaydeder ve DAİMA 200 döner.
//
// Sink asla hata döndürmez: amacı akışı gözlemlemek, akışa müdahale etmek
// değil. Hata dönseydi retry mekanizmasını tetikler ve gözlemi bozardı.
func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, "gövde okunamadı", http.StatusBadRequest)
		return
	}

	c := Capture{
		ID:        uuid.NewString(),
		At:        time.Now().UTC(),
		Method:    r.Method,
		Path:      r.URL.Path,
		SourceIP:  httpx.ClientIP(r),
		EventID:   r.Header.Get("X-Event-Id"),
		EventType: r.Header.Get("X-Event-Type"),
		Attempt:   r.Header.Get("X-Delivery-Attempt"),
		TraceID:   r.Header.Get("X-Trace-Id"),
		Headers:   allHeaders(r),
		SizeBytes: len(body),
		BodyText:  string(body),
	}

	if json.Valid(body) {
		c.Body = body
	}

	signature := r.Header.Get("X-Hub-Signature-256")
	timestamp := r.Header.Get("X-Hub-Timestamp")

	switch {
	case signature == "":
		c.Signature = "missing"
	default:
		if err := cryptox.VerifyRequest([]byte(s.cfg.SigningKey), timestamp, body, signature, s.cfg.HMACWindow); err != nil {
			c.Signature = "invalid"
		} else {
			c.Signature = "valid"
		}
	}

	s.store(c)
	s.mCaptured.WithLabelValues(orDash(c.EventType), c.Signature).Inc()

	httpx.JSON(w, http.StatusOK, map[string]string{
		"status":     "captured",
		"capture_id": c.ID,
	})
}

func allHeaders(r *http.Request) map[string]string {
	out := make(map[string]string, len(r.Header))
	for k, v := range r.Header {
		val := strings.Join(v, ", ")
		// İmza kısaltılır: tam değer arayüzde gösterilmemeli.
		if strings.EqualFold(k, "X-Hub-Signature-256") && len(val) > 24 {
			val = val[:24] + "…"
		}
		out[k] = val
	}
	return out
}

func (s *Server) store(c Capture) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.captures = append([]Capture{c}, s.captures...)
	if len(s.captures) > s.cfg.Capacity {
		s.captures = s.captures[:s.cfg.Capacity]
	}
}

// --- HTTP handler'ları -------------------------------------------------------

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	eventType := strings.TrimSpace(r.URL.Query().Get("event_type"))
	sig := strings.TrimSpace(r.URL.Query().Get("signature"))

	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}

	s.mu.RLock()
	all := make([]Capture, len(s.captures))
	copy(all, s.captures)
	s.mu.RUnlock()

	out := make([]Capture, 0, limit)
	for _, c := range all {
		if eventType != "" && c.EventType != eventType {
			continue
		}
		if sig != "" && c.Signature != sig {
			continue
		}
		if q != "" && !matches(c, q) {
			continue
		}

		// Liste yanıtında gövde kısaltılır; tam gövde detay ucundan alınır.
		slim := c
		if len(slim.BodyText) > 300 {
			slim.BodyText = slim.BodyText[:300] + "…"
		}
		slim.Body = nil

		out = append(out, slim)
		if len(out) >= limit {
			break
		}
	}

	httpx.JSON(w, http.StatusOK, map[string]any{
		"items": out,
		"total": len(all),
	})
}

func matches(c Capture, q string) bool {
	return strings.Contains(strings.ToLower(c.EventID), q) ||
		strings.Contains(strings.ToLower(c.EventType), q) ||
		strings.Contains(strings.ToLower(c.TraceID), q) ||
		strings.Contains(strings.ToLower(c.BodyText), q)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/captures/")

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, c := range s.captures {
		if c.ID == id {
			httpx.JSON(w, http.StatusOK, c)
			return
		}
	}
	httpx.Error(w, r, httpx.NotFound("not_found", "kayıt bulunamadı"))
}

func (s *Server) handleClear(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	n := len(s.captures)
	s.captures = s.captures[:0]
	s.mu.Unlock()

	httpx.JSON(w, http.StatusOK, map[string]int{"cleared": n})
}

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.RLock()
	n := len(s.captures)
	s.mu.RUnlock()

	httpx.JSON(w, http.StatusOK, map[string]any{
		"registered":  s.registered.Load(),
		"webhook_url": s.cfg.PublicURL,
		"captured":    n,
		"capacity":    s.cfg.Capacity,
	})
}

func (s *Server) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(uiHTML))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
