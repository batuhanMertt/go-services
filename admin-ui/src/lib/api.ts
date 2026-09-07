import type { ApiErrorBody, ListResponse, TokenResponse } from './types'

const BASE = (import.meta.env.VITE_API_BASE_URL as string | undefined) ?? '/api'

const REFRESH_STORAGE_KEY = 'hub.refresh_token'

/**
 * ApiError, backend'in tek biçimli hata gövdesini taşır.
 * `details` alan bazlı doğrulama hatalarını içerir; formlar bunu doğrudan
 * ilgili alanın altında gösterir.
 */
export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly details?: Record<string, string>
  readonly traceId?: string

  constructor(status: number, body: ApiErrorBody | null, fallback: string) {
    super(body?.error?.message ?? fallback)
    this.name = 'ApiError'
    this.status = status
    this.code = body?.error?.code ?? 'unknown_error'
    this.details = body?.error?.details
    this.traceId = body?.error?.trace_id
  }
}

/**
 * Token yönetimi.
 *
 * Access token yalnızca BELLEKTE tutulur; localStorage'a yazılmaz. Gerekçe:
 * XSS ile çalınabilecek bir değerin kalıcı depolamada durması gereksiz risktir.
 * Refresh token localStorage'dadır çünkü sayfa yenilendiğinde oturumun
 * sürmesi gerekir — ancak tek kullanımlıktır (backend her yenilemede
 * rotate eder), bu yüzden çalınan bir refresh token en fazla bir kez
 * kullanılabilir ve kullanıldığında gerçek kullanıcının oturumu bozulur;
 * bu da hırsızlığın tespit edilebilir olmasını sağlar.
 */
let accessToken: string | null = null
let onUnauthenticated: (() => void) | null = null

export const tokens = {
  setAccess(token: string | null) {
    accessToken = token
  },
  getAccess() {
    return accessToken
  },
  setRefresh(token: string | null) {
    try {
      if (token) localStorage.setItem(REFRESH_STORAGE_KEY, token)
      else localStorage.removeItem(REFRESH_STORAGE_KEY)
    } catch {
      /* private mode: oturum yalnızca sekme ömrü boyunca sürer */
    }
  },
  getRefresh(): string | null {
    try {
      return localStorage.getItem(REFRESH_STORAGE_KEY)
    } catch {
      return null
    }
  },
  clear() {
    accessToken = null
    this.setRefresh(null)
  },
  onLogout(fn: () => void) {
    onUnauthenticated = fn
  },
}

async function parseError(res: Response): Promise<ApiError> {
  let body: ApiErrorBody | null = null
  try {
    body = (await res.json()) as ApiErrorBody
  } catch {
    /* gövde JSON değil */
  }
  return new ApiError(res.status, body, `İstek başarısız (HTTP ${res.status})`)
}

/**
 * refreshAccessToken, süresi dolan access token'ı yeniler.
 *
 * Eşzamanlı istekler aynı anda 401 alırsa TEK bir yenileme yapılır;
 * `inFlightRefresh` promise'i paylaşılır. Aksi halde her istek ayrı bir
 * refresh tetikler ve rotation nedeniyle hepsi birbirinin token'ını
 * geçersiz kılardı.
 */
let inFlightRefresh: Promise<boolean> | null = null

async function refreshAccessToken(): Promise<boolean> {
  if (inFlightRefresh) return inFlightRefresh

  inFlightRefresh = (async () => {
    const refresh = tokens.getRefresh()
    if (!refresh) return false

    try {
      const res = await fetch(`${BASE}/v1/auth/refresh`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ refresh_token: refresh }),
      })
      if (!res.ok) {
        tokens.clear()
        return false
      }

      const data = (await res.json()) as TokenResponse
      tokens.setAccess(data.access_token)
      tokens.setRefresh(data.refresh_token)
      return true
    } catch {
      return false
    } finally {
      inFlightRefresh = null
    }
  })()

  return inFlightRefresh
}

interface RequestOptions {
  method?: string
  body?: unknown
  query?: Record<string, string | number | boolean | undefined | null>
  signal?: AbortSignal
  /** true ise 401'de yenileme denenmez (login/refresh uçları için). */
  skipAuth?: boolean
}

async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, query, signal, skipAuth } = opts

  const url = new URL(`${BASE}/v1${path}`, window.location.origin)
  if (query) {
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== null && v !== '') url.searchParams.set(k, String(v))
    }
  }

  const send = async (): Promise<Response> => {
    const headers: Record<string, string> = {}
    if (body !== undefined) headers['Content-Type'] = 'application/json'
    if (!skipAuth && accessToken) headers.Authorization = `Bearer ${accessToken}`

    return fetch(url.toString(), {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined,
      signal,
    })
  }

  let res = await send()

  // Access token süresi dolduysa bir kez yenile ve isteği tekrarla.
  if (res.status === 401 && !skipAuth) {
    const refreshed = await refreshAccessToken()
    if (refreshed) {
      res = await send()
    } else {
      tokens.clear()
      onUnauthenticated?.()
      throw await parseError(res)
    }
  }

  if (!res.ok) throw await parseError(res)

  if (res.status === 204) return undefined as T

  const text = await res.text()
  if (!text) return undefined as T
  return JSON.parse(text) as T
}

// --- Uç noktalar --------------------------------------------------------------

export const api = {
  // Auth
  login: (email: string, password: string) =>
    request<TokenResponse>('/auth/login', {
      method: 'POST',
      body: { email, password },
      skipAuth: true,
    }),

  logout: () => request<void>('/auth/logout', { method: 'POST' }),

  me: () => request<import('./types').AdminUser>('/auth/me'),

  changePassword: (current_password: string, new_password: string) =>
    request<void>('/auth/change-password', {
      method: 'POST',
      body: { current_password, new_password },
    }),

  // Tools
  listTools: (query?: Record<string, string | number | undefined>) =>
    request<ListResponse<import('./types').Tool>>('/tools', { query }),

  createTool: (body: unknown) =>
    request<import('./types').Tool>('/tools', { method: 'POST', body }),

  updateTool: (id: string, body: unknown) =>
    request<import('./types').Tool>(`/tools/${id}`, { method: 'PUT', body }),

  deleteTool: (id: string) => request<void>(`/tools/${id}`, { method: 'DELETE' }),

  listKeys: (toolId: string) =>
    request<{ items: import('./types').ApiKeyInfo[] }>(`/tools/${toolId}/keys`),

  issueKey: (toolId: string) =>
    request<import('./types').ApiKeyIssued>(`/tools/${toolId}/keys`, { method: 'POST', body: {} }),

  rotateKey: (toolId: string, grace_period?: string) =>
    request<import('./types').ApiKeyIssued>(`/tools/${toolId}/keys/rotate`, {
      method: 'POST',
      body: { grace_period },
    }),

  revokeKey: (toolId: string, keyId: string) =>
    request<void>(`/tools/${toolId}/keys/${keyId}`, { method: 'DELETE' }),

  // Event types
  listEventTypes: (query?: Record<string, string | number | undefined>) =>
    request<ListResponse<import('./types').EventType>>('/event-types', { query }),

  createEventType: (body: unknown) =>
    request<import('./types').EventType>('/event-types', { method: 'POST', body }),

  updateEventType: (id: string, body: unknown) =>
    request<import('./types').EventType>(`/event-types/${id}`, { method: 'PUT', body }),

  deleteEventType: (id: string) => request<void>(`/event-types/${id}`, { method: 'DELETE' }),

  // Subscriptions
  listSubscriptions: (query?: Record<string, string | number | undefined>) =>
    request<ListResponse<import('./types').Subscription>>('/subscriptions', { query }),

  createSubscription: (body: unknown) =>
    request<import('./types').Subscription>('/subscriptions', { method: 'POST', body }),

  updateSubscription: (id: string, body: unknown) =>
    request<import('./types').Subscription>(`/subscriptions/${id}`, { method: 'PUT', body }),

  deleteSubscription: (id: string) => request<void>(`/subscriptions/${id}`, { method: 'DELETE' }),

  // Routing rules
  listRules: (query?: Record<string, string | number | undefined>) =>
    request<ListResponse<import('./types').RoutingRule>>('/routing-rules', { query }),

  createRule: (body: unknown) =>
    request<import('./types').RoutingRule>('/routing-rules', { method: 'POST', body }),

  updateRule: (id: string, body: unknown) =>
    request<import('./types').RoutingRule>(`/routing-rules/${id}`, { method: 'PUT', body }),

  deleteRule: (id: string) => request<void>(`/routing-rules/${id}`, { method: 'DELETE' }),

  testRule: (condition: unknown, sample: unknown) =>
    request<{ matched: boolean; note: string }>('/routing-rules/test', {
      method: 'POST',
      body: { condition, sample },
    }),

  // Operations
  listEvents: (query?: Record<string, string | number | undefined>) =>
    request<ListResponse<import('./types').EventInstance>>('/events', { query }),

  getEvent: (id: string) => request<import('./types').EventInstance>(`/events/${id}`),

  listAttempts: (query?: Record<string, string | number | boolean | undefined>) =>
    request<ListResponse<import('./types').DeliveryAttempt>>('/delivery-attempts', { query }),

  listDlq: (query?: Record<string, string | number | undefined>) =>
    request<ListResponse<import('./types').DlqEvent>>('/dlq', { query }),

  replayDlq: (body: { ids?: string[]; subscription_id?: string; limit?: number }) =>
    request<{ requested: number; replayed: number; skipped: number }>('/dlq/replay', {
      method: 'POST',
      body,
    }),

  discardDlq: (ids: string[]) =>
    request<{ discarded: number }>('/dlq/discard', { method: 'POST', body: { ids } }),

  listAudit: (query?: Record<string, string | number | undefined>) =>
    request<ListResponse<import('./types').AuditLog>>('/audit-logs', { query }),

  stats: () => request<import('./types').DashboardStats>('/stats'),
}

/**
 * streamEvents, canlı event akışını tüketir.
 *
 * Neden `EventSource` değil? EventSource özel HTTP başlığı gönderemez; token'ı
 * sorgu parametresine koymak gerekirdi. Token'lar URL'de proxy loglarına,
 * tarayıcı geçmişine ve Referer başlığına sızar. `fetch` + ReadableStream ile
 * SSE'yi elle çözümlemek, `Authorization` başlığını kullanmamıza izin verir.
 *
 * Bedeli: otomatik yeniden bağlanmayı kendimiz yazmak zorundayız (çağıran
 * taraf `onClose` ile tekrar bağlanır).
 */
export async function streamEvents(
  onEvent: (data: unknown) => void,
  signal: AbortSignal,
): Promise<void> {
  const res = await fetch(`${BASE}/v1/stream/events`, {
    headers: accessToken ? { Authorization: `Bearer ${accessToken}` } : {},
    signal,
  })

  if (!res.ok || !res.body) {
    throw await parseError(res)
  }

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  while (!signal.aborted) {
    const { done, value } = await reader.read()
    if (done) break

    buffer += decoder.decode(value, { stream: true })

    // SSE mesajları boş satırla ayrılır.
    let sep: number
    while ((sep = buffer.indexOf('\n\n')) !== -1) {
      const chunk = buffer.slice(0, sep)
      buffer = buffer.slice(sep + 2)

      let eventName = 'message'
      const dataLines: string[] = []

      for (const line of chunk.split('\n')) {
        if (line.startsWith(':')) continue // heartbeat yorumu
        if (line.startsWith('event:')) eventName = line.slice(6).trim()
        else if (line.startsWith('data:')) dataLines.push(line.slice(5).trim())
      }

      if (eventName === 'event' && dataLines.length > 0) {
        try {
          onEvent(JSON.parse(dataLines.join('\n')))
        } catch {
          /* bozuk kare atlanır */
        }
      }
    }
  }
}
