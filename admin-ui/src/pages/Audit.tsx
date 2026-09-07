import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { RefreshCw } from 'lucide-react'

import { api } from '../lib/api'
import { formatDateTime } from '../lib/format'
import type { AuditLog } from '../lib/types'
import {
  Badge,
  Card,
  EmptyState,
  ErrorBlock,
  JsonBlock,
  LoadingBlock,
  Modal,
  PageHeader,
  Pagination,
  Table,
  TBody,
  THead,
  TR,
} from '../components/ui'

const LIMIT = 30

function actionTone(action: string) {
  if (action.endsWith('.delete') || action.includes('failed') || action.includes('revoke')) {
    return 'danger' as const
  }
  if (action.includes('rotate') || action.includes('replay') || action.includes('discard')) {
    return 'warn' as const
  }
  if (action.endsWith('.create')) return 'ok' as const
  return 'neutral' as const
}

export default function Audit() {
  const { t } = useTranslation()

  const [offset, setOffset] = useState(0)
  const [action, setAction] = useState('')
  const [detail, setDetail] = useState<AuditLog | null>(null)

  const { data, isLoading, error, refetch, isFetching } = useQuery({
    queryKey: ['audit', action, offset],
    queryFn: () => api.listAudit({ action: action || undefined, limit: LIMIT, offset }),
  })

  return (
    <>
      <PageHeader
        title={t('audit.title')}
        subtitle={t('audit.subtitle')}
        actions={
          <>
            <input
              className="input w-56 py-1.5 text-xs"
              placeholder="tool.create"
              value={action}
              onChange={(e) => {
                setAction(e.target.value.trim())
                setOffset(0)
              }}
            />
            <button
              type="button"
              className="btn-ghost btn-sm"
              onClick={() => void refetch()}
              aria-label={t('common.refresh')}
            >
              <RefreshCw className={`h-3.5 w-3.5 ${isFetching ? 'animate-spin' : ''}`} />
            </button>
          </>
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
                <th className="table-head">{t('common.createdAt')}</th>
                <th className="table-head">{t('audit.actor')}</th>
                <th className="table-head">{t('audit.action')}</th>
                <th className="table-head">{t('audit.resource')}</th>
                <th className="table-head">IP</th>
                <th className="table-head text-right">{t('common.actions')}</th>
              </THead>
              <TBody>
                {data.items.map((log) => (
                  <TR key={log.id} onClick={() => setDetail(log)}>
                    <td className="table-cell whitespace-nowrap text-xs text-ink-muted">
                      {formatDateTime(log.created_at)}
                    </td>
                    <td className="table-cell">
                      <p className="text-sm text-ink">{log.actor_label || log.actor_id || '—'}</p>
                      <Badge tone={log.actor_type === 'system' ? 'info' : 'neutral'}>
                        {log.actor_type}
                      </Badge>
                    </td>
                    <td className="table-cell">
                      <Badge tone={actionTone(log.action)}>{log.action}</Badge>
                    </td>
                    <td className="table-cell mono text-xs text-ink-muted">
                      {log.resource_type}
                      {log.resource_id && (
                        <span className="text-ink-subtle"> · {log.resource_id.slice(0, 8)}…</span>
                      )}
                    </td>
                    <td className="table-cell mono text-xs text-ink-subtle">{log.ip || '—'}</td>
                    <td className="table-cell text-right">
                      <button type="button" className="btn-ghost btn-sm">
                        {t('common.details')}
                      </button>
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
        open={!!detail}
        onClose={() => setDetail(null)}
        title={detail?.action ?? ''}
        wide
      >
        {detail && (
          <>
            <dl className="mb-5 grid gap-x-6 gap-y-2.5 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('audit.actor')}
                </dt>
                <dd className="mt-0.5 text-ink">
                  {detail.actor_label || detail.actor_id || '—'} ({detail.actor_type})
                </dd>
              </div>
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('audit.resource')}
                </dt>
                <dd className="mono mt-0.5 break-all text-ink">
                  {detail.resource_type} {detail.resource_id}
                </dd>
              </div>
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('events.traceId')}
                </dt>
                <dd className="mono mt-0.5 break-all text-ink">{detail.trace_id || '—'}</dd>
              </div>
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">User-Agent</dt>
                <dd className="mt-0.5 break-all text-xs text-ink-muted">
                  {detail.user_agent || '—'}
                </dd>
              </div>
            </dl>

            {detail.before_state != null && (
              <div className="mb-4">
                <h3 className="label">{t('audit.before')}</h3>
                <JsonBlock value={detail.before_state} maxHeight={220} />
              </div>
            )}

            {detail.after_state != null && (
              <div>
                <h3 className="label">{t('audit.after')}</h3>
                <JsonBlock value={detail.after_state} maxHeight={220} />
              </div>
            )}
          </>
        )}
      </Modal>
    </>
  )
}
