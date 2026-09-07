// Admin API kontratının (docs/api-contracts/admin-api.openapi.yaml) TypeScript
// karşılığı. Spec değişirse burası da güncellenir — tek doğruluk kaynağı spec'tir.

export interface ListMeta {
  total: number
  limit: number
  offset: number
}

export interface ListResponse<T> {
  items: T[]
  meta: ListMeta
}

export interface ApiErrorBody {
  error: {
    code: string
    message: string
    details?: Record<string, string>
    trace_id?: string
  }
}

export type ToolKind = 'publisher' | 'subscriber' | 'both'
export type ToolStatus = 'active' | 'suspended' | 'disabled'

export interface Tool {
  id: string
  slug: string
  name: string
  description: string
  kind: ToolKind
  status: ToolStatus
  contact_email: string
  metadata: Record<string, unknown>
  created_at: string
  updated_at: string
}

export interface ApiKeyInfo {
  id: string
  tool_id: string
  key_prefix: string
  status: 'active' | 'rotating' | 'revoked'
  grace_until?: string | null
  expires_at?: string | null
  last_used_at?: string | null
  created_at: string
}

export interface ApiKeyIssued extends ApiKeyInfo {
  api_key: string
  hmac_secret: string
  warning: string
}

export interface EventType {
  id: string
  owner_tool_id?: string | null
  name: string
  version: number
  description: string
  json_schema: unknown
  is_active: boolean
  created_at: string
  updated_at: string
}

export interface Subscription {
  id: string
  subscriber_tool_id: string
  subscriber_slug: string
  event_type_id: string
  event_type_name: string
  event_type_version: number
  endpoint_url: string
  http_headers: Record<string, string>
  max_retries: number
  timeout_ms: number
  is_active: boolean
  created_at: string
  updated_at: string
}

export interface RoutingRule {
  id: string
  name: string
  subscription_id: string
  condition: unknown
  priority: number
  is_active: boolean
  event_type_name?: string
  endpoint_url?: string
  created_at: string
  updated_at: string
}

export type EventStatus = 'accepted' | 'routed' | 'no_match' | 'failed'

export interface EventInstance {
  id: string
  event_id: string
  event_type: string
  event_version: number
  source_tool: string
  payload: unknown
  metadata: unknown
  trace_id: string
  status: EventStatus
  received_at: string
  routed_at?: string | null
}

export interface DeliveryAttempt {
  id: number
  delivery_task_id: string
  event_id: string
  event_type: string
  subscriber_slug: string
  target_url: string
  attempt_no: number
  response_status?: number | null
  response_body?: string
  latency_ms: number
  error?: string
  attempted_at: string
  task_status: string
  next_attempt_at?: string | null
}

export type DlqStatus = 'new' | 'replay_requested' | 'replayed' | 'discarded'

export interface DlqEvent {
  id: string
  delivery_task_id?: string | null
  subscription_id?: string | null
  subscriber_slug: string
  event_id: string
  event_type: string
  target_url: string
  reason: string
  attempts: number
  payload: unknown
  status: DlqStatus
  created_at: string
  replayed_at?: string | null
}

export interface AuditLog {
  id: number
  actor_type: 'admin' | 'tool' | 'system'
  actor_id: string
  actor_label: string
  action: string
  resource_type: string
  resource_id: string
  before_state?: unknown
  after_state?: unknown
  ip: string
  user_agent: string
  trace_id: string
  created_at: string
}

export interface AdminUser {
  id: string
  email: string
  display_name: string
  role: 'admin' | 'operator' | 'viewer'
  is_active: boolean
  last_login_at?: string | null
  created_at: string
}

export interface TokenResponse {
  access_token: string
  refresh_token: string
  token_type: string
  expires_at: string
  user: AdminUser
}

export interface NameCount {
  name: string
  count: number
}

export interface TimeCount {
  bucket: string
  count: number
}

export interface DashboardStats {
  tools: { total: number; active: number }
  subscriptions: { total: number; active: number }
  events: { last_24h: number; last_hour: number; total: number }
  deliveries: {
    succeeded_24h: number
    failed_24h: number
    pending: number
    success_rate: number
  }
  dlq: { pending: number; total: number; replayed: number }
  outbox: { pending: number }
  events_per_type: NameCount[]
  events_timeline: TimeCount[]
  top_subscribers: NameCount[]
  generated_at: string
}
