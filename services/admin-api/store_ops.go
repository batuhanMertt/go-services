package main

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// --- Event akışı ------------------------------------------------------------------

// EventFilter, Events sayfasının filtreleri.
type EventFilter struct {
	EventType  string
	SourceTool string
	Status     string
	Search     string // event_id veya trace_id içinde arama
	Since      *time.Time
	Limit      int
	Offset     int
}

const eventSelect = `
	SELECT e.id, e.event_id, e.event_type, e.event_version,
	       COALESCE(t.slug, e.source_tool_slug), e.payload, e.metadata,
	       e.trace_id, e.status, e.received_at, e.routed_at
	FROM hub.event_instances e
	LEFT JOIN hub.tools t ON t.id = e.source_tool_id`

const eventWhere = `
	WHERE ($1 = '' OR e.event_type = $1)
	  AND ($2 = '' OR COALESCE(t.slug, e.source_tool_slug) = $2)
	  AND ($3 = '' OR e.status = $3)
	  AND ($4 = '' OR e.event_id ILIKE '%' || $4 || '%' OR e.trace_id ILIKE '%' || $4 || '%')
	  AND ($5::timestamptz IS NULL OR e.received_at >= $5)`

func (s *Store) ListEvents(ctx context.Context, f EventFilter) ([]EventInstance, int, error) {
	countQ := `SELECT count(*) FROM hub.event_instances e
	           LEFT JOIN hub.tools t ON t.id = e.source_tool_id` + eventWhere

	var total int
	if err := s.pool.QueryRow(ctx, countQ,
		f.EventType, f.SourceTool, f.Status, f.Search, f.Since).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.pool.Query(ctx, eventSelect+eventWhere+`
		ORDER BY e.received_at DESC
		LIMIT $6 OFFSET $7`,
		f.EventType, f.SourceTool, f.Status, f.Search, f.Since, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]EventInstance, 0, f.Limit)
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, ev)
	}
	return out, total, rows.Err()
}

func scanEvent(r rowScanner) (EventInstance, error) {
	var e EventInstance
	var payload, meta []byte
	if err := r.Scan(&e.ID, &e.EventID, &e.EventType, &e.EventVersion, &e.SourceTool,
		&payload, &meta, &e.TraceID, &e.Status, &e.ReceivedAt, &e.RoutedAt); err != nil {
		return e, err
	}
	e.Payload = payload
	e.Metadata = meta
	return e, nil
}

func (s *Store) GetEvent(ctx context.Context, id string) (*EventInstance, error) {
	row := s.pool.QueryRow(ctx, eventSelect+` WHERE e.id = $1 OR e.event_id = $1`, id)
	e, err := scanEvent(row)
	if err != nil {
		return nil, mapErr(err)
	}
	return &e, nil
}

// LatestEventID, SSE akışının "yeni kayıt var mı" kontrolü için kullanılır.
func (s *Store) EventsSince(ctx context.Context, since time.Time, limit int) ([]EventInstance, error) {
	rows, err := s.pool.Query(ctx, eventSelect+`
		WHERE e.received_at > $1
		ORDER BY e.received_at ASC
		LIMIT $2`, since, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]EventInstance, 0, limit)
	for rows.Next() {
		ev, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, rows.Err()
}

// --- Teslimat denemeleri --------------------------------------------------------

type AttemptFilter struct {
	SubscriptionID string
	EventID        string
	Status         string // task durumu
	OnlyFailed     bool
	Limit          int
	Offset         int
}

const attemptSelect = `
	SELECT a.id, a.delivery_task_id, e.event_id, e.event_type, t.slug, dt.target_url,
	       a.attempt_no, a.response_status, COALESCE(a.response_body, ''), a.latency_ms,
	       COALESCE(a.error, ''), a.attempted_at, dt.status, dt.next_attempt_at
	FROM hub.delivery_attempts a
	JOIN hub.delivery_tasks dt   ON dt.id = a.delivery_task_id
	JOIN hub.event_instances e   ON e.id  = dt.event_instance_id
	JOIN hub.subscriptions s     ON s.id  = dt.subscription_id
	JOIN hub.tools t             ON t.id  = s.subscriber_tool_id`

const attemptWhere = `
	WHERE ($1 = '' OR dt.subscription_id = $1::uuid)
	  AND ($2 = '' OR e.event_id = $2)
	  AND ($3 = '' OR dt.status = $3)
	  AND ($4 = false OR a.response_status IS NULL OR a.response_status >= 400)`

func (s *Store) ListAttempts(ctx context.Context, f AttemptFilter) ([]DeliveryAttempt, int, error) {
	countQ := `SELECT count(*) FROM hub.delivery_attempts a
	           JOIN hub.delivery_tasks dt ON dt.id = a.delivery_task_id
	           JOIN hub.event_instances e ON e.id = dt.event_instance_id` + attemptWhere

	var total int
	if err := s.pool.QueryRow(ctx, countQ,
		f.SubscriptionID, f.EventID, f.Status, f.OnlyFailed).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.pool.Query(ctx, attemptSelect+attemptWhere+`
		ORDER BY a.attempted_at DESC, a.id DESC
		LIMIT $5 OFFSET $6`,
		f.SubscriptionID, f.EventID, f.Status, f.OnlyFailed, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]DeliveryAttempt, 0, f.Limit)
	for rows.Next() {
		var a DeliveryAttempt
		if err := rows.Scan(&a.ID, &a.TaskID, &a.EventID, &a.EventType, &a.SubscriberSlug,
			&a.TargetURL, &a.AttemptNo, &a.ResponseStatus, &a.ResponseBody, &a.LatencyMS,
			&a.Error, &a.AttemptedAt, &a.TaskStatus, &a.NextAttemptAt); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// --- DLQ ---------------------------------------------------------------------------

type DLQFilter struct {
	Status         string
	SubscriptionID string
	EventType      string
	Limit          int
	Offset         int
}

const dlqSelect = `
	SELECT d.id, d.delivery_task_id, d.subscription_id, COALESCE(t.slug, ''),
	       d.event_id, d.event_type, d.target_url, d.reason, d.attempts,
	       d.payload, d.status, d.created_at, d.replayed_at
	FROM hub.dlq_events d
	LEFT JOIN hub.subscriptions s ON s.id = d.subscription_id
	LEFT JOIN hub.tools t         ON t.id = s.subscriber_tool_id`

const dlqWhere = `
	WHERE ($1 = '' OR d.status = $1)
	  AND ($2 = '' OR d.subscription_id = $2::uuid)
	  AND ($3 = '' OR d.event_type = $3)`

func (s *Store) ListDLQ(ctx context.Context, f DLQFilter) ([]DLQEvent, int, error) {
	countQ := `SELECT count(*) FROM hub.dlq_events d` + dlqWhere

	var total int
	if err := s.pool.QueryRow(ctx, countQ, f.Status, f.SubscriptionID, f.EventType).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.pool.Query(ctx, dlqSelect+dlqWhere+`
		ORDER BY d.created_at DESC
		LIMIT $4 OFFSET $5`,
		f.Status, f.SubscriptionID, f.EventType, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]DLQEvent, 0, f.Limit)
	for rows.Next() {
		var d DLQEvent
		var payload []byte
		if err := rows.Scan(&d.ID, &d.TaskID, &d.SubscriptionID, &d.SubscriberSlug,
			&d.EventID, &d.EventType, &d.TargetURL, &d.Reason, &d.Attempts,
			&payload, &d.Status, &d.CreatedAt, &d.ReplayedAt); err != nil {
			return nil, 0, err
		}
		d.Payload = payload
		out = append(out, d)
	}
	return out, total, rows.Err()
}

func (s *Store) GetDLQEvent(ctx context.Context, id string) (*DLQEvent, error) {
	row := s.pool.QueryRow(ctx, dlqSelect+` WHERE d.id = $1`, id)

	var d DLQEvent
	var payload []byte
	if err := row.Scan(&d.ID, &d.TaskID, &d.SubscriptionID, &d.SubscriberSlug,
		&d.EventID, &d.EventType, &d.TargetURL, &d.Reason, &d.Attempts,
		&payload, &d.Status, &d.CreatedAt, &d.ReplayedAt); err != nil {
		return nil, mapErr(err)
	}
	d.Payload = payload
	return &d, nil
}

// RequestReplayByIDs, seçilen DLQ kayıtlarını replay kuyruğuna alır.
//
// Admin API mesajı Kafka'ya KENDİ basmaz; yalnızca niyeti kaydeder. Asıl
// yeniden yayını dlq-handler yapar. Bu ayrım sayesinde kontrol düzlemi
// (admin-api) veri düzleminin (Kafka) bağımlılığını taşımaz ve replay
// isteği, servis yeniden başlasa bile kaybolmaz (DB'de kalıcı).
func (s *Store) RequestReplayByIDs(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	ct, err := s.pool.Exec(ctx, `
		UPDATE hub.dlq_events
		SET status = 'replay_requested', replay_requested_at = now()
		WHERE id = ANY($1::uuid[]) AND status IN ('new', 'replayed')`, ids)
	if err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}

// RequestReplayBySubscription, bir subscriber'ın tüm bekleyen DLQ kayıtlarını
// toplu olarak replay kuyruğuna alır (FR-5.2).
func (s *Store) RequestReplayBySubscription(ctx context.Context, subscriptionID string, limit int) (int, error) {
	if limit <= 0 {
		limit = 500
	}
	ct, err := s.pool.Exec(ctx, `
		UPDATE hub.dlq_events
		SET status = 'replay_requested', replay_requested_at = now()
		WHERE id IN (
			SELECT id FROM hub.dlq_events
			WHERE subscription_id = $1::uuid AND status = 'new'
			ORDER BY created_at
			LIMIT $2
		)`, subscriptionID, limit)
	if err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}

// DiscardDLQ, kaydı kalıcı olarak "işlenmeyecek" olarak işaretler.
func (s *Store) DiscardDLQ(ctx context.Context, ids []string) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	ct, err := s.pool.Exec(ctx, `
		UPDATE hub.dlq_events SET status = 'discarded'
		WHERE id = ANY($1::uuid[]) AND status <> 'discarded'`, ids)
	if err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}

// --- Dashboard istatistikleri ------------------------------------------------------

func (s *Store) DashboardStats(ctx context.Context) (*DashboardStats, error) {
	var st DashboardStats
	st.GeneratedAt = time.Now().UTC()

	// Tek turda birden çok sayım: her biri ayrı sorgu olsaydı 10+ gidiş-dönüş
	// olurdu. Sayımlar ucuz (index-only) ve dashboard 10 sn'de bir yenileniyor.
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM hub.tools),
			(SELECT count(*) FROM hub.tools WHERE status = 'active'),
			(SELECT count(*) FROM hub.subscriptions),
			(SELECT count(*) FROM hub.subscriptions WHERE is_active),
			(SELECT count(*) FROM hub.event_instances),
			(SELECT count(*) FROM hub.event_instances WHERE received_at > now() - interval '24 hours'),
			(SELECT count(*) FROM hub.event_instances WHERE received_at > now() - interval '1 hour'),
			(SELECT count(*) FROM hub.delivery_tasks WHERE status = 'succeeded' AND updated_at > now() - interval '24 hours'),
			(SELECT count(*) FROM hub.delivery_tasks WHERE status IN ('failed','dead') AND updated_at > now() - interval '24 hours'),
			(SELECT count(*) FROM hub.delivery_tasks WHERE status IN ('pending','in_flight')),
			(SELECT count(*) FROM hub.dlq_events WHERE status IN ('new','replay_requested')),
			(SELECT count(*) FROM hub.dlq_events),
			(SELECT count(*) FROM hub.dlq_events WHERE status = 'replayed'),
			(SELECT count(*) FROM hub.outbox_messages WHERE published_at IS NULL)
	`).Scan(
		&st.Tools.Total, &st.Tools.Active,
		&st.Subscriptions.Total, &st.Subscriptions.Active,
		&st.Events.Total, &st.Events.Last24h, &st.Events.LastHr,
		&st.Deliveries.Succeeded24h, &st.Deliveries.Failed24h, &st.Deliveries.Pending,
		&st.DLQ.Pending, &st.DLQ.Total, &st.DLQ.Replayed,
		&st.Outbox.Pending,
	)
	if err != nil {
		return nil, err
	}

	if totalDeliveries := st.Deliveries.Succeeded24h + st.Deliveries.Failed24h; totalDeliveries > 0 {
		st.Deliveries.SuccessRate = float64(st.Deliveries.Succeeded24h) / float64(totalDeliveries)
	}

	// Event tipi dağılımı
	typeRows, err := s.pool.Query(ctx, `
		SELECT event_type, count(*) FROM hub.event_instances
		WHERE received_at > now() - interval '24 hours'
		GROUP BY event_type ORDER BY count(*) DESC LIMIT 10`)
	if err != nil {
		return nil, err
	}
	st.EventsPerType = []NameCount{}
	for typeRows.Next() {
		var nc NameCount
		if err := typeRows.Scan(&nc.Name, &nc.Count); err != nil {
			typeRows.Close()
			return nil, err
		}
		st.EventsPerType = append(st.EventsPerType, nc)
	}
	typeRows.Close()
	if err := typeRows.Err(); err != nil {
		return nil, err
	}

	// Saatlik zaman serisi (son 24 saat) — boş saatler de 0 olarak döner.
	tlRows, err := s.pool.Query(ctx, `
		SELECT g.bucket, COALESCE(c.n, 0)
		FROM generate_series(
			date_trunc('hour', now() - interval '23 hours'),
			date_trunc('hour', now()),
			interval '1 hour'
		) AS g(bucket)
		LEFT JOIN (
			SELECT date_trunc('hour', received_at) AS bucket, count(*) AS n
			FROM hub.event_instances
			WHERE received_at > now() - interval '24 hours'
			GROUP BY 1
		) c ON c.bucket = g.bucket
		ORDER BY g.bucket`)
	if err != nil {
		return nil, err
	}
	st.EventsTimeline = []TimeCount{}
	for tlRows.Next() {
		var tc TimeCount
		if err := tlRows.Scan(&tc.Bucket, &tc.Count); err != nil {
			tlRows.Close()
			return nil, err
		}
		st.EventsTimeline = append(st.EventsTimeline, tc)
	}
	tlRows.Close()
	if err := tlRows.Err(); err != nil {
		return nil, err
	}

	// En çok teslimat alan subscriber'lar
	subRows, err := s.pool.Query(ctx, `
		SELECT t.slug, count(*) FROM hub.delivery_tasks dt
		JOIN hub.subscriptions s ON s.id = dt.subscription_id
		JOIN hub.tools t         ON t.id = s.subscriber_tool_id
		WHERE dt.created_at > now() - interval '24 hours'
		GROUP BY t.slug ORDER BY count(*) DESC LIMIT 10`)
	if err != nil {
		return nil, err
	}
	st.TopSubscribers = []NameCount{}
	for subRows.Next() {
		var nc NameCount
		if err := subRows.Scan(&nc.Name, &nc.Count); err != nil {
			subRows.Close()
			return nil, err
		}
		st.TopSubscribers = append(st.TopSubscribers, nc)
	}
	subRows.Close()

	return &st, subRows.Err()
}

// ValidateUUIDList, gelen id listesini temizler ve doğrular.
func ValidateUUIDList(ids []string) ([]string, error) {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if len(id) != 36 {
			return nil, fmt.Errorf("geçersiz uuid: %q", id)
		}
		out = append(out, id)
	}
	return out, nil
}
