import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, RefreshCw, RotateCcw, XCircle } from 'lucide-react'

import { api } from '../lib/api'
import { formatDateTime, shortId } from '../lib/format'
import type { DlqEvent, DlqStatus } from '../lib/types'
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
  useToast,
} from '../components/ui'

const LIMIT = 25

const statusTone: Record<DlqStatus, 'danger' | 'warn' | 'ok' | 'neutral'> = {
  new: 'danger',
  replay_requested: 'warn',
  replayed: 'ok',
  discarded: 'neutral',
}

const statusLabelKey: Record<DlqStatus, string> = {
  new: 'dlq.statusNew',
  replay_requested: 'dlq.statusRequested',
  replayed: 'dlq.statusReplayed',
  discarded: 'dlq.statusDiscarded',
}

export default function Dlq() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const toast = useToast()

  const [offset, setOffset] = useState(0)
  const [status, setStatus] = useState('')
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [detail, setDetail] = useState<DlqEvent | null>(null)

  const { data, isLoading, error, refetch, isFetching } = useQuery({
    queryKey: ['dlq', status, offset],
    queryFn: () => api.listDlq({ status: status || undefined, limit: LIMIT, offset }),
    refetchInterval: 8000,
    staleTime: 3000,
  })

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['dlq'] })
    void qc.invalidateQueries({ queryKey: ['stats'] })
  }

  const replay = useMutation({
    mutationFn: (ids: string[]) => api.replayDlq({ ids }),
    onSuccess: (res) => {
      setSelected(new Set())
      invalidate()
      toast.push(`${t('dlq.replayed')}: ${res.replayed}`, 'ok')
    },
    onError: (err) => toast.push(err instanceof Error ? err.message : String(err), 'danger'),
  })

  const discard = useMutation({
    mutationFn: (ids: string[]) => api.discardDlq(ids),
    onSuccess: (res) => {
      setSelected(new Set())
      invalidate()
      toast.push(`${t('dlq.discard')}: ${res.discarded}`, 'ok')
    },
    onError: (err) => toast.push(err instanceof Error ? err.message : String(err), 'danger'),
  })

  function toggle(id: string) {
    setSelected((prev) => {
      const next = new Set(prev)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  const selectableIds = (data?.items ?? [])
    .filter((d) => d.status === 'new' || d.status === 'replayed')
    .map((d) => d.id)

  const allSelected = selectableIds.length > 0 && selectableIds.every((id) => selected.has(id))

  return (
    <>
      <PageHeader
        title={t('dlq.title')}
        subtitle={t('dlq.subtitle')}
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
              <option value="new">{t('dlq.statusNew')}</option>
              <option value="replay_requested">{t('dlq.statusRequested')}</option>
              <option value="replayed">{t('dlq.statusReplayed')}</option>
              <option value="discarded">{t('dlq.statusDiscarded')}</option>
            </select>

            {selected.size > 0 && (
              <>
                <button
                  type="button"
                  className="btn-primary btn-sm"
                  disabled={replay.isPending}
                  onClick={() => replay.mutate([...selected])}
                >
                  <RotateCcw className="h-3.5 w-3.5" />
                  {t('dlq.replaySelected', { count: selected.size })}
                </button>
                <button
                  type="button"
                  className="btn-danger btn-sm"
                  disabled={discard.isPending}
                  onClick={() => discard.mutate([...selected])}
                >
                  <XCircle className="h-3.5 w-3.5" />
                  {t('dlq.discard')}
                </button>
              </>
            )}

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
          <EmptyState
            message={t('dlq.emptyGood')}
            icon={<CheckCircle2 className="h-7 w-7 text-ok" />}
          />
        ) : (
          <>
            <Table>
              <THead>
                <th className="table-head w-10">
                  <input
                    type="checkbox"
                    className="accent-brand-600"
                    aria-label={t('dlq.selectAll')}
                    checked={allSelected}
                    onChange={(e) =>
                      setSelected(e.target.checked ? new Set(selectableIds) : new Set())
                    }
                  />
                </th>
                <th className="table-head">{t('common.createdAt')}</th>
                <th className="table-head">{t('common.type')}</th>
                <th className="table-head">{t('subscriptions.subscriber')}</th>
                <th className="table-head">{t('dlq.attempts')}</th>
                <th className="table-head">{t('dlq.reason')}</th>
                <th className="table-head">{t('common.status')}</th>
                <th className="table-head text-right">{t('common.actions')}</th>
              </THead>
              <TBody>
                {data.items.map((d) => (
                  <TR key={d.id}>
                    <td className="table-cell">
                      <input
                        type="checkbox"
                        className="accent-brand-600"
                        checked={selected.has(d.id)}
                        disabled={d.status === 'discarded' || d.status === 'replay_requested'}
                        onChange={() => toggle(d.id)}
                        aria-label={d.event_id}
                      />
                    </td>
                    <td className="table-cell whitespace-nowrap text-xs text-ink-muted">
                      {formatDateTime(d.created_at)}
                    </td>
                    <td className="table-cell">
                      <span className="mono text-ink">{d.event_type}</span>
                      <p className="mono mt-0.5 text-[11px] text-ink-subtle">
                        {shortId(d.event_id, 10)}
                      </p>
                    </td>
                    <td className="table-cell mono text-xs text-ink-muted">
                      {d.subscriber_slug || '—'}
                    </td>
                    <td className="table-cell tabular-nums text-sm text-ink">{d.attempts}</td>
                    <td className="table-cell max-w-xs truncate text-xs text-danger">
                      {d.reason}
                    </td>
                    <td className="table-cell">
                      <Badge tone={statusTone[d.status]} dot>
                        {t(statusLabelKey[d.status])}
                      </Badge>
                    </td>
                    <td className="table-cell">
                      <div className="flex justify-end gap-1.5">
                        <button
                          type="button"
                          className="btn-ghost btn-sm"
                          onClick={() => setDetail(d)}
                        >
                          {t('common.details')}
                        </button>
                        {(d.status === 'new' || d.status === 'replayed') && (
                          <button
                            type="button"
                            className="btn-primary btn-sm"
                            onClick={() => replay.mutate([d.id])}
                          >
                            <RotateCcw className="h-3.5 w-3.5" />
                            {t('dlq.replay')}
                          </button>
                        )}
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
        open={!!detail}
        onClose={() => setDetail(null)}
        title={detail ? `DLQ · ${detail.event_type}` : ''}
        wide
        footer={
          detail && (detail.status === 'new' || detail.status === 'replayed') ? (
            <button
              type="button"
              className="btn-primary"
              onClick={() => {
                replay.mutate([detail.id])
                setDetail(null)
              }}
            >
              <RotateCcw className="h-4 w-4" />
              {t('dlq.replay')}
            </button>
          ) : undefined
        }
      >
        {detail && (
          <>
            <dl className="mb-5 grid gap-x-6 gap-y-2.5 text-sm sm:grid-cols-2">
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('events.eventId')}
                </dt>
                <dd className="mono mt-0.5 break-all text-ink">{detail.event_id}</dd>
              </div>
              <div>
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('deliveries.target')}
                </dt>
                <dd className="mono mt-0.5 break-all text-ink">{detail.target_url}</dd>
              </div>
              <div className="sm:col-span-2">
                <dt className="text-xs uppercase tracking-wider text-ink-subtle">
                  {t('dlq.reason')}
                </dt>
                <dd className="mt-0.5 text-danger">{detail.reason}</dd>
              </div>
            </dl>

            <h3 className="label">{t('events.payload')}</h3>
            <JsonBlock value={detail.payload} maxHeight={360} />
          </>
        )}
      </Modal>
    </>
  )
}
