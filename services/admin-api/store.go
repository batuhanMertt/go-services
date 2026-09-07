package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store, admin-api'nin veri erişim katmanı.
//
// Tüm sorgular pgx'in parametreli sorgu (prepared statement) mekanizmasını
// kullanır — string birleştirme ile SQL kurulan tek bir yer yoktur.
// Bu, SQL injection'a karşı yapısal koruma sağlar (docs/08-threat-model.md).
type Store struct {
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// ErrNotFound, kayıt bulunamadığında döner.
var ErrNotFound = errors.New("kayıt bulunamadı")

func mapErr(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// --- Admin kullanıcıları -------------------------------------------------------

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*AdminUser, error) {
	const q = `
		SELECT id, email, password_hash, display_name, role, is_active, last_login_at, created_at
		FROM hub.admin_users
		WHERE lower(email) = lower($1)`

	var u AdminUser
	err := s.pool.QueryRow(ctx, q, email).Scan(
		&u.ID, &u.Email, &u.passwordHash, &u.DisplayName, &u.Role, &u.IsActive, &u.LastLoginAt, &u.CreatedAt,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	return &u, nil
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*AdminUser, error) {
	const q = `
		SELECT id, email, password_hash, display_name, role, is_active, last_login_at, created_at
		FROM hub.admin_users WHERE id = $1`

	var u AdminUser
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&u.ID, &u.Email, &u.passwordHash, &u.DisplayName, &u.Role, &u.IsActive, &u.LastLoginAt, &u.CreatedAt,
	)
	if err != nil {
		return nil, mapErr(err)
	}
	return &u, nil
}

// CreateUserIfMissing, bootstrap admin kullanıcısını oluşturur.
// İkinci dönüş değeri: kullanıcı bu çağrıda oluşturulduysa true.
func (s *Store) CreateUserIfMissing(ctx context.Context, email, passwordHash, displayName, role string) (bool, error) {
	const q = `
		INSERT INTO hub.admin_users (email, password_hash, display_name, role)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (email) DO NOTHING
		RETURNING id`

	var id string
	err := s.pool.QueryRow(ctx, q, email, passwordHash, displayName, role).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // zaten vardı
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) TouchLogin(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE hub.admin_users SET last_login_at = now(), updated_at = now() WHERE id = $1`, userID)
	return err
}

func (s *Store) UpdatePassword(ctx context.Context, userID, passwordHash string) error {
	ct, err := s.pool.Exec(ctx,
		`UPDATE hub.admin_users SET password_hash = $2, updated_at = now() WHERE id = $1`,
		userID, passwordHash)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// --- Refresh token'lar ----------------------------------------------------------

func (s *Store) StoreRefreshToken(ctx context.Context, userID string, hash []byte, expiresAt time.Time, ua, ip string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO hub.refresh_tokens (user_id, token_hash, expires_at, user_agent, ip)
		VALUES ($1, $2, $3, $4, $5)`,
		userID, hash, expiresAt, ua, ip)
	return err
}

// ConsumeRefreshToken, token'ı doğrular ve TEK KULLANIMLIK yapar (rotation).
//
// Neden tek kullanımlık? Refresh token çalınırsa, saldırgan ve gerçek kullanıcı
// aynı token'ı kullanmaya çalışır. Token her kullanımda döndürüldüğünde
// (rotation), ikinci kullanım başarısız olur ve hırsızlık tespit edilebilir.
func (s *Store) ConsumeRefreshToken(ctx context.Context, hash []byte) (string, error) {
	const q = `
		UPDATE hub.refresh_tokens
		SET revoked_at = now()
		WHERE token_hash = $1 AND revoked_at IS NULL AND expires_at > now()
		RETURNING user_id`

	var userID string
	if err := s.pool.QueryRow(ctx, q, hash).Scan(&userID); err != nil {
		return "", mapErr(err)
	}
	return userID, nil
}

func (s *Store) RevokeAllRefreshTokens(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE hub.refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL`,
		userID)
	return err
}

func (s *Store) RevokeRefreshToken(ctx context.Context, hash []byte) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE hub.refresh_tokens SET revoked_at = now() WHERE token_hash = $1 AND revoked_at IS NULL`,
		hash)
	return err
}

// --- Denetim kaydı --------------------------------------------------------------

// AuditEntry, yazılacak denetim kaydı.
type AuditEntry struct {
	ActorType    string
	ActorID      string
	ActorLabel   string
	Action       string
	ResourceType string
	ResourceID   string
	Before       any
	After        any
	IP           string
	UserAgent    string
	TraceID      string
}

// WriteAudit, denetim kaydı yazar (FR-6.2).
//
// Denetim kaydı yazımı, ana işlemi ASLA başarısız kılmaz: log yazılamadı diye
// bir tool silme işlemini geri almak, operasyonel olarak daha kötüdür. Hata
// loglanır ve alert'e konu olur.
func (s *Store) WriteAudit(ctx context.Context, e AuditEntry) error {
	beforeJSON, err := toJSONOrNil(e.Before)
	if err != nil {
		return err
	}
	afterJSON, err := toJSONOrNil(e.After)
	if err != nil {
		return err
	}

	if e.ActorType == "" {
		e.ActorType = "admin"
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO hub.audit_logs
			(actor_type, actor_id, actor_label, action, resource_type, resource_id,
			 before_state, after_state, ip, user_agent, trace_id)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		e.ActorType, e.ActorID, e.ActorLabel, e.Action, e.ResourceType, e.ResourceID,
		beforeJSON, afterJSON, e.IP, e.UserAgent, e.TraceID)
	return err
}

func toJSONOrNil(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("denetim kaydı serileştirilemedi: %w", err)
	}
	return b, nil
}

func (s *Store) ListAuditLogs(ctx context.Context, f AuditFilter) ([]AuditLog, int, error) {
	const countQ = `
		SELECT count(*) FROM hub.audit_logs
		WHERE ($1 = '' OR action = $1)
		  AND ($2 = '' OR resource_type = $2)
		  AND ($3 = '' OR actor_id = $3)`

	var total int
	if err := s.pool.QueryRow(ctx, countQ, f.Action, f.ResourceType, f.ActorID).Scan(&total); err != nil {
		return nil, 0, err
	}

	const q = `
		SELECT id, actor_type, actor_id, actor_label, action, resource_type, resource_id,
		       before_state, after_state, ip, user_agent, trace_id, created_at
		FROM hub.audit_logs
		WHERE ($1 = '' OR action = $1)
		  AND ($2 = '' OR resource_type = $2)
		  AND ($3 = '' OR actor_id = $3)
		ORDER BY created_at DESC, id DESC
		LIMIT $4 OFFSET $5`

	rows, err := s.pool.Query(ctx, q, f.Action, f.ResourceType, f.ActorID, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	out := make([]AuditLog, 0, f.Limit)
	for rows.Next() {
		var a AuditLog
		var before, after []byte
		if err := rows.Scan(&a.ID, &a.ActorType, &a.ActorID, &a.ActorLabel, &a.Action,
			&a.ResourceType, &a.ResourceID, &before, &after, &a.IP, &a.UserAgent,
			&a.TraceID, &a.CreatedAt); err != nil {
			return nil, 0, err
		}
		a.BeforeState = before
		a.AfterState = after
		out = append(out, a)
	}
	return out, total, rows.Err()
}

// AuditFilter, denetim kaydı listeleme filtreleri.
type AuditFilter struct {
	Action       string
	ResourceType string
	ActorID      string
	Limit        int
	Offset       int
}
