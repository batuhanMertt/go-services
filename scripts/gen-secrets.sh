#!/usr/bin/env bash
# =============================================================================
# .env dosyasını güçlü, rastgele üretilmiş secret'larla oluşturur.
#
#   ./scripts/gen-secrets.sh          # .env yoksa oluşturur
#   ./scripts/gen-secrets.sh --force  # mevcut .env'i yedekleyip yeniden üretir
#
# Neden gerekli? .env.example içindeki "CHANGE_ME_*" değerleri bilinçli olarak
# çalışmayan yer tutuculardır. Bir örnek dosyaya gerçek secret yazmak, o
# secret'ın er ya da geç versiyon kontrolüne girmesi demektir.
#
# Not: Üretilen parolalar yalnızca [A-Za-z0-9] karakterlerinden oluşur.
# Gerekçe: Postgres bağlantı dizesi bir URL'dir; `@`, `/`, `#` gibi karakterler
# URL-encode edilmeden DSN'i bozar.
# =============================================================================
set -euo pipefail

cd "$(dirname "$0")/.."

FORCE=0
[[ "${1:-}" == "--force" ]] && FORCE=1

if [[ -f .env && $FORCE -eq 0 ]]; then
  echo ".env zaten var. Yeniden üretmek için: $0 --force"
  exit 0
fi

if [[ -f .env ]]; then
  BACKUP=".env.backup.$(date +%Y%m%d%H%M%S)"
  cp .env "$BACKUP"
  echo "Mevcut .env yedeklendi: $BACKUP"
fi

command -v openssl >/dev/null 2>&1 || { echo "HATA: openssl bulunamadı." >&2; exit 1; }

# Alfanümerik parola üretici (URL-safe).
rand_alnum() {
  openssl rand -base64 48 | tr -dc 'A-Za-z0-9' | head -c "${1:-32}"
}

POSTGRES_PASSWORD="$(rand_alnum 32)"
DB_SERVICE_PASSWORD="$(rand_alnum 32)"
REDIS_PASSWORD="$(rand_alnum 32)"
JWT_SECRET="$(rand_alnum 64)"
WEBHOOK_SIGNING_KEY="$(rand_alnum 48)"
ADMIN_PASSWORD="$(rand_alnum 20)"
GRAFANA_ADMIN_PASSWORD="$(rand_alnum 20)"
PGADMIN_PASSWORD="$(rand_alnum 20)"
# AES-256-GCM anahtarı: TAM 32 byte = 64 hex karakter.
SECRET_ENCRYPTION_KEY="$(openssl rand -hex 32)"

cp .env.example .env

# Yer tutucuları üretilen değerlerle değiştir.
replace() {
  local key="$1" value="$2"
  # `|` ayırıcı: değerler `/` içerebilir (burada içermiyorlar ama güvenli taraf).
  sed -i.bak "s|^${key}=.*|${key}=${value}|" .env && rm -f .env.bak
}

replace POSTGRES_PASSWORD      "$POSTGRES_PASSWORD"
replace DB_SERVICE_PASSWORD    "$DB_SERVICE_PASSWORD"
replace REDIS_PASSWORD         "$REDIS_PASSWORD"
replace JWT_SECRET             "$JWT_SECRET"
replace SECRET_ENCRYPTION_KEY  "$SECRET_ENCRYPTION_KEY"
replace WEBHOOK_SIGNING_KEY    "$WEBHOOK_SIGNING_KEY"
replace ADMIN_PASSWORD         "$ADMIN_PASSWORD"
replace GRAFANA_ADMIN_PASSWORD "$GRAFANA_ADMIN_PASSWORD"
replace PGADMIN_PASSWORD       "$PGADMIN_PASSWORD"

chmod 600 .env

cat <<EOF

.env oluşturuldu (izin: 0600).

  Admin paneli : http://localhost:3000
  Kullanıcı    : $(grep '^ADMIN_EMAIL=' .env | cut -d= -f2)
  Şifre        : ${ADMIN_PASSWORD}

  Grafana      : http://localhost:3001  (admin / ${GRAFANA_ADMIN_PASSWORD})

Bu çıktıyı güvenli bir yere kaydedin. .env dosyası .gitignore içindedir.

Sonraki adım:  docker compose up -d --build
EOF
