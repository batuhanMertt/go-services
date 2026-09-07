package main

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mindmons/integration-hub/internal/model"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// SaveDLQEvent, karantinaya alınan mesajı kalıcılaştırır.
//
// ON CONFLICT (delivery_task_id) DO UPDATE: aynı görev tekrar DLQ'ya düşerse
// (replay sonrası yine başarısız olduysa) yeni satır açılmaz, mevcut kayıt
// güncellenir ve durumu 'new'e döner. Böylece operatör tek bir kayıt üzerinden
// tüm geçmişi izler.
func (s *Store) SaveDLQEvent(ctx context.Context, m model.DLQMessage) (bool, error) {
	payload, err := json.Marshal(m.Envelope)
	if err != nil {
		return false, err
	}

	var inserted bool
	err = s.pool.QueryRow(ctx, `
		INSERT INTO hub.dlq_events
			(delivery_task_id, event_instance_id, subscription_id, event_id, event_type,
			 target_url, reason, attempts, payload, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'new')
		ON CONFLICT (delivery_task_id) DO UPDATE SET
			reason     = EXCLUDED.reason,
			attempts   = EXCLUDED.attempts,
			payload    = EXCLUDED.payload,
			status     = 'new',
			created_at = now()
		RETURNING (xmax = 0) AS inserted`,
		nullIfEmpty(m.TaskID), nullIfEmpty(m.EventInstanceID), nullIfEmpty(m.SubscriptionID),
		m.Envelope.EventID, m.Envelope.EventType, m.TargetURL,
		m.Reason, m.Attempts, payload,
	).Scan(&inserted)

	return inserted, err
}

// ReplayCandidate, replay kuyruğundan alınan bir kayıt ve yeniden yayınlanacak
// teslimat görevi.
type ReplayCandidate struct {
	DLQID  string
	TaskID string
	Task   model.DeliveryTask
}

// ClaimReplayQueue, admin'in replay talebi verdiği kayıtları kilitleyip alır
// ve teslimat görevini yeniden yayına hazırlar.
//
// Aynı transaction içinde:
//  1. dlq_events durumu 'replayed' yapılır,
//  2. delivery_tasks sıfırlanır (pending, attempt_count=0),
//  3. görevin tam gövdesi okunur.
//
// Kafka'ya yayın transaction'dan SONRA yapılır; yayın başarısız olursa görev
// yine de 'pending' + next_attempt_at=now() durumundadır ve delivery-worker'ın
// retry zamanlayıcısı onu kurtarır. Yani replay talebi kaybolmaz.
func (s *Store) ClaimReplayQueue(ctx context.Context, limit int) ([]ReplayCandidate, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id, delivery_task_id
		FROM hub.dlq_events
		WHERE status = 'replay_requested' AND delivery_task_id IS NOT NULL
		ORDER BY replay_requested_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}

	type pair struct{ dlqID, taskID string }
	var pairs []pair

	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.dlqID, &p.taskID); err != nil {
			rows.Close()
			return nil, err
		}
		pairs = append(pairs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(pairs) == 0 {
		return nil, tx.Commit(ctx)
	}

	dlqIDs := make([]string, 0, len(pairs))
	taskIDs := make([]string, 0, len(pairs))
	for _, p := range pairs {
		dlqIDs = append(dlqIDs, p.dlqID)
		taskIDs = append(taskIDs, p.taskID)
	}

	// Teslimat görevini sıfırla: deneme sayacı baştan başlar, böylece replay
	// edilen mesaj tam bir retry bütçesi alır.
	if _, err := tx.Exec(ctx, `
		UPDATE hub.delivery_tasks
		SET status = 'pending', attempt_count = 0, next_attempt_at = now(),
		    last_error = NULL, updated_at = now()
		WHERE id = ANY($1::uuid[])`, taskIDs); err != nil {
		return nil, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE hub.dlq_events
		SET status = 'replayed', replayed_at = now(), replay_count = replay_count + 1
		WHERE id = ANY($1::uuid[])`, dlqIDs); err != nil {
		return nil, err
	}

	detailRows, err := tx.Query(ctx, `
		SELECT d.id, dt.id, dt.event_instance_id, dt.subscription_id, t.slug, dt.target_url,
		       s.http_headers, dt.max_retries, s.timeout_ms,
		       e.event_id, e.event_type, e.event_version, e.source_tool_slug,
		       e.payload, e.metadata, e.trace_id, e.received_at
		FROM hub.dlq_events d
		JOIN hub.delivery_tasks dt ON dt.id = d.delivery_task_id
		JOIN hub.subscriptions s   ON s.id  = dt.subscription_id
		JOIN hub.tools t           ON t.id  = s.subscriber_tool_id
		JOIN hub.event_instances e ON e.id  = dt.event_instance_id
		WHERE d.id = ANY($1::uuid[])`, dlqIDs)
	if err != nil {
		return nil, err
	}

	var out []ReplayCandidate
	for detailRows.Next() {
		c, err := scanReplayCandidate(detailRows)
		if err != nil {
			detailRows.Close()
			return nil, err
		}
		out = append(out, c)
	}
	detailRows.Close()
	if err := detailRows.Err(); err != nil {
		return nil, err
	}

	return out, tx.Commit(ctx)
}

type rowScanner interface{ Scan(dest ...any) error }

func scanReplayCandidate(r rowScanner) (ReplayCandidate, error) {
	var c ReplayCandidate
	var headers, payload, metadata []byte

	if err := r.Scan(
		&c.DLQID, &c.TaskID, &c.Task.EventInstanceID, &c.Task.SubscriptionID,
		&c.Task.SubscriberSlug, &c.Task.TargetURL, &headers,
		&c.Task.MaxRetries, &c.Task.TimeoutMS,
		&c.Task.Envelope.EventID, &c.Task.Envelope.EventType, &c.Task.Envelope.EventVersion,
		&c.Task.Envelope.SourceTool, &payload, &metadata,
		&c.Task.Envelope.TraceID, &c.Task.ReceivedAt,
	); err != nil {
		return c, err
	}

	c.Task.TaskID = c.TaskID
	c.Task.Attempt = 1
	c.Task.Replay = true

	if len(headers) > 0 {
		_ = json.Unmarshal(headers, &c.Task.ExtraHeaders)
	}
	c.Task.Envelope.Payload = payload
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &c.Task.Envelope.Metadata)
	}

	return c, nil
}

// MarkReplayFailed, yayın başarısız olduğunda hatayı kaydeder.
func (s *Store) MarkReplayFailed(ctx context.Context, dlqIDs []string, errMsg string) error {
	if len(dlqIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE hub.dlq_events SET last_replay_error = $2 WHERE id = ANY($1::uuid[])`,
		dlqIDs, truncate(errMsg, 500))
	return err
}

// PendingCount, işlenmemiş DLQ kayıt sayısı (alert metriği kaynağı).
func (s *Store) PendingCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM hub.dlq_events WHERE status IN ('new', 'replay_requested')`).Scan(&n)
	return n, err
}

// --- Retention -----------------------------------------------------------------

// PurgeDLQ, saklama süresi dolmuş DLQ kayıtlarını siler.
//
// Yalnızca çözülmüş kayıtlar (replayed/discarded) ve süresi dolmuş 'new'
// kayıtlar silinir. Batch'li silme, uzun süreli tablo kilitlerini önler.
func (s *Store) PurgeDLQ(ctx context.Context, days, batch int) (int64, error) {
	ct, err := s.pool.Exec(ctx, `
		DELETE FROM hub.dlq_events
		WHERE id IN (
			SELECT id FROM hub.dlq_events
			WHERE created_at < now() - make_interval(days => $1)
			LIMIT $2
		)`, days, batch)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// PurgeAttempts, eski teslimat denemesi loglarını siler.
func (s *Store) PurgeAttempts(ctx context.Context, days, batch int) (int64, error) {
	ct, err := s.pool.Exec(ctx, `
		DELETE FROM hub.delivery_attempts
		WHERE id IN (
			SELECT id FROM hub.delivery_attempts
			WHERE attempted_at < now() - make_interval(days => $1)
			LIMIT $2
		)`, days, batch)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// PurgeEvents, eski event kayıtlarını siler.
//
// DİKKAT: Bekleyen teslimatı olan veya DLQ'da kaydı bulunan event'ler
// silinmez. Aksi halde ON DELETE CASCADE ile aktif bir teslimat görevi
// ortadan kalkardı.
func (s *Store) PurgeEvents(ctx context.Context, days, batch int) (int64, error) {
	ct, err := s.pool.Exec(ctx, `
		DELETE FROM hub.event_instances
		WHERE id IN (
			SELECT e.id FROM hub.event_instances e
			WHERE e.received_at < now() - make_interval(days => $1)
			  AND NOT EXISTS (
			      SELECT 1 FROM hub.delivery_tasks dt
			      WHERE dt.event_instance_id = e.id
			        AND dt.status IN ('pending', 'in_flight')
			  )
			  AND NOT EXISTS (
			      SELECT 1 FROM hub.dlq_events d
			      WHERE d.event_instance_id = e.id
			        AND d.status IN ('new', 'replay_requested')
			  )
			LIMIT $2
		)`, days, batch)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// WriteAudit, retention gibi sistem aksiyonlarını denetim kaydına yazar.
func (s *Store) WriteAudit(ctx context.Context, action string, after any) error {
	payload, err := json.Marshal(after)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO hub.audit_logs
			(actor_type, actor_label, action, resource_type, after_state)
		VALUES ('system', 'dlq-handler', $1, 'retention', $2)`, action, payload)
	return err
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
