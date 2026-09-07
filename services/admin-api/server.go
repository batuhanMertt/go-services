package main

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/mindmons/integration-hub/internal/auth"
	"github.com/mindmons/integration-hub/internal/cryptox"
	"github.com/mindmons/integration-hub/internal/httpx"
	"github.com/mindmons/integration-hub/internal/metrics"
	"github.com/mindmons/integration-hub/internal/ratelimit"
	"github.com/mindmons/integration-hub/internal/redisx"
)

// Server, admin-api'nin tüm bağımlılıklarını taşır.
type Server struct {
	cfg     Config
	log     *slog.Logger
	store   *Store
	pool    *pgxpool.Pool
	rdb     *redis.Client
	tokens  *auth.TokenManager
	sealer  *cryptox.Sealer
	limiter *ratelimit.Limiter
	metrics *metrics.Registry

	argonParams auth.Argon2Params

	// Kullanıcı bulunamadığında zamanlama saldırısını önleyen sahte hash.
	dummyOnce      sync.Once
	dummyHashValue string

	// Servise özel metrikler
	mAuthFailures  *prometheusCounter
	mAuditWrites   *prometheusCounter
	mReplayRequest *prometheusCounter
}

// prometheusCounter, tek etiketli sayaçlar için ince sarmalayıcı.
type prometheusCounter struct {
	inc func(labels ...string)
}

func (c *prometheusCounter) Inc(labels ...string) {
	if c != nil && c.inc != nil {
		c.inc(labels...)
	}
}

// Routes, HTTP yönlendirme tablosunu kurar.
func (s *Server) Routes(health *httpx.Health) http.Handler {
	r := httpx.NewRouter(s.log, s.metrics, s.cfg.MaxBodyBytes)
	r.Use(httpx.CORS(s.cfg.CORSOrigins))

	httpx.MountObservability(r, s.metrics, health)

	// OpenAPI kontratı ve Swagger UI (FR: /docs)
	r.Get("/openapi.yaml", s.handleOpenAPISpec)
	r.Get("/docs", s.handleSwaggerUI)

	r.Route("/api/v1", func(api chi.Router) {
		// --- Herkese açık: kimlik doğrulama --------------------------------
		api.Group(func(pub chi.Router) {
			pub.Use(s.rateLimitByIP)
			pub.Post("/auth/login", s.handleLogin)
			pub.Post("/auth/refresh", s.handleRefresh)
		})

		// --- Korumalı: JWT gerekli -----------------------------------------
		api.Group(func(priv chi.Router) {
			priv.Use(s.requireAuth)
			priv.Use(s.rateLimitByUser)

			priv.Post("/auth/logout", s.handleLogout)
			priv.Get("/auth/me", s.handleMe)
			priv.Post("/auth/change-password", s.handleChangePassword)

			// Yıkıcı ve güvenlik-kritik işlemler ayrıca rol kontrolünden
			// geçer. Şu an yalnızca `admin` rolü var, ancak `operator` ve
			// `viewer` rolleri şemada tanımlı — bu ayrım sayesinde ileride
			// salt-okunur bir operatör rolü eklemek tek satırlık bir değişiklik.
			adminOnly := s.requireRole("admin")

			// Tool yönetimi + API key
			priv.Get("/tools", s.handleListTools)
			priv.Post("/tools", s.handleCreateTool)
			priv.Get("/tools/{id}", s.handleGetTool)
			priv.Put("/tools/{id}", s.handleUpdateTool)
			priv.With(adminOnly).Delete("/tools/{id}", s.handleDeleteTool)
			priv.Get("/tools/{id}/keys", s.handleListKeys)
			priv.With(adminOnly).Post("/tools/{id}/keys", s.handleIssueKey)
			priv.With(adminOnly).Post("/tools/{id}/keys/rotate", s.handleRotateKey)
			priv.With(adminOnly).Delete("/tools/{id}/keys/{keyId}", s.handleRevokeKey)

			// Event tipleri
			priv.Get("/event-types", s.handleListEventTypes)
			priv.Post("/event-types", s.handleCreateEventType)
			priv.Get("/event-types/{id}", s.handleGetEventType)
			priv.Put("/event-types/{id}", s.handleUpdateEventType)
			priv.With(adminOnly).Delete("/event-types/{id}", s.handleDeleteEventType)

			// Abonelikler
			priv.Get("/subscriptions", s.handleListSubscriptions)
			priv.Post("/subscriptions", s.handleCreateSubscription)
			priv.Get("/subscriptions/{id}", s.handleGetSubscription)
			priv.Put("/subscriptions/{id}", s.handleUpdateSubscription)
			priv.With(adminOnly).Delete("/subscriptions/{id}", s.handleDeleteSubscription)

			// Yönlendirme kuralları
			priv.Get("/routing-rules", s.handleListRules)
			priv.Post("/routing-rules", s.handleCreateRule)
			priv.Get("/routing-rules/{id}", s.handleGetRule)
			priv.Put("/routing-rules/{id}", s.handleUpdateRule)
			priv.With(adminOnly).Delete("/routing-rules/{id}", s.handleDeleteRule)
			priv.Post("/routing-rules/test", s.handleTestRule)

			// Operasyon
			priv.Get("/events", s.handleListEvents)
			priv.Get("/events/{id}", s.handleGetEvent)
			priv.Get("/delivery-attempts", s.handleListAttempts)
			priv.Get("/dlq", s.handleListDLQ)
			priv.Get("/dlq/{id}", s.handleGetDLQ)
			priv.Post("/dlq/replay", s.handleReplayDLQ)
			priv.With(adminOnly).Post("/dlq/discard", s.handleDiscardDLQ)

			priv.Get("/audit-logs", s.handleListAudit)
			priv.Get("/stats", s.handleStats)
			priv.Get("/stream/events", s.handleEventStream)
		})
	})

	return r
}

// --- Ortak yardımcılar --------------------------------------------------------

// pagination, limit/offset sorgu parametrelerini güvenli aralıkta okur.
func pagination(r *http.Request) (limit, offset int) {
	limit = 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	// Üst sınır: tek istekle tüm tabloyu çekip belleği tüketmek engellenir.
	if limit > 500 {
		limit = 500
	}

	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

func q(r *http.Request, key string) string {
	return strings.TrimSpace(r.URL.Query().Get(key))
}

// writeList, liste yanıtlarını tek biçimli sarmalar.
func writeList[T any](w http.ResponseWriter, items []T, total, limit, offset int) {
	if items == nil {
		items = []T{}
	}
	httpx.JSON(w, http.StatusOK, ListResponse[T]{
		Items: items,
		Meta:  ListMeta{Total: total, Limit: limit, Offset: offset},
	})
}

// audit, denetim kaydını yazar; hata ana akışı bozmaz, yalnızca loglanır.
func (s *Server) audit(ctx context.Context, r *http.Request, action, resourceType, resourceID string, before, after any) {
	user := userFrom(ctx)

	entry := AuditEntry{
		ActorType:    "admin",
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Before:       before,
		After:        after,
		IP:           httpx.ClientIP(r),
		UserAgent:    r.UserAgent(),
		TraceID:      httpx.TraceIDFromRequest(r),
	}
	if user != nil {
		entry.ActorID = user.UserID
		entry.ActorLabel = user.Email
	}

	if err := s.store.WriteAudit(ctx, entry); err != nil {
		s.log.ErrorContext(ctx, "denetim kaydı yazılamadı",
			slog.String("action", action), slog.String("err", err.Error()))
		s.mAuditWrites.Inc("error")
		return
	}
	s.mAuditWrites.Inc("ok")
}

// bumpRulesGeneration, routing-engine'in kural önbelleğini geçersiz kılar.
//
// Neden "generation" sayacı, doğrudan anahtar silme değil? Kurallar event tipi
// bazında önbelleklenir; bir aboneliği değiştirmek hangi tipleri etkilediğini
// admin-api'de yeniden hesaplamayı gerektirirdi. Tek bir sayacı artırmak
// O(1)'dir ve tüm consumer'lar bir sonraki kontrolde önbelleklerini tazeler.
// TTL ise ikinci güvenlik ağıdır: Redis silinse bile kurallar en geç TTL
// kadar sonra tazelenir.
func (s *Server) bumpRulesGeneration(ctx context.Context) {
	if s.rdb == nil {
		return
	}
	if err := s.rdb.Incr(ctx, redisx.Key(redisx.NSRules, "gen")).Err(); err != nil {
		// Redis yoksa kurallar TTL dolduğunda yine tazelenir — akış durmaz.
		s.log.WarnContext(ctx, "kural önbelleği geçersiz kılınamadı (TTL ile tazelenecek)",
			slog.String("err", err.Error()))
	}
}
