// Package toolkit, mock tool'ların platforma kendilerini kaydederken
// kullandığı küçük bir Admin API istemcisidir.
//
// Bu paket, "bir dış geliştirici platforma nasıl bağlanır" sorusunun
// çalıştırılabilir cevabıdır (docs/07-tool-onboarding.md'nin kod karşılığı).
// Gerçek bir tool da tam olarak bu adımları izler.
package toolkit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AdminClient, Admin API'ye JWT ile bağlanan minimal istemci.
type AdminClient struct {
	baseURL string
	http    *http.Client
	token   string
}

func NewAdminClient(baseURL string) *AdminClient {
	return &AdminClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

// WaitReady, Admin API ayağa kalkana kadar bekler.
// Docker Compose healthcheck'i çoğu durumu çözer, ancak uygulama seviyesinde
// beklemek yarış durumlarına karşı ikinci güvencedir.
func (c *AdminClient) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/readyz", nil)
		resp, err := c.http.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("readyz %d döndü", resp.StatusCode)
		} else {
			lastErr = err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("admin-api hazır olmadı: %w", lastErr)
}

// Login, admin kimlik bilgileriyle oturum açar ve token'ı saklar.
func (c *AdminClient) Login(ctx context.Context, email, password string) error {
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/auth/login",
		map[string]string{"email": email, "password": password}, &out); err != nil {
		return fmt.Errorf("admin login başarısız: %w", err)
	}
	if out.AccessToken == "" {
		return fmt.Errorf("admin login yanıtında token yok")
	}
	c.token = out.AccessToken
	return nil
}

// Tool, Admin API'nin döndüğü tool kaydı.
type Tool struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}

// EnsureTool, tool yoksa oluşturur, varsa mevcut kaydı döner (idempotent).
func (c *AdminClient) EnsureTool(ctx context.Context, slug, name, kind, description string) (*Tool, error) {
	var list struct {
		Items []Tool `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/tools?limit=500", nil, &list); err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].Slug == slug {
			return &list.Items[i], nil
		}
	}

	var created Tool
	body := map[string]any{
		"slug": slug, "name": name, "kind": kind, "description": description,
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/tools", body, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// Credentials, üretilen API anahtarı ve HMAC secret.
type Credentials struct {
	APIKey     string `json:"api_key"`
	HMACSecret string `json:"hmac_secret"`
	KeyPrefix  string `json:"key_prefix"`
	ToolID     string `json:"tool_id"`
	ToolSlug   string `json:"tool_slug"`
}

// IssueKey, tool için yeni bir API anahtarı üretir.
// Plaintext yalnızca burada döner; çağıran taraf kalıcı olarak saklamalıdır.
func (c *AdminClient) IssueKey(ctx context.Context, toolID string) (*Credentials, error) {
	var out Credentials
	if err := c.do(ctx, http.MethodPost, "/api/v1/tools/"+toolID+"/keys", map[string]any{}, &out); err != nil {
		return nil, err
	}
	out.ToolID = toolID
	return &out, nil
}

// EventType, kayıtlı event tipi.
type EventType struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// ListEventTypes, tanımlı event tiplerini döner.
func (c *AdminClient) ListEventTypes(ctx context.Context) ([]EventType, error) {
	var list struct {
		Items []EventType `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/event-types?limit=500", nil, &list); err != nil {
		return nil, err
	}
	return list.Items, nil
}

// Subscription, abonelik kaydı.
type Subscription struct {
	ID          string `json:"id"`
	EventTypeID string `json:"event_type_id"`
	EndpointURL string `json:"endpoint_url"`
}

// EnsureSubscription, abonelik yoksa oluşturur (idempotent).
func (c *AdminClient) EnsureSubscription(ctx context.Context, toolID, eventTypeID, endpointURL string) (*Subscription, error) {
	var list struct {
		Items []Subscription `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet,
		"/api/v1/subscriptions?tool_id="+toolID+"&limit=500", nil, &list); err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].EventTypeID == eventTypeID && list.Items[i].EndpointURL == endpointURL {
			return &list.Items[i], nil
		}
	}

	var created Subscription
	body := map[string]any{
		"subscriber_tool_id": toolID,
		"event_type_id":      eventTypeID,
		"endpoint_url":       endpointURL,
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/subscriptions", body, &created); err != nil {
		return nil, err
	}
	return &created, nil
}

// EnsureRoutingRule, koşulsuz bir yönlendirme kuralı oluşturur (idempotent
// değil — çağıran taraf yalnızca ilk kurulumda çağırmalıdır).
func (c *AdminClient) EnsureRoutingRule(ctx context.Context, name, subscriptionID string, condition any) error {
	var list struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := c.do(ctx, http.MethodGet,
		"/api/v1/routing-rules?subscription_id="+subscriptionID+"&limit=100", nil, &list); err != nil {
		return err
	}
	for _, r := range list.Items {
		if r.Name == name {
			return nil
		}
	}

	if condition == nil {
		condition = map[string]any{}
	}
	return c.do(ctx, http.MethodPost, "/api/v1/routing-rules", map[string]any{
		"name":            name,
		"subscription_id": subscriptionID,
		"condition":       condition,
	}, nil)
}

func (c *AdminClient) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("%s %s → %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}
