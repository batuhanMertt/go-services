package main

import (
	"encoding/json"
	"time"
)

// --- Kimlik ------------------------------------------------------------------

type AdminUser struct {
	ID           string     `json:"id"`
	Email        string     `json:"email"`
	DisplayName  string     `json:"display_name"`
	Role         string     `json:"role"`
	IsActive     bool       `json:"is_active"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	passwordHash string     // asla JSON'a serileştirilmez
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type TokenResponse struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	TokenType    string    `json:"token_type"`
	ExpiresAt    time.Time `json:"expires_at"`
	User         AdminUser `json:"user"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

type ChangePasswordRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

// --- Katalog ------------------------------------------------------------------

type Tool struct {
	ID           string         `json:"id"`
	Slug         string         `json:"slug"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Kind         string         `json:"kind"`
	Status       string         `json:"status"`
	ContactEmail string         `json:"contact_email"`
	Metadata     map[string]any `json:"metadata"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type ToolRequest struct {
	Slug         string         `json:"slug"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Kind         string         `json:"kind"`
	Status       string         `json:"status"`
	ContactEmail string         `json:"contact_email"`
	Metadata     map[string]any `json:"metadata"`
}

// APIKeyInfo, anahtarın güvenli (plaintext içermeyen) gösterimi.
type APIKeyInfo struct {
	ID         string     `json:"id"`
	ToolID     string     `json:"tool_id"`
	KeyPrefix  string     `json:"key_prefix"`
	Status     string     `json:"status"`
	GraceUntil *time.Time `json:"grace_until,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// APIKeyIssued, anahtar üretimi/rotasyonu sonrası TEK SEFERLİK yanıt.
// Plaintext ve HMAC secret yalnızca burada döner; sonra bir daha asla.
type APIKeyIssued struct {
	APIKeyInfo
	APIKey     string `json:"api_key"`
	HMACSecret string `json:"hmac_secret"`
	Warning    string `json:"warning"`
}

type RotateKeyRequest struct {
	// GracePeriod, eski anahtarın ne kadar süre daha geçerli kalacağı.
	// Boş bırakılırsa 24 saat kullanılır.
	GracePeriod string `json:"grace_period,omitempty"`
}

type EventType struct {
	ID          string          `json:"id"`
	OwnerToolID *string         `json:"owner_tool_id,omitempty"`
	Name        string          `json:"name"`
	Version     int             `json:"version"`
	Description string          `json:"description"`
	JSONSchema  json.RawMessage `json:"json_schema"`
	IsActive    bool            `json:"is_active"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type EventTypeRequest struct {
	OwnerToolID *string         `json:"owner_tool_id,omitempty"`
	Name        string          `json:"name"`
	Version     int             `json:"version"`
	Description string          `json:"description"`
	JSONSchema  json.RawMessage `json:"json_schema"`
	IsActive    *bool           `json:"is_active,omitempty"`
}

type Subscription struct {
	ID               string            `json:"id"`
	SubscriberToolID string            `json:"subscriber_tool_id"`
	SubscriberSlug   string            `json:"subscriber_slug,omitempty"`
	EventTypeID      string            `json:"event_type_id"`
	EventTypeName    string            `json:"event_type_name,omitempty"`
	EventTypeVersion int               `json:"event_type_version,omitempty"`
	EndpointURL      string            `json:"endpoint_url"`
	HTTPHeaders      map[string]string `json:"http_headers"`
	MaxRetries       int               `json:"max_retries"`
	TimeoutMS        int               `json:"timeout_ms"`
	IsActive         bool              `json:"is_active"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
}

type SubscriptionRequest struct {
	SubscriberToolID string            `json:"subscriber_tool_id"`
	EventTypeID      string            `json:"event_type_id"`
	EndpointURL      string            `json:"endpoint_url"`
	HTTPHeaders      map[string]string `json:"http_headers"`
	MaxRetries       *int              `json:"max_retries,omitempty"`
	TimeoutMS        *int              `json:"timeout_ms,omitempty"`
	IsActive         *bool             `json:"is_active,omitempty"`
}

type RoutingRule struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	SubscriptionID string          `json:"subscription_id"`
	Condition      json.RawMessage `json:"condition"`
	Priority       int             `json:"priority"`
	IsActive       bool            `json:"is_active"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	// Okuma kolaylığı için ilişkili alanlar
	EventTypeName string `json:"event_type_name,omitempty"`
	EndpointURL   string `json:"endpoint_url,omitempty"`
}

type RoutingRuleRequest struct {
	Name           string          `json:"name"`
	SubscriptionID string          `json:"subscription_id"`
	Condition      json.RawMessage `json:"condition"`
	Priority       *int            `json:"priority,omitempty"`
	IsActive       *bool           `json:"is_active,omitempty"`
}

// --- Operasyon ----------------------------------------------------------------

type EventInstance struct {
	ID           string          `json:"id"`
	EventID      string          `json:"event_id"`
	EventType    string          `json:"event_type"`
	EventVersion int             `json:"event_version"`
	SourceTool   string          `json:"source_tool"`
	Payload      json.RawMessage `json:"payload"`
	Metadata     json.RawMessage `json:"metadata"`
	TraceID      string          `json:"trace_id"`
	Status       string          `json:"status"`
	ReceivedAt   time.Time       `json:"received_at"`
	RoutedAt     *time.Time      `json:"routed_at,omitempty"`
	DeliveryLine string          `json:"delivery_summary,omitempty"`
}

type DeliveryAttempt struct {
	ID             int64      `json:"id"`
	TaskID         string     `json:"delivery_task_id"`
	EventID        string     `json:"event_id"`
	EventType      string     `json:"event_type"`
	SubscriberSlug string     `json:"subscriber_slug"`
	TargetURL      string     `json:"target_url"`
	AttemptNo      int        `json:"attempt_no"`
	ResponseStatus *int       `json:"response_status,omitempty"`
	ResponseBody   string     `json:"response_body,omitempty"`
	LatencyMS      int        `json:"latency_ms"`
	Error          string     `json:"error,omitempty"`
	AttemptedAt    time.Time  `json:"attempted_at"`
	TaskStatus     string     `json:"task_status,omitempty"`
	NextAttemptAt  *time.Time `json:"next_attempt_at,omitempty"`
}

type DLQEvent struct {
	ID             string          `json:"id"`
	TaskID         *string         `json:"delivery_task_id,omitempty"`
	SubscriptionID *string         `json:"subscription_id,omitempty"`
	SubscriberSlug string          `json:"subscriber_slug,omitempty"`
	EventID        string          `json:"event_id"`
	EventType      string          `json:"event_type"`
	TargetURL      string          `json:"target_url"`
	Reason         string          `json:"reason"`
	Attempts       int             `json:"attempts"`
	Payload        json.RawMessage `json:"payload"`
	Status         string          `json:"status"`
	CreatedAt      time.Time       `json:"created_at"`
	ReplayedAt     *time.Time      `json:"replayed_at,omitempty"`
}

type ReplayRequest struct {
	// IDs boşsa ve SubscriptionID doluysa, o abonelik için toplu replay yapılır.
	IDs            []string `json:"ids,omitempty"`
	SubscriptionID string   `json:"subscription_id,omitempty"`
	Limit          int      `json:"limit,omitempty"`
}

type ReplayResponse struct {
	Requested int      `json:"requested"`
	Replayed  int      `json:"replayed"`
	Skipped   int      `json:"skipped"`
	Errors    []string `json:"errors,omitempty"`
}

type AuditLog struct {
	ID           int64           `json:"id"`
	ActorType    string          `json:"actor_type"`
	ActorID      string          `json:"actor_id"`
	ActorLabel   string          `json:"actor_label"`
	Action       string          `json:"action"`
	ResourceType string          `json:"resource_type"`
	ResourceID   string          `json:"resource_id"`
	BeforeState  json.RawMessage `json:"before_state,omitempty"`
	AfterState   json.RawMessage `json:"after_state,omitempty"`
	IP           string          `json:"ip"`
	UserAgent    string          `json:"user_agent"`
	TraceID      string          `json:"trace_id"`
	CreatedAt    time.Time       `json:"created_at"`
}

// DashboardStats, Admin UI ana ekranının özet verisi.
type DashboardStats struct {
	Tools struct {
		Total  int `json:"total"`
		Active int `json:"active"`
	} `json:"tools"`
	Subscriptions struct {
		Total  int `json:"total"`
		Active int `json:"active"`
	} `json:"subscriptions"`
	Events struct {
		Last24h int `json:"last_24h"`
		LastHr  int `json:"last_hour"`
		Total   int `json:"total"`
	} `json:"events"`
	Deliveries struct {
		Succeeded24h int     `json:"succeeded_24h"`
		Failed24h    int     `json:"failed_24h"`
		Pending      int     `json:"pending"`
		SuccessRate  float64 `json:"success_rate"`
	} `json:"deliveries"`
	DLQ struct {
		Pending  int `json:"pending"`
		Total    int `json:"total"`
		Replayed int `json:"replayed"`
	} `json:"dlq"`
	Outbox struct {
		Pending int `json:"pending"`
	} `json:"outbox"`
	EventsPerType  []NameCount `json:"events_per_type"`
	EventsTimeline []TimeCount `json:"events_timeline"`
	TopSubscribers []NameCount `json:"top_subscribers"`
	GeneratedAt    time.Time   `json:"generated_at"`
}

type NameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

type TimeCount struct {
	Bucket time.Time `json:"bucket"`
	Count  int       `json:"count"`
}

// ListMeta, sayfalama üst verisi.
type ListMeta struct {
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

// ListResponse, tüm liste uçlarının ortak sarmalayıcısı.
type ListResponse[T any] struct {
	Items []T      `json:"items"`
	Meta  ListMeta `json:"meta"`
}
