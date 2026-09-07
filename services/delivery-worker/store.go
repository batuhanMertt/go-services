package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mindmons/integration-hub/internal/model"
)

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var ErrTaskNotActionable = errors.New("görev işlenebilir durumda değil")

// AttemptContext, bir teslimat denemesinin başlangıç bilgisi.
type AttemptContext struct {
	AttemptNo  int
	MaxRetries int
}

// BeginAttempt, görevi in_flight yapar ve deneme numarasını artırır.
//
// Koşullu UPDATE (WHERE status IN ('pending','in_flight')) sayesinde zaten
// başarılı veya ölü bir görev yeniden gönderilmez. Bu, Kafka'nın at-least-once
// tesliminden gelen mükerrer mesajlara karşı ikinci savunma hattıdır
// (birincisi Redis kilidi).
func (s *Store) BeginAttempt(ctx context.Context, taskID string) (*AttemptContext, error) {
	var a AttemptContext
	err := s.pool.QueryRow(ctx, `
		UPDATE hub.delivery_tasks
		SET status = 'in_flight', attempt_count = attempt_count + 1, updated_at = now()
		WHERE id = $1 AND status IN ('pending', 'in_flight')
		RETURNING attempt_count, max_retries`, taskID).Scan(&a.AttemptNo, &a.MaxRetries)

	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTaskNotActionable
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// RecordAttempt, teslimat denemesinin sonucunu loglar (FR-4.4).
//
// DİKKAT — log sıra numarası, retry bütçesi sayacından FARKLIDIR:
//
//	delivery_tasks.attempt_count : retry BÜTÇESİ sayacı. Replay'de 0'a
//	                               sıfırlanır ki mesaj tam bir bütçe alsın.
//	delivery_attempts.attempt_no : operasyonel LOG sırası. Asla sıfırlanmaz.
//
// İkisi aynı sayı olsaydı, replay sonrası yapılan deneme #1 olur ve tabloda
// zaten var olan eski #1 ile çakışırdı; ON CONFLICT nedeniyle başarılı
// teslimat sessizce kaydedilmez, admin panelinde yalnızca eski hatalar
// görünürdü. Bu yüzden log sırası burada, o göreve ait mevcut en yüksek
// numaranın bir fazlası olarak ATOMİK biçimde hesaplanır.
func (s *Store) RecordAttempt(ctx context.Context, taskID string, _ int,
	headers map[string]string, status *int, body string, latencyMS int, errMsg string) error {

	headersJSON, err := json.Marshal(headers)
	if err != nil {
		headersJSON = []byte(`{}`)
	}

	var errArg any
	if errMsg != "" {
		errArg = errMsg
	}
	var bodyArg any
	if body != "" {
		bodyArg = body
	}

	// Sıra numarası INSERT ... SELECT içinde hesaplanır: ayrı bir SELECT
	// yapılsaydı iki eşzamanlı worker aynı numarayı okuyabilirdi.
	// ON CONFLICT hâlâ duruyor — o yarışta kaybeden taraf sessizce atlar,
	// tekrar denemesi gerekmez (log kaydı, teslimatın kendisi değil).
	_, err = s.pool.Exec(ctx, `
		INSERT INTO hub.delivery_attempts
			(delivery_task_id, attempt_no, request_headers, response_status,
			 response_body, latency_ms, error)
		SELECT $1,
		       COALESCE(MAX(a.attempt_no), 0) + 1,
		       $2, $3, $4, $5, $6
		FROM hub.delivery_attempts a
		WHERE a.delivery_task_id = $1
		ON CONFLICT (delivery_task_id, attempt_no) DO NOTHING`,
		taskID, headersJSON, status, bodyArg, latencyMS, errArg)
	return err
}

// MarkSucceeded, görevi tamamlandı olarak işaretler.
func (s *Store) MarkSucceeded(ctx context.Context, taskID string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE hub.delivery_tasks
		SET status = 'succeeded', next_attempt_at = NULL, last_error = NULL, updated_at = now()
		WHERE id = $1`, taskID)
	return err
}

// ScheduleRetry, görevi gelecekte yeniden denenmek üzere planlar.
//
// Kafka'da yerleşik gecikmeli mesaj yoktur. Alternatifler:
//   - Gecikme topic'leri zinciri (retry.1s, retry.2s …): topic sayısını
//     şişirir, her gecikme için ayrı consumer gerekir, gecikme çözünürlüğü
//     topic sayısıyla sınırlıdır.
//   - Consumer'da sleep: partition'ı bloke eder, aynı partition'daki sağlıklı
//     mesajlar da bekler. Kabul edilemez.
//   - DB'de zamanlanmış görev (seçilen): gecikme sürekli ve keyfi olabilir,
//     admin panelinden "ne zaman yeniden denenecek" görülebilir, worker
//     yeniden başlasa bile plan kaybolmaz.
func (s *Store) ScheduleRetry(ctx context.Context, taskID string, nextAttemptAt time.Time, lastErr string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE hub.delivery_tasks
		SET status = 'pending', next_attempt_at = $2, last_error = $3, updated_at = now()
		WHERE id = $1`, taskID, nextAttemptAt, truncate(lastErr, 1000))
	return err
}

// Defer, bulkhead dolu veya devre açık olduğunda görevi kısa süre sonraya erteler.
//
// Deneme sayacına DOKUNULMAZ. Gerekçe: erteleme kararları BeginAttempt'ten
// ÖNCE verilir, yani bu tur için sayaç henüz artırılmamıştır. Sayacı burada
// azaltmak, hiç yapılmamış bir denemeyi geri almak olur ve görev sonsuza dek
// aynı deneme numarasında dönüp DLQ'ya asla ulaşamaz.
func (s *Store) Defer(ctx context.Context, taskID string, nextAttemptAt time.Time, reason string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE hub.delivery_tasks
		SET status = 'pending',
		    next_attempt_at = $2,
		    last_error = $3,
		    updated_at = now()
		WHERE id = $1 AND status IN ('pending', 'in_flight')`,
		taskID, nextAttemptAt, truncate(reason, 500))
	return err
}

// MarkDead, görevi kalıcı hata olarak işaretler (DLQ'ya gidecek).
func (s *Store) MarkDead(ctx context.Context, taskID string, lastErr string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE hub.delivery_tasks
		SET status = 'dead', next_attempt_at = NULL, last_error = $2, updated_at = now()
		WHERE id = $1`, taskID, truncate(lastErr, 1000))
	return err
}

// DueTask, zamanlayıcının yeniden yayınlayacağı görevin tam bilgisi.
type DueTask struct {
	Task     model.DeliveryTask
	Attempt  int
	Received time.Time
}

const dueTaskSelect = `
	SELECT dt.id, dt.event_instance_id, dt.subscription_id, t.slug, dt.target_url,
	       s.http_headers, dt.max_retries, s.timeout_ms, dt.attempt_count,
	       e.event_id, e.event_type, e.event_version, e.source_tool_slug,
	       e.payload, e.metadata, e.trace_id, e.received_at
	FROM hub.delivery_tasks dt
	JOIN hub.subscriptions s   ON s.id = dt.subscription_id
	JOIN hub.tools t           ON t.id = s.subscriber_tool_id
	JOIN hub.event_instances e ON e.id = dt.event_instance_id`

// ClaimDueTasks, zamanı gelmiş görevleri kilitleyip alır ve bir sonraki
// taramada tekrar alınmamaları için next_attempt_at'i ileri atar.
//
// SKIP LOCKED sayesinde birden fazla delivery-worker instance'ı aynı anda
// tarama yapsa da her görevi yalnızca biri alır.
func (s *Store) ClaimDueTasks(ctx context.Context, limit int, lease time.Duration) ([]DueTask, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT id FROM hub.delivery_tasks
		WHERE status = 'pending' AND next_attempt_at IS NOT NULL AND next_attempt_at <= now()
		ORDER BY next_attempt_at
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, err
	}

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if len(ids) == 0 {
		return nil, tx.Commit(ctx)
	}

	// Kiralama (lease): yeniden yayın başarısız olsa bile görev bu süre
	// sonunda yeniden ele alınabilir; kalıcı olarak kaybolmaz.
	if _, err := tx.Exec(ctx, `
		UPDATE hub.delivery_tasks
		SET next_attempt_at = now() + $2::interval, updated_at = now()
		WHERE id = ANY($1::uuid[])`, ids, lease.String()); err != nil {
		return nil, err
	}

	detailRows, err := tx.Query(ctx, dueTaskSelect+` WHERE dt.id = ANY($1::uuid[])`, ids)
	if err != nil {
		return nil, err
	}

	var out []DueTask
	for detailRows.Next() {
		dt, err := scanDueTask(detailRows)
		if err != nil {
			detailRows.Close()
			return nil, err
		}
		out = append(out, dt)
	}
	detailRows.Close()
	if err := detailRows.Err(); err != nil {
		return nil, err
	}

	return out, tx.Commit(ctx)
}

type rowScanner interface{ Scan(dest ...any) error }

func scanDueTask(r rowScanner) (DueTask, error) {
	var d DueTask
	var headers, payload, metadata []byte

	if err := r.Scan(
		&d.Task.TaskID, &d.Task.EventInstanceID, &d.Task.SubscriptionID,
		&d.Task.SubscriberSlug, &d.Task.TargetURL, &headers,
		&d.Task.MaxRetries, &d.Task.TimeoutMS, &d.Attempt,
		&d.Task.Envelope.EventID, &d.Task.Envelope.EventType, &d.Task.Envelope.EventVersion,
		&d.Task.Envelope.SourceTool, &payload, &metadata,
		&d.Task.Envelope.TraceID, &d.Received,
	); err != nil {
		return d, err
	}

	if len(headers) > 0 {
		_ = json.Unmarshal(headers, &d.Task.ExtraHeaders)
	}
	d.Task.Envelope.Payload = payload
	if len(metadata) > 0 {
		_ = json.Unmarshal(metadata, &d.Task.Envelope.Metadata)
	}
	d.Task.ReceivedAt = d.Received
	d.Task.Attempt = d.Attempt + 1

	return d, nil
}

// PendingCount, bekleyen görev sayısı (metrik).
func (s *Store) PendingCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM hub.delivery_tasks WHERE status IN ('pending','in_flight')`).Scan(&n)
	return n, err
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
