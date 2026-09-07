import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { RefreshCw } from 'lucide-react'

import { api } from '../lib/api'
import { formatDateTime, formatMs, shortId } from '../lib/format'
import type { DeliveryAttempt } from '../lib/types'
import {
  Badge,
  Card,
  EmptyState,
  ErrorBlock,
  LoadingBlock,
  Modal,
  PageHeader,
  Pagination,
  StatusBadge,
  Table,
  TBody,
  THead,
  TR,
} from '../components/ui'

const LIMIT = 25

function taskTone(status: string) {
  switch (status) {
    case 'succeeded':
      return 'ok' as const
    case 'pending':
    case 'in_flight':
      return 'info' as const
    case 'dead':
      return 'danger' as const
    default:
      return 'warn' as const
  }
}

export default function Deliveries() {
  const { t } = useTranslation()

  const [offset, setOffset] = useState(0)
  const [onlyFailed, setOnlyFailed] = useState(false)
  const [status, setStatus] = useState('')
  const [detail, setDetail] = useState<DeliveryAttempt | null>(null)
  const [live, setLive] = useState(true)

  const { data, isLoading, error, refetch, isFetching } = useQuery({
    queryKey: ['attempts', onlyFailed, status, offset],
    queryFn: () =>
      api.listAttempts({
        only_failed: onlyFailed || undefined,
        status: status || undefined,
        limit: LIMIT,
        offset,
      }),
    refetchInterval: live ? 5000 : false,
    staleTime: 2000,
  })

  return (
    <>
      <PageHeader
        title={t('deliveries.title')}
        subtitle={t('deliveries.subtitle')}
        actions={
          <>
            <select
              className="input w-auto py-1.5 text-xs"
              value={status}
              onChange={(e) => {
                setStatus(e.target.value)
                setOffset(0)
              }}
            >
              <option value="">{t('common.all')}</option>
              <option value="pending">pending</option>
              <option value="in_flight">in_flight</option>
              <option value="succeeded">succeeded</option>
              <option value="dead">dead</option>
            </select>

            <label className="flex cursor-pointer items-center gap-1.5 text-xs text-ink-muted">
              <input
                type="checkbox"
                checked={onlyFailed}
                onChange={(e) => {
                  setOnlyFailed(e.target.checked)
                  setOffset(0)
                }}
                className="accent-brand-600"
              />
              {t('deliveries.onlyFailed')}
            </label>

            <label className="flex cursor-pointer items-center gap-1.5 text-xs text-ink-muted">
              <input
                type="checkbox"
                checked={live}
                onChange={(e) => setLive(e.target.checked)}
                className="accent-brand-600"
              />
              live
            </label>

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
                <th className="table-head">{t('events.receivedAt')}</th>
                <th className="table-head">{t('common.type')}</th>
                <th className="table-head">{t('subscriptions.subscriber')}</th>
                <th className="table-head">{t('deliveries.attempt')}</th>
                <th className="table-head">{t('deliveries.responseStatus')}</th>
                <th className="table-head">{t('deliveries.latency')}</th>
                <th className="table-head">{t('common.status')}</th>
              </THead>
              <TBody>
                {data.items.map((a) => (
                  <TR key={a.id} onClick={() => setDetail(a)}>
                    <td className="table-cell whitespace-nowrap text-xs text-ink-muted">
                      {formatDateTime(a.attempted_at)}
                    </td>
                    <td className="table-cell">
                      <span className="mono text-ink">{a.event_type}</span>
                      <p className="mono mt-0.5 text-[11px] text-ink-subtle">
                        {shortId(a.event_id, 10)}
                      </p>
                    </td>
                    <td className="table-cell mono text-xs text-ink-muted">{a.subscriber_slug}</td>
                    <td className="table-cell tabular-nums text-sm text-ink">#{a.attempt_no}</td>
                    <td className="table-cell">
                      <StatusBadge status={a.response_status} />
                    </td>
                    <td className="table-cell tabular-nums text-xs text-ink-muted">
                      {formatMs(a.latency_ms)}
                    </td>
                    <td className="table-cell">
                      <Badge tone={taskTone(a.task_status)} dot>
                        {a.task_status}
                      </Badge>
                      {a.next_attempt_at && a.task_status === 'pending' && (
                        <p className="mt-1 text-[11px] text-ink-subtle">
                          {t('deliveries.nextAttempt')}: {formatDateTime(a.next_attempt_at)}
                        </p>
                      )}
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
        title={detail ? `${t('deliveries.attempt')} #${detail.attempt_no}` : ''}
        wide
      >
        {detail && (
          <>
            <dl className="mb-5 grid gap-x-6 gap-y-2.5 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('deliveries.target')}
                </dt>
                <dd className="mono mt-0.5 break-all text-ink">{detail.target_url}</dd>
              </div>
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('events.eventId')}
                </dt>
                <dd className="mono mt-0.5 break-all text-ink">{detail.event_id}</dd>
              </div>
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('deliveries.responseStatus')}
                </dt>
                <dd className="mt-0.5">
                  <StatusBadge status={detail.response_status} />
                </dd>
              </div>
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('deliveries.latency')}
                </dt>
                <dd className="mt-0.5 tabular-nums text-ink">{formatMs(detail.latency_ms)}</dd>
              </div>
            </dl>

            {detail.error && (
              <>
                <h3 className="label">{t('deliveries.errorMsg')}</h3>
                <pre className="mono mb-4 overflow-auto rounded-lg border border-danger/30 bg-danger/5 p-3 text-danger">
                  {detail.error}
                </pre>
              </>
            )}

            {detail.response_body && (
              <>
                <h3 className="label">{t('deliveries.responseBody')}</h3>
                <pre className="mono max-h-64 overflow-auto rounded-lg border border-edge bg-surface-sunken p-3 text-ink-muted">
                  {detail.response_body}
                </pre>
              </>
            )}
          </>
        )}
      </Modal>
    </>
  )
}
