package model

import (
	"encoding/json"
	"testing"
	"time"
)

func validEnvelope() Envelope {
	return Envelope{
		EventID:      "3f9a1c72-5b0e-4a1d-9f6c-7c2e8d5b1a34",
		EventType:    "user.created",
		EventVersion: 1,
		SourceTool:   "mock-publisher-1",
		Timestamp:    time.Now().UTC(),
		TraceID:      "trace-1",
		Payload:      json.RawMessage(`{"user_id":"u1"}`),
	}
}

func TestValidateAcceptsValidEnvelope(t *testing.T) {
	env := validEnvelope()

	if problems := env.Validate(); problems != nil {
		t.Fatalf("geçerli zarf reddedildi: %v", problems)
	}
}

func TestValidateReportsPerFieldProblems(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*Envelope)
		wantField string
	}{
		{"event_id boş", func(e *Envelope) { e.EventID = "" }, "event_id"},
		{"event_id çok kısa", func(e *Envelope) { e.EventID = "abc" }, "event_id"},
		{"event_id geçersiz karakter", func(e *Envelope) { e.EventID = "abc def!@#" }, "event_id"},
		{"event_type boş", func(e *Envelope) { e.EventType = "" }, "event_type"},
		{"event_type büyük harf", func(e *Envelope) { e.EventType = "User.Created" }, "event_type"},
		{"event_type noktasız", func(e *Envelope) { e.EventType = "usercreated" }, "event_type"},
		{"event_version sıfır", func(e *Envelope) { e.EventVersion = 0 }, "event_version"},
		{"event_version negatif", func(e *Envelope) { e.EventVersion = -1 }, "event_version"},
		{"source_tool boş", func(e *Envelope) { e.SourceTool = "" }, "source_tool"},
		{"timestamp yok", func(e *Envelope) { e.Timestamp = time.Time{} }, "timestamp"},
		{"payload boş", func(e *Envelope) { e.Payload = nil }, "payload"},
		{"payload bozuk JSON", func(e *Envelope) { e.Payload = json.RawMessage(`{bozuk`) }, "payload"},
		{"payload dizi", func(e *Envelope) { e.Payload = json.RawMessage(`[1,2,3]`) }, "payload"},
		{"payload skaler", func(e *Envelope) { e.Payload = json.RawMessage(`"metin"`) }, "payload"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnvelope()
			tc.mutate(&env)

			problems := env.Validate()
			if problems == nil {
				t.Fatal("doğrulama hatası bekleniyordu")
			}
			if _, ok := problems[tc.wantField]; !ok {
				t.Fatalf("%q alanı için hata bekleniyordu, alınan: %v", tc.wantField, problems)
			}
		})
	}
}

func TestValidateReportsAllProblemsAtOnce(t *testing.T) {
	// Kullanıcı hataları tek tek değil, hepsi birden görmeli.
	env := Envelope{}

	problems := env.Validate()
	if len(problems) < 5 {
		t.Fatalf("tüm eksik alanlar raporlanmadı: %v", problems)
	}
}

func TestEventTypeNamePattern(t *testing.T) {
	valid := []string{"user.created", "document.generated", "a.b.c.d.e", "invoice.paid_late", "v2.event_type"}
	invalid := []string{"nodot", "Upper.Case", "trailing.", ".leading", "a..b", "a.b.c.d.e.f.g", "with space.x"}

	for _, name := range valid {
		env := validEnvelope()
		env.EventType = name
		if p := env.Validate(); p != nil && p["event_type"] != "" {
			t.Errorf("geçerli ad reddedildi: %s", name)
		}
	}

	for _, name := range invalid {
		env := validEnvelope()
		env.EventType = name
		p := env.Validate()
		if p == nil || p["event_type"] == "" {
			t.Errorf("geçersiz ad kabul edildi: %s", name)
		}
	}
}

func TestKeyFormat(t *testing.T) {
	env := validEnvelope()
	env.EventType = "user.created"
	env.EventVersion = 3

	if got := env.Key(); got != "user.created@3" {
		t.Fatalf("Key() = %q, beklenen user.created@3", got)
	}
	if got := TypeKey("a.b", 2); got != "a.b@2" {
		t.Fatalf("TypeKey() = %q, beklenen a.b@2", got)
	}
}

func TestEnvelopeJSONRoundTrip(t *testing.T) {
	original := validEnvelope()
	original.Metadata = map[string]any{"tenant": "acme"}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var decoded Envelope
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.EventID != original.EventID ||
		decoded.EventType != original.EventType ||
		decoded.SourceTool != original.SourceTool {
		t.Fatal("zarf alanları serileştirmede kaybedildi")
	}
	if string(decoded.Payload) != string(original.Payload) {
		t.Fatalf("payload değişti: %s != %s", decoded.Payload, original.Payload)
	}
}

func TestToWebhookPayload(t *testing.T) {
	sentAt := time.Now().UTC()

	task := DeliveryTask{
		TaskID:         "task-1",
		SubscriptionID: "sub-1",
		MaxRetries:     5,
		Replay:         true,
		Envelope:       validEnvelope(),
	}

	wp := task.ToWebhookPayload(3, sentAt)

	if wp.EventID != task.Envelope.EventID {
		t.Error("event_id taşınmadı")
	}
	if wp.Delivery.DeliveryID != "task-1" {
		t.Error("delivery_id taşınmadı")
	}
	if wp.Delivery.Attempt != 3 {
		t.Errorf("attempt = %d, beklenen 3", wp.Delivery.Attempt)
	}
	if wp.Delivery.MaxRetries != 5 {
		t.Errorf("max_retries = %d, beklenen 5", wp.Delivery.MaxRetries)
	}
	if !wp.Delivery.Replay {
		t.Error("replay bayrağı taşınmadı")
	}
	if !wp.Delivery.SentAt.Equal(sentAt) {
		t.Error("sent_at taşınmadı")
	}

	// Subscriber'ın gördüğü gövde geçerli JSON olmalı.
	if _, err := json.Marshal(wp); err != nil {
		t.Fatalf("webhook gövdesi serileştirilemedi: %v", err)
	}
}
