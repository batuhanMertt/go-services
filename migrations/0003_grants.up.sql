-- =============================================================================
-- 0003_grants — least privilege (Adım 14)
-- Roller `infra/postgres/init/01-roles.sh` tarafından ilk açılışta oluşturulur.
-- Bu migration yalnızca yetkileri dağıtır ve rol yoksa sessizce atlar; böylece
-- harici bir Postgres'e karşı da çalışabilir.
-- Prensip: hiçbir servis ihtiyacı olmayan tabloya yazamaz.
-- =============================================================================
SET search_path TO hub, public;

DO $$
DECLARE
    r TEXT;
BEGIN
    FOREACH r IN ARRAY ARRAY[
        'svc_admin_api', 'svc_event_ingest', 'svc_routing_engine',
        'svc_delivery_worker', 'svc_dlq_handler'
    ] LOOP
        IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
            RAISE NOTICE 'rol % bulunamadı, grant atlanıyor', r;
            CONTINUE;
        END IF;

        EXECUTE format('GRANT USAGE ON SCHEMA hub TO %I', r);
        EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA hub TO %I', r);
    END LOOP;

    -- --- admin-api: kontrol düzlemi. Her şeyi okur, konfigürasyonu yönetir. ---
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_admin_api') THEN
        GRANT SELECT ON ALL TABLES IN SCHEMA hub TO svc_admin_api;
        GRANT INSERT, UPDATE, DELETE ON
            hub.admin_users, hub.refresh_tokens, hub.tools, hub.api_keys,
            hub.event_types, hub.subscriptions, hub.routing_rules
            TO svc_admin_api;
        -- Replay ve denetim: DLQ durumunu günceller, audit yazar, outbox'a basar.
        GRANT INSERT ON hub.audit_logs, hub.outbox_messages TO svc_admin_api;
        GRANT UPDATE ON hub.dlq_events, hub.delivery_tasks TO svc_admin_api;
    END IF;

    -- --- event-ingest: yalnızca inbox + outbox yazar, konfigürasyonu okur. ---
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_event_ingest') THEN
        GRANT SELECT ON hub.tools, hub.api_keys, hub.event_types TO svc_event_ingest;
        GRANT SELECT, INSERT, UPDATE ON hub.event_instances TO svc_event_ingest;
        GRANT SELECT, INSERT, UPDATE, DELETE ON hub.outbox_messages TO svc_event_ingest;
        GRANT UPDATE ON hub.api_keys TO svc_event_ingest;  -- last_used_at
    END IF;

    -- --- routing-engine: kuralları okur, teslimat görevi üretir. ---
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_routing_engine') THEN
        GRANT SELECT ON hub.subscriptions, hub.routing_rules, hub.event_types,
                        hub.tools, hub.event_instances TO svc_routing_engine;
        GRANT SELECT, INSERT, UPDATE ON hub.delivery_tasks TO svc_routing_engine;
        GRANT UPDATE ON hub.event_instances TO svc_routing_engine;
    END IF;

    -- --- delivery-worker: görev yürütür, deneme loglar. ---
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_delivery_worker') THEN
        GRANT SELECT ON hub.subscriptions, hub.tools, hub.api_keys,
                        hub.event_instances TO svc_delivery_worker;
        GRANT SELECT, UPDATE ON hub.delivery_tasks TO svc_delivery_worker;
        GRANT INSERT, SELECT ON hub.delivery_attempts TO svc_delivery_worker;
    END IF;

    -- --- dlq-handler: ölü mesajları saklar, replay ve retention yürütür. ---
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'svc_dlq_handler') THEN
        GRANT SELECT ON hub.subscriptions, hub.event_instances, hub.tools,
                        hub.event_types TO svc_dlq_handler;
        GRANT INSERT, UPDATE, SELECT, DELETE ON hub.dlq_events TO svc_dlq_handler;
        GRANT SELECT, UPDATE ON hub.delivery_tasks TO svc_dlq_handler;
        -- Retention purge: DELETE tek başına yetmez. Silme sorguları
        -- "hangi satırlar eski?" alt sorgusunu çalıştırdığı için aynı
        -- tablolarda SELECT yetkisi de gerekir.
        GRANT SELECT, DELETE ON hub.delivery_attempts TO svc_dlq_handler;
        GRANT DELETE ON hub.event_instances TO svc_dlq_handler;
        GRANT INSERT ON hub.audit_logs TO svc_dlq_handler;
    END IF;
END
$$;
