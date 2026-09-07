import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { RefreshCw, Search } from 'lucide-react'

import { api } from '../lib/api'
import { formatDateTime, shortId } from '../lib/format'
import type { EventInstance } from '../lib/types'
import {
  Badge,
  Card,
  CopyButton,
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

const LIMIT = 25

function statusTone(status: string) {
  switch (status) {
    case 'routed':
      return 'ok' as const
    case 'no_match':
      return 'warn' as const
    case 'failed':
      return 'danger' as const
    default:
      return 'info' as const
  }
}

export default function Events() {
  const { t } = useTranslation()

  const [offset, setOffset] = useState(0)
  const [search, setSearch] = useState('')
  const [searchInput, setSearchInput] = useState('')
  const [status, setStatus] = useState('')
  const [detail, setDetail] = useState<EventInstance | null>(null)
  const [live, setLive] = useState(true)

  const { data, isLoading, error, refetch, isFetching } = useQuery({
    queryKey: ['events', search, status, offset],
    queryFn: () =>
      api.listEvents({
        search: search || undefined,
        status: status || undefined,
        limit: LIMIT,
        offset,
      }),
    // Operasyonel sayfa: canlı takip açıkken 5 saniyede bir tazelenir.
    refetchInterval: live ? 5000 : false,
    staleTime: 2000,
  })

  return (
    <>
      <PageHeader
        title={t('events.title')}
        subtitle={t('events.subtitle')}
        actions={
          <>
            <form
              className="relative"
              onSubmit={(e) => {
                e.preventDefault()
                setSearch(searchInput.trim())
                setOffset(0)
              }}
            >
              <Search className="pointer-events-none absolute left-2.5 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-ink-subtle" />
              <input
                className="input w-64 py-1.5 pl-8 text-xs"
                placeholder={t('events.searchHint')}
                value={searchInput}
                onChange={(e) => setSearchInput(e.target.value)}
              />
            </form>

            <select
              className="input w-auto py-1.5 text-xs"
              value={status}
              onChange={(e) => {
                setStatus(e.target.value)
                setOffset(0)
              }}
            >
              <option value="">{t('common.all')}</option>
              <option value="accepted">{t('events.statusAccepted')}</option>
              <option value="routed">{t('events.statusRouted')}</option>
              <option value="no_match">{t('events.statusNoMatch')}</option>
              <option value="failed">{t('events.statusFailed')}</option>
            </select>

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
                <th className="table-head">{t('events.source')}</th>
                <th className="table-head">{t('events.eventId')}</th>
                <th className="table-head">{t('common.status')}</th>
                <th className="table-head text-right">{t('common.actions')}</th>
              </THead>
              <TBody>
                {data.items.map((ev) => (
                  <TR key={ev.id} onClick={() => setDetail(ev)}>
                    <td className="table-cell whitespace-nowrap text-xs text-ink-muted">
                      {formatDateTime(ev.received_at)}
                    </td>
                    <td className="table-cell">
                      <span className="mono text-ink">{ev.event_type}</span>
                      <Badge tone="brand">v{ev.event_version}</Badge>
                    </td>
                    <td className="table-cell mono text-xs text-ink-muted">{ev.source_tool}</td>
                    <td className="table-cell mono text-xs text-ink-muted">
                      {shortId(ev.event_id, 12)}
                    </td>
                    <td className="table-cell">
                      <Badge tone={statusTone(ev.status)} dot>
                        {ev.status}
                      </Badge>
                    </td>
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
        title={detail ? `${detail.event_type}@${detail.event_version}` : ''}
        wide
      >
        {detail && (
          <>
            <dl className="mb-5 grid gap-x-6 gap-y-2.5 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('events.eventId')}
                </dt>
                <dd className="mono mt-0.5 flex items-center gap-2 break-all text-ink">
                  {detail.event_id}
                  <CopyButton text={detail.event_id} label="" />
                </dd>
              </div>
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('events.traceId')}
                </dt>
                <dd className="mono mt-0.5 flex items-center gap-2 break-all text-ink">
                  {detail.trace_id || '—'}
                  {detail.trace_id && <CopyButton text={detail.trace_id} label="" />}
                </dd>
              </div>
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('events.source')}
                </dt>
                <dd className="mono mt-0.5 text-ink">{detail.source_tool}</dd>
              </div>
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('events.receivedAt')}
                </dt>
                <dd className="mt-0.5 text-ink">{formatDateTime(detail.received_at)}</dd>
              </div>
            </dl>

            <h3 className="label">{t('events.payload')}</h3>
            <div className="mb-4">
              <JsonBlock value={detail.payload} />
            </div>

            <h3 className="label">{t('events.metadata')}</h3>
            <JsonBlock value={detail.metadata} maxHeight={180} />
          </>
        )}
      </Modal>
    </>
  )
}
