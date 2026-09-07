#!/bin/bash
# =============================================================================
# Servis başına least-privilege Postgres rolleri (Adım 14).
# Yalnızca veri dizini boşken, ilk container başlangıcında bir kez çalışır.
# Şifre env'den gelir; script içinde hardcoded secret yoktur.
# Yetkiler migrations/0003_grants.up.sql tarafından dağıtılır.
# =============================================================================
set -euo pipefail

: "${DB_SERVICE_PASSWORD:?DB_SERVICE_PASSWORD tanımlı olmalı}"

for role in svc_admin_api svc_event_ingest svc_routing_engine svc_delivery_worker svc_dlq_handler; do
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" <<-EOSQL
    DO \$\$
    BEGIN
      IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '${role}') THEN
        CREATE ROLE ${role} LOGIN PASSWORD '${DB_SERVICE_PASSWORD}';
      ELSE
        ALTER ROLE ${role} WITH LOGIN PASSWORD '${DB_SERVICE_PASSWORD}';
      END IF;
    END
    \$\$;
    GRANT CONNECT ON DATABASE "${POSTGRES_DB}" TO ${role};
EOSQL
  echo "rol hazır: ${role}"
done

# public şemasına yazma yetkisi kimseye verilmez (PG15+ zaten kısıtlı).
psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" \
  -c "REVOKE CREATE ON SCHEMA public FROM PUBLIC;"

echo "Postgres rol bootstrap tamamlandı."
