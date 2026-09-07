-- =============================================================================
-- 0002_indexes — sorgu erişim yollarına göre index stratejisi
-- Gerekçeleri docs/04-domain-model.md §Index Stratejisi altında.
-- =============================================================================
SET search_path TO hub, public;

-- API key doğrulaması her ingest isteğinde çalışır: prefix zaten UNIQUE, ancak
-- tool bazlı listeleme ve rotasyon sorguları için ek index gerekir.
CREATE INDEX IF NOT EXISTS idx_api_keys_tool          ON hub.api_keys (tool_id, status);
CREATE INDEX IF NOT EXISTS idx_api_keys_grace         ON hub.api_keys (grace_until)
    WHERE status = 'rotating';

CREATE INDEX IF NOT EXISTS idx_tools_status           ON hub.tools (status);
CREATE INDEX IF NOT EXISTS idx_event_types_name       ON hub.event_types (name, version)
    WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_event_types_owner      ON hub.event_types (owner_tool_id);

-- Routing engine'in sıcak yolu: event_type_id -> aktif abonelikler.
CREATE INDEX IF NOT EXISTS idx_subscriptions_type     ON hub.subscriptions (event_type_id)
    WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_subscriptions_tool     ON hub.subscriptions (subscriber_tool_id);
CREATE INDEX IF NOT EXISTS idx_routing_rules_sub      ON hub.routing_rules (subscription_id)
    WHERE is_active;

-- Admin UI "Events" sayfası: en yeniden eskiye sayfalama + tip/zaman filtresi.
CREATE INDEX IF NOT EXISTS idx_events_received        ON hub.event_instances (received_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_type_received   ON hub.event_instances (event_type, received_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_source          ON hub.event_instances (source_tool_id, received_at DESC);
CREATE INDEX IF NOT EXISTS idx_events_trace           ON hub.event_instances (trace_id);
-- Payload içinde alan bazlı arama (admin filtreleri) için GIN.
CREATE INDEX IF NOT EXISTS idx_events_payload_gin     ON hub.event_instances USING GIN (payload jsonb_path_ops);

-- Outbox poller yalnızca yayınlanmamış satırlarla ilgilenir: partial index
-- tablo büyüdükçe tarama maliyetini sabit tutar.
CREATE INDEX IF NOT EXISTS idx_outbox_unpublished     ON hub.outbox_messages (created_at)
    WHERE published_at IS NULL;

-- Retry zamanlayıcısı: "zamanı gelmiş bekleyen görevler".
CREATE INDEX IF NOT EXISTS idx_tasks_due              ON hub.delivery_tasks (next_attempt_at)
    WHERE status = 'pending' AND next_attempt_at IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_status           ON hub.delivery_tasks (status, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_tasks_subscription     ON hub.delivery_tasks (subscription_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_tasks_event           ON hub.delivery_tasks (event_instance_id);

CREATE INDEX IF NOT EXISTS idx_attempts_task          ON hub.delivery_attempts (delivery_task_id, attempt_no DESC);
CREATE INDEX IF NOT EXISTS idx_attempts_time          ON hub.delivery_attempts (attempted_at DESC);
CREATE INDEX IF NOT EXISTS idx_attempts_status        ON hub.delivery_attempts (response_status, attempted_at DESC);

CREATE INDEX IF NOT EXISTS idx_dlq_status_created     ON hub.dlq_events (status, created_at DESC);
-- dlq-handler'ın replay kuyruğu taraması: yalnızca talep edilmiş kayıtlar.
CREATE INDEX IF NOT EXISTS idx_dlq_replay_queue        ON hub.dlq_events (replay_requested_at)
    WHERE status = 'replay_requested';
CREATE INDEX IF NOT EXISTS idx_dlq_subscription       ON hub.dlq_events (subscription_id, status);
CREATE INDEX IF NOT EXISTS idx_dlq_event_type         ON hub.dlq_events (event_type, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_audit_created          ON hub.audit_logs (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_actor            ON hub.audit_logs (actor_type, actor_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_resource         ON hub.audit_logs (resource_type, resource_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_refresh_user           ON hub.refresh_tokens (user_id)
    WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_refresh_expiry         ON hub.refresh_tokens (expires_at);
