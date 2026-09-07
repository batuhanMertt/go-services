package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store, event-ingest'in veri erişim katmanı.
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

var ErrNotFound = errors.New("kayıt bulunamadı")

// APIKeyRecord, doğrulama için gereken anahtar + tool bilgisi.
type APIKeyRecord struct {
	KeyID         string
	ToolID        string
	ToolSlug      string
	ToolKind      string
	ToolStatus    string
	KeyPrefix     string
	KeyHash       []byte
	HMACSecretEnc []byte
	Status        string
	GraceUntil    *time.Time
	ExpiresAt     *time.Time
}

// LookupAPIKey, prefix ile TEK satır çeker.
//
// Prefix indeksli ve UNIQUE olduğu için bu sorgu O(1)'dir. Anahtarın kendisi
// hash'lenmiş saklandığından, prefix olmadan doğru satırı bulmanın yolu tüm
// tabloyu taramak olurdu.
func (s *Store) LookupAPIKey(ctx context.Context, prefix string) (*APIKeyRecord, error) {
	const q = `
		SELECT k.id, k.tool_id, t.slug, t.kind, t.status,
		       k.key_prefix, k.key_hash, k.hmac_secret_enc,
		       k.status, k.grace_until, k.expires_at
		FROM hub.api_keys k
		JOIN hub.tools t ON t.id = k.tool_id
		WHERE k.key_prefix = $1`

	var r APIKeyRecord
	err := s.pool.QueryRow(ctx, q, prefix).Scan(
		&r.KeyID, &r.ToolID, &r.ToolSlug, &r.ToolKind, &r.ToolStatus,
		&r.KeyPrefix, &r.KeyHash, &r.HMACSecretEnc,
		&r.Status, &r.GraceUntil, &r.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// TouchKeyUsage, anahtarın son kullanım zamanını günceller.
// Her istekte yazmak gereksiz yük olurdu; çağıran taraf örnekleme yapar.
func (s *Store) TouchKeyUsage(ctx context.Context, keyID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE hub.api_keys SET last_used_at = now() WHERE id = $1`, keyID)
	return err
}

// LoadActiveSchemas, aktif event tiplerinin şemalarını döner.
func (s *Store) LoadActiveSchemas(ctx context.Context) (map[string][]byte, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT name, version, json_schema FROM hub.event_types WHERE is_active`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]byte{}
	for rows.Next() {
		var name string
		var version int
		var raw []byte
		if err := rows.Scan(&name, &version, &raw); err != nil {
			return nil, err
		}
		out[typeKey(name, version)] = raw
	}
	return out, rows.Err()
}

// IngestResult, kabul edilen event'in sonucu.
type IngestResult struct {
	EventInstanceID string
	Duplicate       bool
	Snapshot        json.RawMessage
}

// PersistEvent, event'i ve outbox mesajını TEK TRANSACTION'da yazar.
//
// Transactional Outbox deseninin özü budur: "DB'ye yazdım ama Kafka'ya
// basamadım" durumu imkânsız hale gelir. İki işlem aynı ACID sınırı içindedir;
// ya ikisi de olur ya hiçbiri. Kafka'ya publish, ayrı bir poller tarafından
// ASENKRON yapılır (outbox.go). Kafka saatlerce kapalı kalsa bile tek bir
// event kaybolmaz — yalnızca teslimat gecikir (NFR-1.1 Zero Data Loss).
//
// Alternatif olan "önce Kafka'ya bas, sonra DB'ye yaz" yaklaşımında broker
// yazımı başarılı olup DB yazımı çökerse, sistemde kaydı olmayan bir event
// dolaşırdı ve idempotency kontrolü yapılamazdı.
func (s *Store) PersistEvent(
	ctx context.Context,
	eventID, eventType string, eventVersion int,
	sourceToolID, sourceToolSlug string,
	payload, metadata []byte,
	traceID string,
	topic string,
	kafkaMessage []byte,
	snapshot []byte,
) (*IngestResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// ON CONFLICT DO NOTHING + RETURNING: mükerrer event_id'de satır dönmez.
	// Bu, "önce SELECT sonra INSERT" yarışını (iki eşzamanlı istek) tek atomik
	// adımda çözer (FR-2.5).
	var instanceID string
	err = tx.QueryRow(ctx, `
		INSERT INTO hub.event_instances
			(event_id, event_type, event_version, source_tool_id, source_tool_slug,
			 payload, metadata, trace_id, status, response_snapshot)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'accepted',$9)
		ON CONFLICT (event_id) DO NOTHING
		RETURNING id`,
		eventID, eventType, eventVersion, sourceToolID, sourceToolSlug,
		payload, metadata, traceID, snapshot,
	).Scan(&instanceID)

	if errors.Is(err, pgx.ErrNoRows) {
		// Mükerrer: ilk isteğin yanıtını aynen döndürmek üzere oku.
		var existingID string
		var existingSnapshot []byte
		if err := tx.QueryRow(ctx,
			`SELECT id, response_snapshot FROM hub.event_instances WHERE event_id = $1`,
			eventID).Scan(&existingID, &existingSnapshot); err != nil {
			return nil, err
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, err
		}
		return &IngestResult{
			EventInstanceID: existingID,
			Duplicate:       true,
			Snapshot:        existingSnapshot,
		}, nil
	}
	if err != nil {
		return nil, err
	}

	// Outbox kaydı. partition_key = event_id: aynı event'in tüm mesajları aynı
	// partition'a düşer, böylece sıralama garantisi korunur.
	if _, err := tx.Exec(ctx, `
		INSERT INTO hub.outbox_messages (topic, partition_key, headers, payload)
		VALUES ($1, $2, $3, $4)`,
		topic, eventID,
		[]byte(`{"event_type":"`+eventType+`","trace_id":"`+traceID+`"}`),
		kafkaMessage,
	); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	return &IngestResult{EventInstanceID: instanceID, Duplicate: false}, nil
}

// OutboxRow, yayınlanmayı bekleyen bir outbox kaydı.
type OutboxRow struct {
	ID           int64
	Topic        string
	PartitionKey string
	Headers      map[string]string
	Payload      []byte
	Attempts     int
}

// ClaimOutboxBatch, yayınlanmamış mesajları KİLİTLEYEREK alır.
//
// FOR UPDATE SKIP LOCKED: birden fazla ingest instance'ı aynı anda çalışsa
// bile her satırı yalnızca biri alır; diğerleri kilitli satırları atlayıp
// devam eder (beklemez). Bu, klasik "kuyruk tablosu" desenidir ve poller'ın
// yatay ölçeklenmesini mümkün kılar.
func (s *Store) ClaimOutboxBatch(ctx context.Context, limit int) (pgx.Tx, []OutboxRow, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}

	rows, err := tx.Query(ctx, `
		SELECT id, topic, partition_key, headers, payload, attempts
		FROM hub.outbox_messages
		WHERE published_at IS NULL
		ORDER BY id
		LIMIT $1
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}

	var out []OutboxRow
	for rows.Next() {
		var r OutboxRow
		var headers []byte
		if err := rows.Scan(&r.ID, &r.Topic, &r.PartitionKey, &headers, &r.Payload, &r.Attempts); err != nil {
			rows.Close()
			_ = tx.Rollback(ctx)
			return nil, nil, err
		}
		if len(headers) > 0 {
			_ = json.Unmarshal(headers, &r.Headers)
		}
		out = append(out, r)
	}
	rows.Close()

	if err := rows.Err(); err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}
	return tx, out, nil
}

// MarkPublished, başarıyla yayınlanan mesajları işaretler.
func MarkPublished(ctx context.Context, tx pgx.Tx, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx,
		`UPDATE hub.outbox_messages SET published_at = now() WHERE id = ANY($1::bigint[])`, ids)
	return err
}

// MarkFailed, yayınlanamayan mesajın deneme sayısını artırır.
func MarkFailed(ctx context.Context, tx pgx.Tx, ids []int64, errMsg string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE hub.outbox_messages
		SET attempts = attempts + 1, last_error = $2
		WHERE id = ANY($1::bigint[])`, ids, errMsg)
	return err
}

// PendingOutboxCount, metrik için bekleyen mesaj sayısını döner.
func (s *Store) PendingOutboxCount(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM hub.outbox_messages WHERE published_at IS NULL`).Scan(&n)
	return n, err
}

// PurgePublishedOutbox, yayınlanmış eski kayıtları temizler.
// Outbox tablosu sonsuza dek büyümemeli (genel kural: "disk şişmemeli").
func (s *Store) PurgePublishedOutbox(ctx context.Context, olderThan time.Duration) (int64, error) {
	ct, err := s.pool.Exec(ctx, `
		DELETE FROM hub.outbox_messages
		WHERE published_at IS NOT NULL AND published_at < now() - $1::interval`,
		olderThan.String())
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}
