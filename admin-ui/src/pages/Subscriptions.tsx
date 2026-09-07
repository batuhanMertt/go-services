import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2 } from 'lucide-react'

import { api, ApiError } from '../lib/api'
import { formatDateTime } from '../lib/format'
import type { Subscription } from '../lib/types'
import {
  Badge,
  Card,
  ConfirmDialog,
  EmptyState,
  ErrorBlock,
  Field,
  LoadingBlock,
  Modal,
  PageHeader,
  Pagination,
  Table,
  TBody,
  THead,
  TR,
  useToast,
} from '../components/ui'

const LIMIT = 25

export default function Subscriptions() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const toast = useToast()

  const [offset, setOffset] = useState(0)
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Subscription | null>(null)
  const [toolId, setToolId] = useState('')
  const [eventTypeId, setEventTypeId] = useState('')
  const [endpoint, setEndpoint] = useState('')
  const [maxRetries, setMaxRetries] = useState(5)
  const [timeoutMs, setTimeoutMs] = useState(10000)
  const [headersText, setHeadersText] = useState('{}')
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [deleteTarget, setDeleteTarget] = useState<Subscription | null>(null)

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['subscriptions', offset],
    queryFn: () => api.listSubscriptions({ limit: LIMIT, offset }),
  })

  // Form için referans listeler (yalnızca modal açıkken çekilir).
  const { data: tools } = useQuery({
    queryKey: ['tools', 'for-select'],
    queryFn: () => api.listTools({ limit: 500 }),
    enabled: formOpen,
  })
  const { data: types } = useQuery({
    queryKey: ['event-types', 'for-select'],
    queryFn: () => api.listEventTypes({ limit: 500 }),
    enabled: formOpen,
  })

  const invalidate = () => void qc.invalidateQueries({ queryKey: ['subscriptions'] })

  const save = useMutation({
    mutationFn: async () => {
      let http_headers: Record<string, string>
      try {
        http_headers = JSON.parse(headersText || '{}') as Record<string, string>
      } catch {
        throw new Error('Ek başlıklar geçerli JSON değil')
      }

      const body = {
        subscriber_tool_id: toolId,
        event_type_id: eventTypeId,
        endpoint_url: endpoint,
        http_headers,
        max_retries: maxRetries,
        timeout_ms: timeoutMs,
      }
      return editing ? api.updateSubscription(editing.id, body) : api.createSubscription(body)
    },
    onSuccess: () => {
      setFormOpen(false)
      setFieldErrors({})
      invalidate()
      toast.push(t('common.save'), 'ok')
    },
    onError: (err) => {
      if (err instanceof ApiError && err.details) setFieldErrors(err.details)
      else toast.push(err instanceof Error ? err.message : String(err), 'danger')
    },
  })

  const remove = useMutation({
    mutationFn: (id: string) => api.deleteSubscription(id),
    onSuccess: () => {
      setDeleteTarget(null)
      invalidate()
      toast.push(t('common.delete'), 'ok')
    },
  })

  function openCreate() {
    setEditing(null)
    setToolId('')
    setEventTypeId('')
    setEndpoint('')
    setMaxRetries(5)
    setTimeoutMs(10000)
    setHeadersText('{}')
    setFieldErrors({})
    setFormOpen(true)
  }

  function openEdit(sub: Subscription) {
    setEditing(sub)
    setToolId(sub.subscriber_tool_id)
    setEventTypeId(sub.event_type_id)
    setEndpoint(sub.endpoint_url)
    setMaxRetries(sub.max_retries)
    setTimeoutMs(sub.timeout_ms)
    setHeadersText(JSON.stringify(sub.http_headers ?? {}, null, 2))
    setFieldErrors({})
    setFormOpen(true)
  }

  return (
    <>
      <PageHeader
        title={t('subscriptions.title')}
        subtitle={t('subscriptions.subtitle')}
        actions={
          <button type="button" className="btn-primary btn-sm" onClick={openCreate}>
            <Plus className="h-3.5 w-3.5" />
            {t('subscriptions.new')}
          </button>
        }
      />

      <Card>
        {isLoading ? (
          <LoadingBlock />
        ) : error ? (
          <ErrorBlock error={error} onRetry={() => void refetch()} />
        ) : !data || data.items.length === 0 ? (
          <EmptyState />
        ) : (
          <>
            <Table>
              <THead>
                <th className="table-head">{t('subscriptions.subscriber')}</th>
                <th className="table-head">{t('subscriptions.eventType')}</th>
                <th className="table-head">{t('subscriptions.endpoint')}</th>
                <th className="table-head">{t('subscriptions.maxRetries')}</th>
                <th className="table-head">{t('common.status')}</th>
                <th className="table-head text-right">{t('common.actions')}</th>
              </THead>
              <TBody>
                {data.items.map((sub) => (
                  <TR key={sub.id}>
                    <td className="table-cell mono text-ink">{sub.subscriber_slug}</td>
                    <td className="table-cell">
                      <span className="mono text-ink">{sub.event_type_name}</span>
                      <Badge tone="brand">v{sub.event_type_version}</Badge>
                    </td>
                    <td className="table-cell mono max-w-xs truncate text-xs text-ink-muted">
                      {sub.endpoint_url}
                    </td>
                    <td className="table-cell text-xs text-ink-muted">
                      {sub.max_retries} · {sub.timeout_ms} ms
                    </td>
                    <td className="table-cell">
                      <Badge tone={sub.is_active ? 'ok' : 'neutral'} dot>
                        {sub.is_active ? t('common.active') : t('common.inactive')}
                      </Badge>
                      <p className="mt-1 text-[11px] text-ink-subtle">
                        {formatDateTime(sub.created_at)}
                      </p>
                    </td>
                    <td className="table-cell">
                      <div className="flex justify-end gap-1.5">
                        <button
                          type="button"
                          className="btn-ghost btn-sm"
                          onClick={() => openEdit(sub)}
                        >
                          {t('common.edit')}
                        </button>
                        <button
                          type="button"
                          className="btn-danger btn-sm"
                          onClick={() => setDeleteTarget(sub)}
                          aria-label={t('common.delete')}
                        >
                          <Trash2 className="h-3.5 w-3.5" />
                        </button>
                      </div>
                    </td>
                  </TR>
                ))}
              </TBody>
            </Table>
            <Pagination total={data.meta.total} limit={LIMIT} offset={offset} onChange={setOffset} />
          </>
        )}
      </Card>

      <Modal
        open={formOpen}
        onClose={() => setFormOpen(false)}
        title={editing ? t('common.edit') : t('subscriptions.new')}
        wide
        footer={
          <>
            <button type="button" className="btn-ghost" onClick={() => setFormOpen(false)}>
              {t('common.cancel')}
            </button>
            <button
              type="button"
              className="btn-primary"
              disabled={save.isPending}
              onClick={() => save.mutate()}
            >
              {t('common.save')}
            </button>
          </>
        }
      >
        <Field label={t('subscriptions.subscriber')} error={fieldErrors.subscriber_tool_id}>
          <select
            className="input"
            value={toolId}
            disabled={!!editing}
            onChange={(e) => setToolId(e.target.value)}
          >
            <option value="">—</option>
            {tools?.items
              .filter((x) => x.kind === 'subscriber' || x.kind === 'both')
              .map((x) => (
                <option key={x.id} value={x.id}>
                  {x.slug} — {x.name}
                </option>
              ))}
          </select>
        </Field>

        <Field label={t('subscriptions.eventType')} error={fieldErrors.event_type_id}>
          <select
            className="input"
            value={eventTypeId}
            disabled={!!editing}
            onChange={(e) => setEventTypeId(e.target.value)}
          >
            <option value="">—</option>
            {types?.items.map((x) => (
              <option key={x.id} value={x.id}>
                {x.name}@{x.version}
              </option>
            ))}
          </select>
        </Field>

        <Field label={t('subscriptions.endpoint')} error={fieldErrors.endpoint_url}>
          <input
            className="input mono"
            value={endpoint}
            onChange={(e) => setEndpoint(e.target.value)}
            placeholder="http://mock-subscriber:8091/webhook"
          />
        </Field>

        <div className="grid gap-x-4 sm:grid-cols-2">
          <Field label={t('subscriptions.maxRetries')} error={fieldErrors.max_retries}>
            <input
              type="number"
              min={0}
              max={20}
              className="input"
              value={maxRetries}
              onChange={(e) => setMaxRetries(Number(e.target.value))}
            />
          </Field>

          <Field label={t('subscriptions.timeout')} error={fieldErrors.timeout_ms}>
            <input
              type="number"
              min={100}
              max={120000}
              step={100}
              className="input"
              value={timeoutMs}
              onChange={(e) => setTimeoutMs(Number(e.target.value))}
            />
          </Field>
        </div>

        <Field label={t('subscriptions.headers')}>
          <textarea
            className="input mono min-h-[96px]"
            spellCheck={false}
            value={headersText}
            onChange={(e) => setHeadersText(e.target.value)}
          />
        </Field>
      </Modal>

      <ConfirmDialog
        open={!!deleteTarget}
        title={t('common.delete')}
        message={t('common.deleteConfirm')}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => deleteTarget && remove.mutate(deleteTarget.id)}
      />
    </>
  )
}
