-- =============================================================================
-- 0004_seed_event_types — demo/ekosistem başlangıç event tipleri
-- Şemalar docs/api-contracts/events/ altındaki dosyaların birebir kopyasıdır.
-- owner_tool_id NULL: tool'lar kendilerini çalışma zamanında kaydeder, ancak
-- kontrat (şema) platformun sahipliğindedir ve tool'dan önce var olabilir.
-- Not: admin kullanıcısı bilerek burada seed EDİLMEZ — argon2id hash'i SQL'de
-- üretilemez; admin-api açılışta ADMIN_EMAIL/ADMIN_PASSWORD ile bootstrap eder.
-- =============================================================================
SET search_path TO hub, public;

INSERT INTO hub.event_types (name, version, description, json_schema)
VALUES
('user.created', 1,
 'Kullanıcı yönetimi tool''u yeni bir kullanıcı oluşturduğunda yayınlanır.',
 '{
   "$schema": "https://json-schema.org/draft/2020-12/schema",
   "title": "user.created v1",
   "type": "object",
   "required": ["user_id", "email", "status"],
   "additionalProperties": true,
   "properties": {
     "user_id":    { "type": "string", "minLength": 1 },
     "email":      { "type": "string", "format": "email" },
     "full_name":  { "type": "string" },
     "status":     { "type": "string", "enum": ["active", "pending", "suspended"] },
     "tenant_id":  { "type": "string" },
     "created_at": { "type": "string", "format": "date-time" }
   }
 }'::jsonb),

('document.generated', 1,
 'Belge üretim tool''u bir belgeyi tamamladığında yayınlanır.',
 '{
   "$schema": "https://json-schema.org/draft/2020-12/schema",
   "title": "document.generated v1",
   "type": "object",
   "required": ["document_id", "document_type", "status"],
   "additionalProperties": true,
   "properties": {
     "document_id":   { "type": "string", "minLength": 1 },
     "document_type": { "type": "string", "enum": ["invoice", "contract", "report", "certificate"] },
     "owner_user_id": { "type": "string" },
     "status":        { "type": "string", "enum": ["draft", "final", "archived"] },
     "size_bytes":    { "type": "integer", "minimum": 0 },
     "storage_url":   { "type": "string" }
   }
 }'::jsonb),

('verification.completed', 1,
 'Regülasyon/doğrulama tool''u bir kontrolü tamamladığında yayınlanır.',
 '{
   "$schema": "https://json-schema.org/draft/2020-12/schema",
   "title": "verification.completed v1",
   "type": "object",
   "required": ["verification_id", "subject_id", "result"],
   "additionalProperties": true,
   "properties": {
     "verification_id": { "type": "string", "minLength": 1 },
     "subject_id":      { "type": "string", "minLength": 1 },
     "subject_type":    { "type": "string", "enum": ["user", "document", "transaction"] },
     "result":          { "type": "string", "enum": ["passed", "failed", "manual_review"] },
     "score":           { "type": "number", "minimum": 0, "maximum": 100 },
     "regulation":      { "type": "string" }
   }
 }'::jsonb)
ON CONFLICT (name, version) DO NOTHING;
