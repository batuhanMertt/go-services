package main

import (
	"context"
	"encoding/json"
	"time"
)

// --- Tool'lar --------------------------------------------------------------------

func (s *Store) ListTools(ctx context.Context, kind, status string, limit, offset int) ([]Tool, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM hub.tools
		WHERE ($1 = '' OR kind = $1) AND ($2 = '' OR status = $2)`,
		kind, status).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, slug, name, description, kind, status, contact_email, metadata, created_at, updated_at
		FROM hub.tools
		WHERE ($1 = '' OR kind = $1) AND ($2 = '' OR status = $2)
		ORDER BY created_at DESC
		LIMIT $3 OFFSET $4`, kind, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]Tool, 0, limit)
	for rows.Next() {
		t, err := scanTool(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanTool(r rowScanner) (Tool, error) {
	var t Tool
	var meta []byte
	if err := r.Scan(&t.ID, &t.Slug, &t.Name, &t.Description, &t.Kind, &t.Status,
		&t.ContactEmail, &meta, &t.CreatedAt, &t.UpdatedAt); err != nil {
		return t, err
	}
	if len(meta) > 0 {
		_ = json.Unmarshal(meta, &t.Metadata)
	}
	if t.Metadata == nil {
		t.Metadata = map[string]any{}
	}
	return t, nil
}

func (s *Store) GetTool(ctx context.Context, id string) (*Tool, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, description, kind, status, contact_email, metadata, created_at, updated_at
		FROM hub.tools WHERE id = $1`, id)

	t, err := scanTool(row)
	if err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

func (s *Store) GetToolBySlug(ctx context.Context, slug string) (*Tool, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, slug, name, description, kind, status, contact_email, metadata, created_at, updated_at
		FROM hub.tools WHERE slug = $1`, slug)

	t, err := scanTool(row)
	if err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

func (s *Store) CreateTool(ctx context.Context, req ToolRequest) (*Tool, error) {
	meta, err := json.Marshal(orEmptyMap(req.Metadata))
	if err != nil {
		return nil, err
	}

	row := s.pool.QueryRow(ctx, `
		INSERT INTO hub.tools (slug, name, description, kind, status, contact_email, metadata)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING id, slug, name, description, kind, status, contact_email, metadata, created_at, updated_at`,
		req.Slug, req.Name, req.Description, req.Kind, defaultStr(req.Status, "active"),
		req.ContactEmail, meta)

	t, err := scanTool(row)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s *Store) UpdateTool(ctx context.Context, id string, req ToolRequest) (*Tool, error) {
	meta, err := json.Marshal(orEmptyMap(req.Metadata))
	if err != nil {
		return nil, err
	}

	// COALESCE(NULLIF(...)) deseni: boş gönderilen alanlar mevcut değeri korur.
	row := s.pool.QueryRow(ctx, `
		UPDATE hub.tools SET
			name          = COALESCE(NULLIF($2, ''), name),
			description   = COALESCE(NULLIF($3, ''), description),
			kind          = COALESCE(NULLIF($4, ''), kind),
			status        = COALESCE(NULLIF($5, ''), status),
			contact_email = COALESCE(NULLIF($6, ''), contact_email),
			metadata      = CASE WHEN $7::jsonb = '{}'::jsonb THEN metadata ELSE $7::jsonb END,
			updated_at    = now()
		WHERE id = $1
		RETURNING id, slug, name, description, kind, status, contact_email, metadata, created_at, updated_at`,
		id, req.Name, req.Description, req.Kind, req.Status, req.ContactEmail, meta)

	t, err := scanTool(row)
	if err != nil {
		return nil, mapErr(err)
	}
	return &t, nil
}

func (s *Store) DeleteTool(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM hub.tools WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- API anahtarları ---------------------------------------------------------------

func (s *Store) ListAPIKeys(ctx context.Context, toolID string) ([]APIKeyInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tool_id, key_prefix, status, grace_until, expires_at, last_used_at, created_at
		FROM hub.api_keys
		WHERE tool_id = $1
		ORDER BY created_at DESC`, toolID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []APIKeyInfo{}
	for rows.Next() {
		var k APIKeyInfo
		if err := rows.Scan(&k.ID, &k.ToolID, &k.KeyPrefix, &k.Status,
			&k.GraceUntil, &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// InsertAPIKey, yeni anahtarı kaydeder. hmacSecretEnc AES-GCM ile şifrelenmiştir.
func (s *Store) InsertAPIKey(ctx context.Context, toolID, prefix string, hash, hmacSecretEnc []byte) (*APIKeyInfo, error) {
	var k APIKeyInfo
	err := s.pool.QueryRow(ctx, `
		INSERT INTO hub.api_keys (tool_id, key_prefix, key_hash, hmac_secret_enc, status)
		VALUES ($1,$2,$3,$4,'active')
		RETURNING id, tool_id, key_prefix, status, grace_until, expires_at, last_used_at, created_at`,
		toolID, prefix, hash, hmacSecretEnc).Scan(
		&k.ID, &k.ToolID, &k.KeyPrefix, &k.Status, &k.GraceUntil, &k.ExpiresAt, &k.LastUsedAt, &k.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// StartRotation, mevcut aktif anahtarları grace period ile "rotating" yapar.
//
// Neden anında iptal etmiyoruz? Tool'un yeni anahtarı alıp dağıtması zaman
// alır; eski anahtar anında geçersiz olsaydı her rotasyon kesinti yaratırdı
// (FR-1.3). Grace period boyunca İKİ anahtar da geçerlidir; süre sonunda eski
// anahtar otomatik geçersizleşir (event-ingest doğrulamasında kontrol edilir).
func (s *Store) StartRotation(ctx context.Context, toolID string, graceUntil time.Time) (int, error) {
	ct, err := s.pool.Exec(ctx, `
		UPDATE hub.api_keys
		SET status = 'rotating', grace_until = $2
		WHERE tool_id = $1 AND status = 'active'`,
		toolID, graceUntil)
	if err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}

func (s *Store) RevokeAPIKey(ctx context.Context, keyID string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE hub.api_keys SET status = 'revoked', revoked_at = now()
		WHERE id = $1 AND status <> 'revoked'`, keyID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Event tipleri ---------------------------------------------------------------

func (s *Store) ListEventTypes(ctx context.Context, limit, offset int) ([]EventType, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM hub.event_types`).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id, owner_tool_id, name, version, description, json_schema, is_active, created_at, updated_at
		FROM hub.event_types
		ORDER BY name, version
		LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]EventType, 0, limit)
	for rows.Next() {
		et, err := scanEventType(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, et)
	}
	return out, total, rows.Err()
}

func scanEventType(r rowScanner) (EventType, error) {
	var et EventType
	var schemaBytes []byte
	if err := r.Scan(&et.ID, &et.OwnerToolID, &et.Name, &et.Version, &et.Description,
		&schemaBytes, &et.IsActive, &et.CreatedAt, &et.UpdatedAt); err != nil {
		return et, err
	}
	et.JSONSchema = schemaBytes
	return et, nil
}

func (s *Store) GetEventType(ctx context.Context, id string) (*EventType, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, owner_tool_id, name, version, description, json_schema, is_active, created_at, updated_at
		FROM hub.event_types WHERE id = $1`, id)

	et, err := scanEventType(row)
	if err != nil {
		return nil, mapErr(err)
	}
	return &et, nil
}

func (s *Store) CreateEventType(ctx context.Context, req EventTypeRequest) (*EventType, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO hub.event_types (owner_tool_id, name, version, description, json_schema, is_active)
		VALUES ($1,$2,$3,$4,$5,$6)
		RETURNING id, owner_tool_id, name, version, description, json_schema, is_active, created_at, updated_at`,
		req.OwnerToolID, req.Name, req.Version, req.Description, []byte(req.JSONSchema),
		boolOr(req.IsActive, true))

	et, err := scanEventType(row)
	if err != nil {
		return nil, err
	}
	return &et, nil
}

func (s *Store) UpdateEventType(ctx context.Context, id string, req EventTypeRequest) (*EventType, error) {
	var schemaArg any
	if len(req.JSONSchema) > 0 {
		schemaArg = []byte(req.JSONSchema)
	}

	row := s.pool.QueryRow(ctx, `
		UPDATE hub.event_types SET
			description = COALESCE(NULLIF($2, ''), description),
			json_schema = COALESCE($3::jsonb, json_schema),
			is_active   = COALESCE($4, is_active),
			updated_at  = now()
		WHERE id = $1
		RETURNING id, owner_tool_id, name, version, description, json_schema, is_active, created_at, updated_at`,
		id, req.Description, schemaArg, req.IsActive)

	et, err := scanEventType(row)
	if err != nil {
		return nil, mapErr(err)
	}
	return &et, nil
}

func (s *Store) DeleteEventType(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM hub.event_types WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Abonelikler --------------------------------------------------------------------

const subscriptionSelect = `
	SELECT s.id, s.subscriber_tool_id, t.slug, s.event_type_id, et.name, et.version,
	       s.endpoint_url, s.http_headers, s.max_retries, s.timeout_ms, s.is_active,
	       s.created_at, s.updated_at
	FROM hub.subscriptions s
	JOIN hub.tools t        ON t.id  = s.subscriber_tool_id
	JOIN hub.event_types et ON et.id = s.event_type_id`

func scanSubscription(r rowScanner) (Subscription, error) {
	var sub Subscription
	var headers []byte
	if err := r.Scan(&sub.ID, &sub.SubscriberToolID, &sub.SubscriberSlug, &sub.EventTypeID,
		&sub.EventTypeName, &sub.EventTypeVersion, &sub.EndpointURL, &headers,
		&sub.MaxRetries, &sub.TimeoutMS, &sub.IsActive, &sub.CreatedAt, &sub.UpdatedAt); err != nil {
		return sub, err
	}
	if len(headers) > 0 {
		_ = json.Unmarshal(headers, &sub.HTTPHeaders)
	}
	if sub.HTTPHeaders == nil {
		sub.HTTPHeaders = map[string]string{}
	}
	return sub, nil
}

func (s *Store) ListSubscriptions(ctx context.Context, toolID, eventTypeID string, limit, offset int) ([]Subscription, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM hub.subscriptions
		WHERE ($1 = '' OR subscriber_tool_id = $1::uuid)
		  AND ($2 = '' OR event_type_id = $2::uuid)`,
		toolID, eventTypeID).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.pool.Query(ctx, subscriptionSelect+`
		WHERE ($1 = '' OR s.subscriber_tool_id = $1::uuid)
		  AND ($2 = '' OR s.event_type_id = $2::uuid)
		ORDER BY s.created_at DESC
		LIMIT $3 OFFSET $4`, toolID, eventTypeID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]Subscription, 0, limit)
	for rows.Next() {
		sub, err := scanSubscription(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, sub)
	}
	return out, total, rows.Err()
}

func (s *Store) GetSubscription(ctx context.Context, id string) (*Subscription, error) {
	row := s.pool.QueryRow(ctx, subscriptionSelect+` WHERE s.id = $1`, id)
	sub, err := scanSubscription(row)
	if err != nil {
		return nil, mapErr(err)
	}
	return &sub, nil
}

func (s *Store) CreateSubscription(ctx context.Context, req SubscriptionRequest) (*Subscription, error) {
	headers, err := json.Marshal(orEmptyStringMap(req.HTTPHeaders))
	if err != nil {
		return nil, err
	}

	var id string
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO hub.subscriptions
			(subscriber_tool_id, event_type_id, endpoint_url, http_headers, max_retries, timeout_ms, is_active)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING id`,
		req.SubscriberToolID, req.EventTypeID, req.EndpointURL, headers,
		intOr(req.MaxRetries, 5), intOr(req.TimeoutMS, 10000), boolOr(req.IsActive, true),
	).Scan(&id); err != nil {
		return nil, err
	}
	return s.GetSubscription(ctx, id)
}

func (s *Store) UpdateSubscription(ctx context.Context, id string, req SubscriptionRequest) (*Subscription, error) {
	var headersArg any
	if req.HTTPHeaders != nil {
		b, err := json.Marshal(req.HTTPHeaders)
		if err != nil {
			return nil, err
		}
		headersArg = b
	}

	ct, err := s.pool.Exec(ctx, `
		UPDATE hub.subscriptions SET
			endpoint_url = COALESCE(NULLIF($2, ''), endpoint_url),
			http_headers = COALESCE($3::jsonb, http_headers),
			max_retries  = COALESCE($4, max_retries),
			timeout_ms   = COALESCE($5, timeout_ms),
			is_active    = COALESCE($6, is_active),
			updated_at   = now()
		WHERE id = $1`,
		id, req.EndpointURL, headersArg, req.MaxRetries, req.TimeoutMS, req.IsActive)
	if err != nil {
		return nil, err
	}
	if ct.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetSubscription(ctx, id)
}

func (s *Store) DeleteSubscription(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM hub.subscriptions WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Yönlendirme kuralları --------------------------------------------------------

const ruleSelect = `
	SELECT r.id, r.name, r.subscription_id, r.condition, r.priority, r.is_active,
	       r.created_at, r.updated_at, et.name, s.endpoint_url
	FROM hub.routing_rules r
	JOIN hub.subscriptions s ON s.id  = r.subscription_id
	JOIN hub.event_types et  ON et.id = s.event_type_id`

func scanRule(r rowScanner) (RoutingRule, error) {
	var rule RoutingRule
	var cond []byte
	if err := r.Scan(&rule.ID, &rule.Name, &rule.SubscriptionID, &cond, &rule.Priority,
		&rule.IsActive, &rule.CreatedAt, &rule.UpdatedAt, &rule.EventTypeName, &rule.EndpointURL); err != nil {
		return rule, err
	}
	rule.Condition = cond
	return rule, nil
}

func (s *Store) ListRoutingRules(ctx context.Context, subscriptionID string, limit, offset int) ([]RoutingRule, int, error) {
	var total int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM hub.routing_rules
		WHERE ($1 = '' OR subscription_id = $1::uuid)`, subscriptionID).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.pool.Query(ctx, ruleSelect+`
		WHERE ($1 = '' OR r.subscription_id = $1::uuid)
		ORDER BY r.priority, r.created_at DESC
		LIMIT $2 OFFSET $3`, subscriptionID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]RoutingRule, 0, limit)
	for rows.Next() {
		rule, err := scanRule(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, rule)
	}
	return out, total, rows.Err()
}

func (s *Store) GetRoutingRule(ctx context.Context, id string) (*RoutingRule, error) {
	row := s.pool.QueryRow(ctx, ruleSelect+` WHERE r.id = $1`, id)
	rule, err := scanRule(row)
	if err != nil {
		return nil, mapErr(err)
	}
	return &rule, nil
}

func (s *Store) CreateRoutingRule(ctx context.Context, req RoutingRuleRequest) (*RoutingRule, error) {
	cond := req.Condition
	if len(cond) == 0 {
		cond = json.RawMessage(`{}`)
	}

	var id string
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO hub.routing_rules (name, subscription_id, condition, priority, is_active)
		VALUES ($1,$2,$3,$4,$5)
		RETURNING id`,
		req.Name, req.SubscriptionID, []byte(cond), intOr(req.Priority, 100), boolOr(req.IsActive, true),
	).Scan(&id); err != nil {
		return nil, err
	}
	return s.GetRoutingRule(ctx, id)
}

func (s *Store) UpdateRoutingRule(ctx context.Context, id string, req RoutingRuleRequest) (*RoutingRule, error) {
	var condArg any
	if len(req.Condition) > 0 {
		condArg = []byte(req.Condition)
	}

	ct, err := s.pool.Exec(ctx, `
		UPDATE hub.routing_rules SET
			name       = COALESCE(NULLIF($2, ''), name),
			condition  = COALESCE($3::jsonb, condition),
			priority   = COALESCE($4, priority),
			is_active  = COALESCE($5, is_active),
			updated_at = now()
		WHERE id = $1`,
		id, req.Name, condArg, req.Priority, req.IsActive)
	if err != nil {
		return nil, err
	}
	if ct.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return s.GetRoutingRule(ctx, id)
}

func (s *Store) DeleteRoutingRule(ctx context.Context, id string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM hub.routing_rules WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Yardımcılar --------------------------------------------------------------------

func defaultStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orEmptyStringMap(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}
