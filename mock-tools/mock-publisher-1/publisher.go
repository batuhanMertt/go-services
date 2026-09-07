package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/mindmons/integration-hub/internal/cryptox"
	"github.com/mindmons/integration-hub/internal/httpx"
	"github.com/mindmons/integration-hub/internal/model"
)

// supportedTypes, bu mock tool'un üretebildiği event tipleri.
// Şemaları migrations/0004_seed_event_types.up.sql ile birebir uyumludur.
var supportedTypes = []string{"user.created", "document.generated", "verification.completed"}

func randomEventType() string {
	return supportedTypes[rand.Intn(len(supportedTypes))] //nolint:gosec
}

// samplePayload, event tipine uygun gerçekçi bir gövde üretir.
func samplePayload(eventType string) map[string]any {
	switch eventType {
	case "user.created":
		return map[string]any{
			"user_id":    "usr_" + uuid.NewString()[:8],
			"email":      fmt.Sprintf("user%d@mindmons.local", rand.Intn(100000)), //nolint:gosec
			"full_name":  pick([]string{"Ada Lovelace", "Alan Turing", "Grace Hopper", "Edsger Dijkstra"}),
			"status":     pick([]string{"active", "pending", "active", "active"}),
			"tenant_id":  pick([]string{"tenant-a", "tenant-b"}),
			"created_at": time.Now().UTC().Format(time.RFC3339),
		}

	case "document.generated":
		return map[string]any{
			"document_id":   "doc_" + uuid.NewString()[:8],
			"document_type": pick([]string{"invoice", "contract", "report", "certificate"}),
			"owner_user_id": "usr_" + uuid.NewString()[:8],
			"status":        pick([]string{"draft", "final", "final"}),
			"size_bytes":    rand.Intn(5_000_000), //nolint:gosec
			"storage_url":   "https://storage.mindmons.local/documents/" + uuid.NewString(),
		}

	default: // verification.completed
		return map[string]any{
			"verification_id": "ver_" + uuid.NewString()[:8],
			"subject_id":      "usr_" + uuid.NewString()[:8],
			"subject_type":    pick([]string{"user", "document", "transaction"}),
			"result":          pick([]string{"passed", "passed", "failed", "manual_review"}),
			"score":           float64(rand.Intn(101)), //nolint:gosec
			"regulation":      pick([]string{"KVKK", "GDPR", "AML"}),
		}
	}
}

func pick(options []string) string {
	return options[rand.Intn(len(options))] //nolint:gosec
}

// sendEvent, tek bir event'i imzalayıp platforma gönderir.
//
// forceEventID doluysa aynı event_id ile gönderilir — idempotency testini
// UI'dan tetiklemek için kullanılır.
func (s *Server) sendEvent(ctx context.Context, eventType string, forceEventID *string) (*SendRecord, error) {
	creds := s.credentials()
	if creds == nil {
		return nil, fmt.Errorf("henüz platforma kaydolunmadı")
	}

	eventID := uuid.NewString()
	if forceEventID != nil && *forceEventID != "" {
		eventID = *forceEventID
	}

	payload, err := json.Marshal(samplePayload(eventType))
	if err != nil {
		return nil, err
	}

	traceID := uuid.NewString()

	envelope := model.Envelope{
		EventID:      eventID,
		EventType:    eventType,
		EventVersion: 1,
		SourceTool:   creds.ToolSlug,
		Timestamp:    time.Now().UTC(),
		TraceID:      traceID,
		Payload:      payload,
		Metadata: map[string]any{
			"emitted_by": "mock-publisher-1",
			"env":        s.cfg.Base.Env,
		},
	}

	body, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}

	// İmza, gövdenin TAM olarak gönderilen hali üzerinden hesaplanır.
	// Gövdeyi burada üretip aynı byte dizisini göndermek şart; yeniden
	// serileştirme alan sırasını değiştirip imzayı bozardı.
	timestamp := cryptox.NowTimestamp()
	signature := cryptox.Sign([]byte(creds.HMACSecret), timestamp, body)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.IngestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", creds.APIKey)
	req.Header.Set("X-Signature-SHA256", signature)
	req.Header.Set("X-Timestamp", timestamp)
	req.Header.Set(httpx.HeaderTraceID, traceID)

	start := time.Now()
	resp, err := s.client.Do(req)
	latency := time.Since(start)
	s.mLatency.Observe(latency.Seconds())

	rec := SendRecord{
		At:        time.Now().UTC(),
		EventID:   eventID,
		EventType: eventType,
		LatencyMS: int(latency.Milliseconds()),
	}

	if err != nil {
		rec.Error = err.Error()
		s.mSent.WithLabelValues(eventType, "error").Inc()
		s.recordSend(rec)
		return &rec, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	rec.Status = resp.StatusCode
	rec.Response = string(raw)
	rec.Duplicate = resp.Header.Get("X-Idempotent-Replay") == "true"

	switch {
	case rec.Duplicate:
		s.mSent.WithLabelValues(eventType, "duplicate").Inc()
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		s.mSent.WithLabelValues(eventType, "accepted").Inc()
	case resp.StatusCode == http.StatusTooManyRequests:
		s.mSent.WithLabelValues(eventType, "rate_limited").Inc()
	default:
		s.mSent.WithLabelValues(eventType, "rejected").Inc()
	}

	s.recordSend(rec)
	return &rec, nil
}
