package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Target, bir event tipine abone olan tek bir hedef ve onun filtreleri.
type Target struct {
	SubscriptionID string            `json:"subscription_id"`
	SubscriberSlug string            `json:"subscriber_slug"`
	EndpointURL    string            `json:"endpoint_url"`
	Headers        map[string]string `json:"headers"`
	MaxRetries     int               `json:"max_retries"`
	TimeoutMS      int               `json:"timeout_ms"`
	// Conditions, bu aboneliğe bağlı AKTİF kuralların koşullarıdır.
	// Boş liste = koşulsuz eşleşme (abonelik tek başına yeterli).
	Conditions []json.RawMessage `json:"conditions"`
}

type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// LoadTargets, bir event tipi için tüm aktif hedefleri ve kurallarını yükler.
//
// Tek sorguda abonelik + kural birleşimi çekilir (kurallar dizi olarak
// toplanır): N+1 sorgu problemi baştan engellenir.
func (s *Store) LoadTargets(ctx context.Context, eventTypeName string, version int) ([]Target, error) {
	const q = `
		SELECT s.id, t.slug, s.endpoint_url, s.http_headers, s.max_retries, s.timeout_ms,
		       COALESCE(
		           (SELECT json_agg(r.condition ORDER BY r.priority, r.created_at)
		            FROM hub.routing_rules r
		            WHERE r.subscription_id = s.id AND r.is_active),
		           '[]'::json
		       ) AS conditions
		FROM hub.subscriptions s
		JOIN hub.tools t        ON t.id  = s.subscriber_tool_id
		JOIN hub.event_types et ON et.id = s.event_type_id
		WHERE s.is_active
		  AND et.is_active
		  AND et.name = $1
		  AND et.version = $2
		  AND t.status = 'active'`

	rows, err := s.pool.Query(ctx, q, eventTypeName, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	targets := []Target{}
	for rows.Next() {
		var t Target
		var headers, conditions []byte

		if err := rows.Scan(&t.SubscriptionID, &t.SubscriberSlug, &t.EndpointURL,
			&headers, &t.MaxRetries, &t.TimeoutMS, &conditions); err != nil {
			return nil, err
		}

		if len(headers) > 0 {
			_ = json.Unmarshal(headers, &t.Headers)
		}
		if len(conditions) > 0 {
			_ = json.Unmarshal(conditions, &t.Conditions)
		}
		targets = append(targets, t)
	}
	return targets, rows.Err()
}

// CreateDeliveryTask, teslimat görevini oluşturur.
//
// ON CONFLICT DO NOTHING + UNIQUE(event_instance_id, subscription_id):
// routing-engine Kafka'yı at-least-once tükettiği için aynı event iki kez
// işlenebilir. Bu kısıt, mükerrer işlemenin mükerrer webhook'a dönüşmesini
// veritabanı seviyesinde engeller — kilit veya dağıtık koordinasyon gerekmez.
//
// next_attempt_at = now() + recoveryDelay: Kafka'ya publish başarısız olursa
// görev DB'de "pending" kalır ve delivery-worker'ın retry zamanlayıcısı onu
// bu süre sonunda kurtarır. Yani Kafka yazımının kaybolması bile teslimatı
// iptal etmez, yalnızca geciktirir.
func (s *Store) CreateDeliveryTask(
	ctx context.Context,
	eventInstanceID, subscriptionID, targetURL string,
	maxRetries int,
	recoveryDelay time.Duration,
) (string, bool, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO hub.delivery_tasks
			(event_instance_id, subscription_id, target_url, status, max_retries, next_attempt_at)
		VALUES ($1, $2, $3, 'pending', $4, now() + $5::interval)
		ON CONFLICT (event_instance_id, subscription_id) DO NOTHING
		RETURNING id`,
		eventInstanceID, subscriptionID, targetURL, maxRetries, recoveryDelay.String(),
	).Scan(&id)

	if errors.Is(err, pgx.ErrNoRows) {
		// Satır dönmediyse görev zaten vardı (mükerrer işleme) — mevcut id'yi al.
		var existing string
		if qerr := s.pool.QueryRow(ctx, `
			SELECT id FROM hub.delivery_tasks
			WHERE event_instance_id = $1 AND subscription_id = $2`,
			eventInstanceID, subscriptionID).Scan(&existing); qerr != nil {
			return "", false, qerr
		}
		return existing, false, nil
	}
	if err != nil {
		return "", false, err
	}
	return id, true, nil
}

// MarkEventRouted, event'in yönlendirme durumunu günceller.
func (s *Store) MarkEventRouted(ctx context.Context, eventInstanceID string, matched int) error {
	status := "routed"
	if matched == 0 {
		status = "no_match"
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE hub.event_instances
		SET status = $2, routed_at = now()
		WHERE id = $1 AND status = 'accepted'`, eventInstanceID, status)
	return err
}

// ResolveEventInstanceID, outbox mesajındaki event_id'den DB id'sini bulur.
//
// Neden gerekli? event-ingest, outbox mesajını event_instances satırı
// yazılmadan ÖNCE serileştirir (aynı transaction içinde), bu yüzden mesajdaki
// event_instance_id yer tutucudur. Kanonik kimlik daima event_id'dir.
func (s *Store) ResolveEventInstanceID(ctx context.Context, eventID string) (string, error) {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM hub.event_instances WHERE event_id = $1`, eventID).Scan(&id)
	return id, err
}
