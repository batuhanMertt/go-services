-- =============================================================================
-- 0001_init — Integration & Event Hub çekirdek şeması
-- Tüm tablolar `hub` şemasında toplanır; servis rolleri 0003'te yetkilendirilir.
-- =============================================================================

CREATE SCHEMA IF NOT EXISTS hub;
SET search_path TO hub, public;

-- --- Yönetim düzlemi: admin kullanıcıları ------------------------------------
CREATE TABLE IF NOT EXISTS hub.admin_users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,                 -- argon2id PHC string
    display_name  TEXT NOT NULL DEFAULT '',
    role          TEXT NOT NULL DEFAULT 'admin'
                  CHECK (role IN ('admin', 'operator', 'viewer')),
    is_active     BOOLEAN NOT NULL DEFAULT TRUE,
    last_login_at TIMESTAMPTZ,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Refresh token'lar hash'lenerek saklanır: DB sızsa bile token kullanılamaz.
CREATE TABLE IF NOT EXISTS hub.refresh_tokens (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    UUID NOT NULL REFERENCES hub.admin_users(id) ON DELETE CASCADE,
    token_hash BYTEA NOT NULL UNIQUE,            -- sha256(raw refresh token)
    user_agent TEXT NOT NULL DEFAULT '',
    ip         TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL,
    revoked_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- Ekosistem: kayıtlı tool'lar ---------------------------------------------
CREATE TABLE IF NOT EXISTS hub.tools (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug          TEXT NOT NULL UNIQUE,          -- makine-dostu kimlik (mock-publisher-1)
    name          TEXT NOT NULL,
    description   TEXT NOT NULL DEFAULT '',
    kind          TEXT NOT NULL
                  CHECK (kind IN ('publisher', 'subscriber', 'both')),
    status        TEXT NOT NULL DEFAULT 'active'
                  CHECK (status IN ('active', 'suspended', 'disabled')),
    contact_email TEXT NOT NULL DEFAULT '',
    metadata      JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- API key'ler plaintext saklanmaz: yalnızca prefix (arama için) + SHA-256 hash.
-- HMAC secret'ı doğrulama sırasında ham haliyle gerektiğinden AES-256-GCM ile
-- şifrelenip saklanır (uygulama seviyesi zarf şifreleme).
CREATE TABLE IF NOT EXISTS hub.api_keys (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tool_id         UUID NOT NULL REFERENCES hub.tools(id) ON DELETE CASCADE,
    key_prefix      TEXT NOT NULL UNIQUE,        -- ih_<12 hex> — lookup anahtarı
    key_hash        BYTEA NOT NULL,              -- sha256(tam api key)
    hmac_secret_enc BYTEA NOT NULL,              -- nonce || ciphertext
    status          TEXT NOT NULL DEFAULT 'active'
                    CHECK (status IN ('active', 'rotating', 'revoked')),
    grace_until     TIMESTAMPTZ,                 -- rotasyonda eski anahtarın son geçerliliği
    expires_at      TIMESTAMPTZ,
    last_used_at    TIMESTAMPTZ,
    revoked_at      TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- Kontrat: event tipleri ---------------------------------------------------
CREATE TABLE IF NOT EXISTS hub.event_types (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_tool_id UUID REFERENCES hub.tools(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,                 -- user.created
    version       INTEGER NOT NULL DEFAULT 1,    -- şema versiyonu (additive-only)
    description   TEXT NOT NULL DEFAULT '',
    json_schema   JSONB NOT NULL,                -- JSON Schema draft 2020-12
    is_active     BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT event_types_name_version_key UNIQUE (name, version)
);

-- --- Abonelikler ---------------------------------------------------------------
CREATE TABLE IF NOT EXISTS hub.subscriptions (
    id                 UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscriber_tool_id UUID NOT NULL REFERENCES hub.tools(id) ON DELETE CASCADE,
    event_type_id      UUID NOT NULL REFERENCES hub.event_types(id) ON DELETE CASCADE,
    endpoint_url       TEXT NOT NULL,
    http_headers       JSONB NOT NULL DEFAULT '{}'::jsonb,  -- ek statik header'lar
    max_retries        INTEGER NOT NULL DEFAULT 5 CHECK (max_retries BETWEEN 0 AND 20),
    timeout_ms         INTEGER NOT NULL DEFAULT 10000 CHECK (timeout_ms BETWEEN 100 AND 120000),
    is_active          BOOLEAN NOT NULL DEFAULT TRUE,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT subscriptions_unique_target
        UNIQUE (subscriber_tool_id, event_type_id, endpoint_url)
);

-- --- Yönlendirme kuralları -----------------------------------------------------
-- condition, `internal/routing` paketindeki koşul DSL'inin JSON gösterimidir:
--   {"op":"and","rules":[{"path":"payload.status","op":"eq","value":"active"}]}
-- Boş obje ({}) "koşulsuz eşleş" anlamına gelir.
CREATE TABLE IF NOT EXISTS hub.routing_rules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    subscription_id UUID NOT NULL REFERENCES hub.subscriptions(id) ON DELETE CASCADE,
    condition       JSONB NOT NULL DEFAULT '{}'::jsonb,
    priority        INTEGER NOT NULL DEFAULT 100,
    is_active       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- --- Alınan event'ler (durable inbox) ------------------------------------------
-- response_snapshot: idempotent replay'de birebir aynı yanıtı döndürmek için
-- ilk isteğin gövdesini saklar (FR-2.5).
CREATE TABLE IF NOT EXISTS hub.event_instances (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_id          TEXT NOT NULL UNIQUE,      -- publisher tarafından üretilen UUID v4
    event_type        TEXT NOT NULL,
    event_version     INTEGER NOT NULL DEFAULT 1,
    source_tool_id    UUID REFERENCES hub.tools(id) ON DELETE SET NULL,
    source_tool_slug  TEXT NOT NULL DEFAULT '',
    payload           JSONB NOT NULL,
    metadata          JSONB NOT NULL DEFAULT '{}'::jsonb,
    trace_id          TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'accepted'
                      CHECK (status IN ('accepted', 'routed', 'no_match', 'failed')),
    response_snapshot JSONB NOT NULL DEFAULT '{}'::jsonb,
    received_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    routed_at         TIMESTAMPTZ
);

-- --- Transactional Outbox ------------------------------------------------------
-- event_instances ile AYNI transaction'da yazılır; poller Kafka'ya basar.
CREATE TABLE IF NOT EXISTS hub.outbox_messages (
    id            BIGSERIAL PRIMARY KEY,
    topic         TEXT NOT NULL,
    partition_key TEXT NOT NULL,
    headers       JSONB NOT NULL DEFAULT '{}'::jsonb,
    payload       JSONB NOT NULL,
    attempts      INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at  TIMESTAMPTZ
);

-- --- Teslimat görevleri ---------------------------------------------------------
-- (event_instance_id, subscription_id) UNIQUE: routing-engine at-least-once
-- tükettiği için aynı event iki kez işlense de mükerrer görev oluşmaz.
CREATE TABLE IF NOT EXISTS hub.delivery_tasks (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    event_instance_id UUID NOT NULL REFERENCES hub.event_instances(id) ON DELETE CASCADE,
    subscription_id   UUID NOT NULL REFERENCES hub.subscriptions(id) ON DELETE CASCADE,
    target_url        TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending'
                      CHECK (status IN ('pending', 'in_flight', 'succeeded', 'failed', 'dead')),
    attempt_count     INTEGER NOT NULL DEFAULT 0,
    max_retries       INTEGER NOT NULL DEFAULT 5,
    next_attempt_at   TIMESTAMPTZ,               -- gecikmeli retry zamanlayıcısının anahtarı
    last_error        TEXT,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT delivery_tasks_unique_target UNIQUE (event_instance_id, subscription_id)
);

-- --- Teslimat denemesi logu ------------------------------------------------------
CREATE TABLE IF NOT EXISTS hub.delivery_attempts (
    id               BIGSERIAL PRIMARY KEY,
    delivery_task_id UUID NOT NULL REFERENCES hub.delivery_tasks(id) ON DELETE CASCADE,
    attempt_no       INTEGER NOT NULL,
    request_headers  JSONB NOT NULL DEFAULT '{}'::jsonb,
    response_status  INTEGER,
    response_body    TEXT,                       -- yalnızca özet (ilk N byte)
    latency_ms       INTEGER NOT NULL DEFAULT 0,
    error            TEXT,
    attempted_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT delivery_attempts_unique_no UNIQUE (delivery_task_id, attempt_no)
);

-- --- Dead Letter Queue -----------------------------------------------------------
CREATE TABLE IF NOT EXISTS hub.dlq_events (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    delivery_task_id  UUID REFERENCES hub.delivery_tasks(id) ON DELETE SET NULL,
    event_instance_id UUID REFERENCES hub.event_instances(id) ON DELETE SET NULL,
    subscription_id   UUID REFERENCES hub.subscriptions(id) ON DELETE SET NULL,
    event_id          TEXT NOT NULL DEFAULT '',
    event_type        TEXT NOT NULL DEFAULT '',
    target_url        TEXT NOT NULL DEFAULT '',
    reason            TEXT NOT NULL DEFAULT '',
    attempts          INTEGER NOT NULL DEFAULT 0,
    payload           JSONB NOT NULL DEFAULT '{}'::jsonb,
    -- Durum makinesi: new → replay_requested → replayed, veya new → discarded.
    -- Admin yalnızca 'replay_requested' yazar; asıl yeniden yayını dlq-handler
    -- yapar (mimari diyagramdaki "REPLAY / PUBLISH" oku). Böylece admin-api'nin
    -- Kafka producer'ı olmasına gerek kalmaz ve kontrol düzlemi ile veri
    -- düzlemi ayrık kalır.
    status            TEXT NOT NULL DEFAULT 'new'
                      CHECK (status IN ('new', 'replay_requested', 'replayed', 'discarded')),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    replay_requested_at TIMESTAMPTZ,
    replayed_at       TIMESTAMPTZ,
    replay_count      INTEGER NOT NULL DEFAULT 0,
    last_replay_error TEXT,
    CONSTRAINT dlq_events_unique_task UNIQUE (delivery_task_id)
);

-- --- Denetim kaydı ----------------------------------------------------------------
CREATE TABLE IF NOT EXISTS hub.audit_logs (
    id            BIGSERIAL PRIMARY KEY,
    actor_type    TEXT NOT NULL DEFAULT 'admin'
                  CHECK (actor_type IN ('admin', 'tool', 'system')),
    actor_id      TEXT NOT NULL DEFAULT '',
    actor_label   TEXT NOT NULL DEFAULT '',
    action        TEXT NOT NULL,                 -- tool.create, api_key.rotate, dlq.replay …
    resource_type TEXT NOT NULL DEFAULT '',
    resource_id   TEXT NOT NULL DEFAULT '',
    before_state  JSONB,
    after_state   JSONB,
    ip            TEXT NOT NULL DEFAULT '',
    user_agent    TEXT NOT NULL DEFAULT '',
    trace_id      TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
