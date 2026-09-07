// Package model, servisler arasında Kafka üzerinden taşınan mesaj
// sözleşmelerini tanımlar.
//
// Bu tipler docs/api-contracts/events/envelope.schema.json ile birebir
// uyumludur. Kontrat önce yazıldı, kod sonra (contract-first).
package model

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Ortak zarf (envelope) alanları — her event tipinde aynıdır. Payload ise
// event tipine özgüdür ve JSON Schema ile doğrulanır.
//
// Neden zarf/payload ayrımı? Routing engine, delivery worker ve DLQ handler
// payload'ın İÇERİĞİNİ bilmeden çalışabilmelidir. Zarf, altyapının ihtiyaç
// duyduğu tüm metadatayı taşır; payload domain'e aittir. Yeni bir event tipi
// eklemek hiçbir servisin kodunu değiştirmez (tak-çıkart mimarisi).
type Envelope struct {
	// EventID, publisher tarafından üretilir (UUID v4) ve idempotency anahtarıdır.
	EventID string `json:"event_id"`
	// EventType, "user.created" gibi noktalı ad.
	EventType string `json:"event_type"`
	// EventVersion, şema versiyonu (v1, v2 …).
	EventVersion int `json:"event_version"`
	// SourceTool, event'i yayınlayan tool'un slug'ı.
	SourceTool string `json:"source_tool"`
	// Timestamp, olayın kaynak sistemde gerçekleştiği an.
	Timestamp time.Time `json:"timestamp"`
	// TraceID, uçtan uca izleme kimliği; verilmezse platform üretir.
	TraceID string `json:"trace_id,omitempty"`
	// Payload, event tipine özgü gövde.
	Payload json.RawMessage `json:"payload"`
	// Metadata, opsiyonel serbest alan (korelasyon id'leri, tenant vb.).
	Metadata map[string]any `json:"metadata,omitempty"`
}

var (
	eventIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._:-]{8,128}$`)
	eventTypePattern = regexp.MustCompile(`^[a-z0-9]+(\.[a-z0-9_]+){1,4}$`)
)

// ErrInvalidEnvelope, zarf doğrulama hatası.
var ErrInvalidEnvelope = errors.New("event zarfı geçersiz")

// Validate, zarfın yapısal geçerliliğini kontrol eder (payload şeması ayrı).
// Alan bazlı hata haritası döner; istemci hangi alanın hatalı olduğunu görür.
func (e *Envelope) Validate() map[string]string {
	problems := map[string]string{}

	if e.EventID == "" {
		problems["event_id"] = "zorunlu"
	} else if !eventIDPattern.MatchString(e.EventID) {
		problems["event_id"] = "8-128 karakter, yalnızca harf/rakam/._:- kullanılabilir"
	}

	if e.EventType == "" {
		problems["event_type"] = "zorunlu"
	} else if !eventTypePattern.MatchString(e.EventType) {
		problems["event_type"] = "küçük harfle noktalı ad bekleniyor (örn. user.created)"
	}

	if e.EventVersion <= 0 {
		problems["event_version"] = "1 veya daha büyük olmalı"
	}

	if e.SourceTool == "" {
		problems["source_tool"] = "zorunlu"
	}

	if e.Timestamp.IsZero() {
		problems["timestamp"] = "zorunlu (RFC3339)"
	}

	if len(e.Payload) == 0 {
		problems["payload"] = "zorunlu"
	} else if !json.Valid(e.Payload) {
		problems["payload"] = "geçerli JSON olmalı"
	} else if strings.TrimSpace(string(e.Payload))[0] != '{' {
		problems["payload"] = "JSON nesnesi olmalı"
	}

	if len(problems) == 0 {
		return nil
	}
	return problems
}

// Key, event tipi + versiyon birleşimi ("user.created@1").
func (e *Envelope) Key() string {
	return fmt.Sprintf("%s@%d", e.EventType, e.EventVersion)
}

// TypeKey, verilen ad ve versiyondan anahtar üretir.
func TypeKey(name string, version int) string {
	return fmt.Sprintf("%s@%d", name, version)
}

// IngestedEvent, `events.ingested` topic'ine basılan mesaj.
// Zarfa ek olarak platformun atadığı kimlikleri taşır.
type IngestedEvent struct {
	// EventInstanceID, event_instances tablosundaki birincil anahtar.
	EventInstanceID string `json:"event_instance_id"`
	// SourceToolID, kaydolmuş tool'un UUID'si.
	SourceToolID string `json:"source_tool_id"`
	// ReceivedAt, platformun event'i kabul ettiği an; uçtan uca gecikme
	// ölçümünün başlangıç noktasıdır.
	ReceivedAt time.Time `json:"received_at"`
	Envelope   Envelope  `json:"envelope"`
}

// DeliveryTask, `events.delivery` topic'ine basılan teslimat görevi.
type DeliveryTask struct {
	TaskID          string            `json:"task_id"`
	EventInstanceID string            `json:"event_instance_id"`
	SubscriptionID  string            `json:"subscription_id"`
	SubscriberSlug  string            `json:"subscriber_slug"`
	TargetURL       string            `json:"target_url"`
	ExtraHeaders    map[string]string `json:"extra_headers,omitempty"`
	MaxRetries      int               `json:"max_retries"`
	TimeoutMS       int               `json:"timeout_ms"`
	// Attempt, bu mesajın kaçıncı deneme olduğu (retry ve replay'de artar).
	Attempt int `json:"attempt"`
	// ReceivedAt, orijinal event'in ingest zamanı (e2e latency için taşınır).
	ReceivedAt time.Time `json:"received_at"`
	// Replay true ise mesaj DLQ'dan yeniden gönderilmiştir.
	Replay   bool     `json:"replay,omitempty"`
	Envelope Envelope `json:"envelope"`
}

// DLQMessage, `events.dlq` topic'ine basılan ölü mesaj.
type DLQMessage struct {
	TaskID          string    `json:"task_id"`
	EventInstanceID string    `json:"event_instance_id"`
	SubscriptionID  string    `json:"subscription_id"`
	TargetURL       string    `json:"target_url"`
	Reason          string    `json:"reason"`
	Attempts        int       `json:"attempts"`
	FailedAt        time.Time `json:"failed_at"`
	Envelope        Envelope  `json:"envelope"`
}

// WebhookPayload, subscriber'a POST edilen gövde.
//
// Zarf ile aynı alanları taşır, üstüne teslimat bağlamını ekler. Böylece
// subscriber "bu kaçıncı deneme" ve "hangi abonelik" bilgisine gövdeden de
// erişebilir (header'lara ek olarak).
type WebhookPayload struct {
	EventID      string          `json:"event_id"`
	EventType    string          `json:"event_type"`
	EventVersion int             `json:"event_version"`
	SourceTool   string          `json:"source_tool"`
	Timestamp    time.Time       `json:"timestamp"`
	TraceID      string          `json:"trace_id"`
	Payload      json.RawMessage `json:"payload"`
	Metadata     map[string]any  `json:"metadata,omitempty"`
	Delivery     DeliveryInfo    `json:"delivery"`
}

// DeliveryInfo, webhook gövdesindeki teslimat bağlamı.
type DeliveryInfo struct {
	DeliveryID     string    `json:"delivery_id"`
	SubscriptionID string    `json:"subscription_id"`
	Attempt        int       `json:"attempt"`
	MaxRetries     int       `json:"max_retries"`
	SentAt         time.Time `json:"sent_at"`
	Replay         bool      `json:"replay"`
}

// ToWebhookPayload, teslimat görevinden webhook gövdesi üretir.
func (t *DeliveryTask) ToWebhookPayload(attempt int, sentAt time.Time) WebhookPayload {
	return WebhookPayload{
		EventID:      t.Envelope.EventID,
		EventType:    t.Envelope.EventType,
		EventVersion: t.Envelope.EventVersion,
		SourceTool:   t.Envelope.SourceTool,
		Timestamp:    t.Envelope.Timestamp,
		TraceID:      t.Envelope.TraceID,
		Payload:      t.Envelope.Payload,
		Metadata:     t.Envelope.Metadata,
		Delivery: DeliveryInfo{
			DeliveryID:     t.TaskID,
			SubscriptionID: t.SubscriptionID,
			Attempt:        attempt,
			MaxRetries:     t.MaxRetries,
			SentAt:         sentAt,
			Replay:         t.Replay,
		},
	}
}
