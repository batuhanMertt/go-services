SET search_path TO hub, public;

DO $$
DECLARE
    r TEXT;
BEGIN
    FOREACH r IN ARRAY ARRAY[
        'svc_admin_api', 'svc_event_ingest', 'svc_routing_engine',
        'svc_delivery_worker', 'svc_dlq_handler'
    ] LOOP
        IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = r) THEN
            EXECUTE format('REVOKE ALL ON ALL TABLES IN SCHEMA hub FROM %I', r);
            EXECUTE format('REVOKE ALL ON ALL SEQUENCES IN SCHEMA hub FROM %I', r);
            EXECUTE format('REVOKE USAGE ON SCHEMA hub FROM %I', r);
        END IF;
    END LOOP;
END
$$;
