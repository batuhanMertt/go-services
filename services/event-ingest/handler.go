package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/mindmons/integration-hub/internal/cryptox"
	"github.com/mindmons/integration-hub/internal/httpx"
	"github.com/mindmons/integration-hub/internal/model"
	"github.com/mindmons/integration-hub/internal/ratelimit"
	"github.com/mindmons/integration-hub/internal/schema"
)

// adaptiveIPRule, saldırı modunda IP rate limitini sıkılaştırır. Detector
// global istek hacmine bakar; ratelimit.Limiter tek bir IP'ye. İkisi
// birlikte "saldırı sırasında herkesi eşit kesme, şüpheli hacmi kısıtla"
// davranışını verir (bkz. internal/ddos paket dokümantasyonu).
func adaptiveIPRule(base ratelimit.Rule, mult float64) ratelimit.Rule {
	if mult >= 1.0 {
		return base
	}
	rule := ratelimit.Rule{Rate: base.Rate * mult, Burst: int(float64(base.Burst) * mult)}
	if rule.Burst < 1 {
		rule.Burst = 1
	}
	return rule
}

// İstek başlıkları — kontrat docs/api-contracts/event-ingest.openapi.yaml.
const (
	HeaderAPIKey    = "X-Api-Key"
	HeaderSignature = "X-Signature-SHA256"
	HeaderTimestamp = "X-Timestamp"
)

// IngestResponse, başarılı kabul yanıtı.
type IngestResponse struct {
	EventID         string    `json:"event_id"`
	EventInstanceID string    `json:"event_instance_id"`
	Status          string    `json:"status"`
	TraceID         string    `json:"trace_id"`
	ReceivedAt      time.Time `json:"received_at"`
}

func typeKey(name string, version int) string {
	return name + "@" + strconv.Itoa(version)
}

// handleIngest, publisher tool'lardan event kabul eder (FR-2.1).
//
// Doğrulama sırası bilinçlidir ve UCUZDAN PAHALIYA gider:
//  0. IP ban kontrolü (Redis EXISTS — Lua script'ten bile daha ucuz)
//  1. IP rate limit  (Redis, DB'ye hiç gitmeden kötü niyetli trafiği keser;
//     Detector saldırı modundaysa kural otomatik sıkılaştırılır)
//  2. Başlık kontrolü (bellekte)
//  3. API key doğrulama (önbellekli, gerekirse DB)
//  4. Tool rate limit (kimlik belli olduktan sonra)
//  5. HMAC + zaman damgası (CPU)
//  6. Zarf doğrulama (bellekte)
//  7. JSON Schema (CPU)
//  8. DB yazımı (en pahalı)
//
// Böylece geçersiz bir istek en ucuz adımda reddedilir. 2-3-5 adımlarındaki
// başarısızlıklar guard.Strike ile IP'nin itibarına işlenir: tekrarlayan
// kötü niyetli istekler (geçersiz key, bozuk imza) belirli bir eşikten sonra
// IP'yi geçici olarak banlar (bkz. internal/ddos.Guard).
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	traceID := httpx.TraceIDFromRequest(r)
	receivedAt := time.Now().UTC()
	ip := httpx.ClientIP(r)

	// 0) IP ban kontrolü
	if s.guard.Banned(ctx, ip) {
		s.mRateLimited.WithLabelValues("banned", "").Inc()
		writeRetryAfter(w, time.Minute)
		httpx.Error(w, r, httpx.Forbidden("ip_banned",
			"bu IP tekrarlayan kötü niyetli istekler nedeniyle geçici olarak engellendi"))
		return
	}

	// 1) IP bazlı rate limit — saldırı modunda daraltılmış kural
	ipRule := adaptiveIPRule(ratelimit.Rule{Rate: s.cfg.RateIP.Rate, Burst: s.cfg.RateIP.Burst},
		s.detector.Multiplier())
	if res, err := s.limiter.Allow(ctx, "ip", ip, ipRule); err != nil || !res.Allowed {
		if err != nil {
			s.log.WarnContext(ctx, "ip rate limit kontrolü başarısız", "err", err.Error())
		}
		if !res.Allowed {
			s.mRateLimited.WithLabelValues("ip", "").Inc()
			writeRetryAfter(w, res.RetryAfter)
			httpx.Error(w, r, httpx.TooManyRequests("rate_limited", "IP başına istek limiti aşıldı"))
			return
		}
	}

	// 2) Zorunlu başlıklar
	apiKey := r.Header.Get(HeaderAPIKey)
	signature := r.Header.Get(HeaderSignature)
	timestamp := r.Header.Get(HeaderTimestamp)

	if apiKey == "" {
		s.guard.Strike(ctx, ip)
		s.mIngested.WithLabelValues("", "", "unauthorized").Inc()
		httpx.Error(w, r, httpx.Unauthorized("missing_api_key", HeaderAPIKey+" başlığı zorunlu"))
		return
	}
	if signature == "" || timestamp == "" {
		s.guard.Strike(ctx, ip)
		s.mIngested.WithLabelValues("", "", "unauthorized").Inc()
		httpx.Error(w, r, httpx.Unauthorized("missing_signature",
			HeaderSignature+" ve "+HeaderTimestamp+" başlıkları zorunlu"))
		return
	}

	// Gövde imza doğrulaması için HAM haliyle okunmalı; JSON'a çevirip geri
	// serileştirmek imzayı bozardı (alan sırası/boşluk farkı).
	body, err := io.ReadAll(r.Body)
	if err != nil {
		httpx.Error(w, r, httpx.PayloadTooLarge("body_too_large",
			fmt.Sprintf("istek gövdesi %d byte sınırını aşıyor", s.cfg.MaxBodyBytes)))
		return
	}
	if len(body) == 0 {
		httpx.Error(w, r, httpx.BadRequest("empty_body", "istek gövdesi boş"))
		return
	}

	// 3) API key doğrulama
	tool, err := s.keys.Authenticate(ctx, apiKey)
	if err != nil {
		s.mIngested.WithLabelValues("", "", "unauthorized").Inc()
		s.log.WarnContext(ctx, "api key doğrulanamadı",
			"ip", ip, "err", err.Error())

		switch {
		case errors.Is(err, ErrToolBlocked), errors.Is(err, ErrNotPublisher):
			httpx.Error(w, r, httpx.Forbidden("tool_not_allowed", err.Error()))
		default:
			// Tanınmayan/geçersiz key: credential-guessing sinyali, itibara işlenir.
			// Ayrıntı verilmez: hangi anahtarın var olduğu bilgisi sızmamalı.
			s.guard.Strike(ctx, ip)
			httpx.Error(w, r, httpx.Unauthorized("invalid_api_key", "api key geçersiz"))
		}
		return
	}

	// 4) Tool bazlı rate limit (NFR-3.2)
	if res, err := s.limiter.Allow(ctx, "tool", tool.ToolSlug,
		ratelimit.Rule{Rate: s.cfg.RateTool.Rate, Burst: s.cfg.RateTool.Burst}); err != nil || !res.Allowed {
		if err != nil {
			s.log.WarnContext(ctx, "tool rate limit kontrolü başarısız", "err", err.Error())
		}
		if !res.Allowed {
			s.mRateLimited.WithLabelValues("tool", tool.ToolSlug).Inc()
			writeRetryAfter(w, res.RetryAfter)
			httpx.Error(w, r, httpx.TooManyRequests("rate_limited",
				"tool başına istek limiti aşıldı"))
			return
		}
	}

	// 5) HMAC imza + zaman damgası (FR-2.2, NFR-3.1)
	if err := cryptox.VerifyRequest(tool.HMACSecret, timestamp, body, signature, s.cfg.HMACTolerance); err != nil {
		s.mIngested.WithLabelValues(tool.ToolSlug, "", "bad_signature").Inc()
		s.log.WarnContext(ctx, "imza doğrulanamadı",
			"tool", tool.ToolSlug, "err", err.Error())

		s.guard.Strike(ctx, ip)
		switch {
		case errors.Is(err, cryptox.ErrTimestampExpired), errors.Is(err, cryptox.ErrTimestampInvalid):
			httpx.Error(w, r, httpx.Unauthorized("stale_timestamp",
				"zaman damgası geçersiz veya tolerans penceresi dışında"))
		default:
			httpx.Error(w, r, httpx.Unauthorized("invalid_signature", "imza doğrulanamadı"))
		}
		return
	}

	// 6) Zarf çözümleme ve yapısal doğrulama
	var env model.Envelope
	if err := json.Unmarshal(body, &env); err != nil {
		s.mValidationErrors.WithLabelValues("malformed_json").Inc()
		httpx.Error(w, r, httpx.BadRequest("invalid_json", "gövde geçerli JSON değil"))
		return
	}

	if env.TraceID == "" {
		env.TraceID = traceID
	}
	if env.EventVersion == 0 {
		env.EventVersion = 1
	}

	if problems := env.Validate(); problems != nil {
		s.mValidationErrors.WithLabelValues("envelope").Inc()
		s.mIngested.WithLabelValues(tool.ToolSlug, env.EventType, "invalid").Inc()
		httpx.Error(w, r, httpx.BadRequest("invalid_envelope", "event zarfı geçersiz").WithDetails(problems))
		return
	}

	// Kimlik sahteciliğini engelle: bir tool başka bir tool adına event
	// yayınlayamaz. API key hangi tool'a aitse source_tool o olmalıdır.
	if env.SourceTool != tool.ToolSlug {
		s.mValidationErrors.WithLabelValues("source_mismatch").Inc()
		httpx.Error(w, r, httpx.Forbidden("source_tool_mismatch",
			fmt.Sprintf("source_tool (%q) kimliği doğrulanan tool ile (%q) eşleşmiyor",
				env.SourceTool, tool.ToolSlug)))
		return
	}

	// 7) JSON Schema doğrulaması (FR-2.3)
	key := typeKey(env.EventType, env.EventVersion)
	if err := s.schemas.Validate(key, env.Payload); err != nil {
		var ve *schema.ValidationError

		if errors.Is(err, schema.ErrSchemaNotFound) {
			s.mValidationErrors.WithLabelValues("unknown_event_type").Inc()
			s.mIngested.WithLabelValues(tool.ToolSlug, env.EventType, "unknown_type").Inc()
			httpx.Error(w, r, httpx.BadRequest("unknown_event_type",
				fmt.Sprintf("%q tipi tanımlı değil — admin panelinden event tipi oluşturun", key)))
			return
		}
		if errors.As(err, &ve) {
			s.mValidationErrors.WithLabelValues("schema").Inc()
			s.mIngested.WithLabelValues(tool.ToolSlug, env.EventType, "invalid").Inc()
			httpx.Error(w, r, httpx.BadRequest("schema_validation_failed",
				"payload event şemasına uymuyor").WithDetails(ve.Problems))
			return
		}

		httpx.Error(w, r, httpx.Internal("şema doğrulaması başarısız", err))
		return
	}

	// 8) Kalıcılaştırma + outbox (FR-2.4)
	metaJSON, err := json.Marshal(orEmpty(env.Metadata))
	if err != nil {
		httpx.Error(w, r, httpx.Internal("metadata serileştirilemedi", err))
		return
	}

	ingested := model.IngestedEvent{
		EventInstanceID: uuid.NewString(), // outbox mesajında referans; DB kendi id'sini üretir
		SourceToolID:    tool.ToolID,
		ReceivedAt:      receivedAt,
		Envelope:        env,
	}

	snapshot := IngestResponse{
		EventID:    env.EventID,
		Status:     "accepted",
		TraceID:    env.TraceID,
		ReceivedAt: receivedAt,
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		httpx.Error(w, r, httpx.Internal("yanıt anlık görüntüsü üretilemedi", err))
		return
	}

	kafkaMsg, err := json.Marshal(ingested)
	if err != nil {
		httpx.Error(w, r, httpx.Internal("kafka mesajı üretilemedi", err))
		return
	}

	result, err := s.store.PersistEvent(ctx,
		env.EventID, env.EventType, env.EventVersion,
		tool.ToolID, tool.ToolSlug,
		env.Payload, metaJSON, env.TraceID,
		s.cfg.Infra.Topics.Ingested, kafkaMsg, snapshotJSON,
	)
	if err != nil {
		s.mIngested.WithLabelValues(tool.ToolSlug, env.EventType, "error").Inc()
		httpx.Error(w, r, httpx.Internal("event kaydedilemedi", err))
		return
	}

	// Mükerrer istek: ilk yanıtın AYNISI döner, Kafka'ya tekrar basılmaz (FR-2.5).
	if result.Duplicate {
		s.mIngested.WithLabelValues(tool.ToolSlug, env.EventType, "duplicate").Inc()
		w.Header().Set("X-Idempotent-Replay", "true")

		var stored IngestResponse
		if err := json.Unmarshal(result.Snapshot, &stored); err != nil || stored.EventID == "" {
			stored = snapshot
		}
		stored.EventInstanceID = result.EventInstanceID

		httpx.JSON(w, http.StatusAccepted, stored)
		return
	}

	s.mIngested.WithLabelValues(tool.ToolSlug, env.EventType, "accepted").Inc()

	// Anahtar kullanım zamanını arka planda, örnekleyerek güncelle:
	// her istekte UPDATE yazmak gereksiz DB yükü olurdu.
	s.touchKeyOccasionally(tool.KeyID)

	snapshot.EventInstanceID = result.EventInstanceID
	httpx.JSON(w, http.StatusAccepted, snapshot)
}

func writeRetryAfter(w http.ResponseWriter, d time.Duration) {
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
