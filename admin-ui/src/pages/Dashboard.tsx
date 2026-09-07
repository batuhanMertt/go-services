import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  Area,
  AreaChart,
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { Activity, Boxes, Inbox, Send, ServerCrash, Share2 } from 'lucide-react'

import { api, streamEvents } from '../lib/api'
import { formatNumber, formatPercent, formatTime, shortId } from '../lib/format'
import type { EventInstance } from '../lib/types'
import {
  Badge,
  Card,
  ErrorBlock,
  LoadingBlock,
  PageHeader,
  StatCard,
} from '../components/ui'

/** Grafiklerde kullanılan marka paleti (CSS değişkenlerinden bağımsız sabitler). */
const CHART_COLORS = ['#645df0', '#8586f6', '#3fb950', '#d8a02d', '#58a0ff', '#f8645c']

function useLiveEvents(limit = 12) {
  const [events, setEvents] = useState<EventInstance[]>([])
  const [connected, setConnected] = useState(false)
  const retryRef = useRef<number | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    let cancelled = false

    async function connect() {
      try {
        setConnected(true)
        await streamEvents((data) => {
          const ev = data as EventInstance
          setEvents((prev) => [ev, ...prev].slice(0, limit))
        }, controller.signal)
      } catch {
        /* bağlantı koptu */
      } finally {
        if (!cancelled) {
          setConnected(false)
          // Otomatik yeniden bağlanma: fetch tabanlı SSE'de bunu elle yazmak
          // zorundayız (EventSource'un aksine).
          retryRef.current = window.setTimeout(connect, 3000)
        }
      }
    }

    void connect()

    return () => {
      cancelled = true
      controller.abort()
      if (retryRef.current) window.clearTimeout(retryRef.current)
    }
  }, [limit])

  return { events, connected }
}

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

export default function Dashboard() {
  const { t } = useTranslation()
  const { events, connected } = useLiveEvents()

  const {
    data: stats,
    isLoading,
    error,
    refetch,
  } = useQuery({
    queryKey: ['stats'],
    queryFn: () => api.stats(),
    refetchInterval: 10_000,
    staleTime: 5_000,
  })

  if (isLoading) return <LoadingBlock />
  if (error) return <ErrorBlock error={error} onRetry={() => void refetch()} />
  if (!stats) return null

  const timeline = stats.events_timeline.map((p) => ({
    label: formatTime(p.bucket).slice(0, 5),
    count: p.count,
  }))

  // Sistem sağlığı özeti: DLQ birikmesi en güçlü uyarı sinyalidir.
  const health =
    stats.dlq.pending > 10
      ? { tone: 'danger' as const, label: t('dashboard.critical') }
      : stats.dlq.pending > 0 || stats.deliveries.success_rate < 0.9
        ? { tone: 'warn' as const, label: t('dashboard.degraded') }
        : { tone: 'ok' as const, label: t('dashboard.healthy') }

  return (
    <>
      <PageHeader
        title={t('dashboard.title')}
        subtitle={t('dashboard.subtitle')}
        actions={
          <Badge tone={health.tone} dot>
            {health.label}
          </Badge>
        }
      />

      <div className="mb-5 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label={t('dashboard.tools')}
          value={formatNumber(stats.tools.active)}
          hint={`${formatNumber(stats.tools.total)} ${t('common.total').toLowerCase()}`}
          icon={<Boxes className="h-4 w-4" />}
          tone="brand"
        />
        <StatCard
          label={t('dashboard.subscriptions')}
          value={formatNumber(stats.subscriptions.active)}
          hint={`${formatNumber(stats.subscriptions.total)} ${t('common.total').toLowerCase()}`}
          icon={<Share2 className="h-4 w-4" />}
        />
        <StatCard
          label={t('dashboard.events24h')}
          value={formatNumber(stats.events.last_24h)}
          hint={t('dashboard.eventsHour', { count: stats.events.last_hour })}
          icon={<Send className="h-4 w-4" />}
        />
        <StatCard
          label={t('dashboard.successRate')}
          value={formatPercent(stats.deliveries.success_rate)}
          hint={`${formatNumber(stats.deliveries.succeeded_24h)} / ${formatNumber(
            stats.deliveries.succeeded_24h + stats.deliveries.failed_24h,
          )}`}
          icon={<Activity className="h-4 w-4" />}
          tone={
            stats.deliveries.success_rate >= 0.98
              ? 'ok'
              : stats.deliveries.success_rate >= 0.9
                ? 'warn'
                : 'danger'
          }
        />
      </div>

      <div className="mb-5 grid gap-4 sm:grid-cols-3">
        <StatCard
          label={t('dashboard.pendingDeliveries')}
          value={formatNumber(stats.deliveries.pending)}
          icon={<Inbox className="h-4 w-4" />}
        />
        <StatCard
          label={t('dashboard.dlqPending')}
          value={formatNumber(stats.dlq.pending)}
          hint={`${formatNumber(stats.dlq.replayed)} replay`}
          icon={<ServerCrash className="h-4 w-4" />}
          tone={stats.dlq.pending > 0 ? 'danger' : 'ok'}
        />
        <StatCard
          label={t('dashboard.outboxPending')}
          value={formatNumber(stats.outbox.pending)}
          tone={stats.outbox.pending > 100 ? 'warn' : 'neutral'}
        />
      </div>

      <div className="mb-5 grid gap-4 xl:grid-cols-3">
        <Card title={t('dashboard.timeline')} className="xl:col-span-2">
          <div className="h-64 px-2 py-4">
            <ResponsiveContainer width="100%" height="100%">
              <AreaChart data={timeline} margin={{ top: 6, right: 12, left: -20, bottom: 0 }}>
                <defs>
                  <linearGradient id="eventFill" x1="0" y1="0" x2="0" y2="1">
                    <stop offset="0%" stopColor="#645df0" stopOpacity={0.45} />
                    <stop offset="100%" stopColor="#645df0" stopOpacity={0.02} />
                  </linearGradient>
                </defs>
                <CartesianGrid strokeDasharray="3 3" stroke="rgb(var(--edge))" vertical={false} />
                <XAxis
                  dataKey="label"
                  tick={{ fontSize: 11, fill: 'rgb(var(--ink-subtle))' }}
                  axisLine={false}
                  tickLine={false}
                  interval="preserveStartEnd"
                />
                <YAxis
                  tick={{ fontSize: 11, fill: 'rgb(var(--ink-subtle))' }}
                  axisLine={false}
                  tickLine={false}
                  allowDecimals={false}
                  width={44}
                />
                <Tooltip
                  contentStyle={{
                    background: 'rgb(var(--surface-overlay))',
                    border: '1px solid rgb(var(--edge))',
                    borderRadius: 10,
                    fontSize: 12,
                    color: 'rgb(var(--ink))',
                  }}
                  labelStyle={{ color: 'rgb(var(--ink-muted))' }}
                />
                <Area
                  type="monotone"
                  dataKey="count"
                  stroke="#645df0"
                  strokeWidth={2}
                  fill="url(#eventFill)"
                />
              </AreaChart>
            </ResponsiveContainer>
          </div>
        </Card>

        <Card title={t('dashboard.byType')}>
          <div className="h-64 px-2 py-4">
            {stats.events_per_type.length === 0 ? (
              <div className="grid h-full place-items-center text-xs text-ink-subtle">
                {t('common.empty')}
              </div>
            ) : (
              <ResponsiveContainer width="100%" height="100%">
                <BarChart
                  data={stats.events_per_type}
                  layout="vertical"
                  margin={{ top: 4, right: 16, left: 8, bottom: 4 }}
                >
                  <CartesianGrid strokeDasharray="3 3" stroke="rgb(var(--edge))" horizontal={false} />
                  <XAxis
                    type="number"
                    tick={{ fontSize: 11, fill: 'rgb(var(--ink-subtle))' }}
                    axisLine={false}
                    tickLine={false}
                    allowDecimals={false}
                  />
                  <YAxis
                    type="category"
                    dataKey="name"
                    tick={{ fontSize: 11, fill: 'rgb(var(--ink-muted))' }}
                    axisLine={false}
                    tickLine={false}
                    width={128}
                  />
                  <Tooltip
                    cursor={{ fill: 'rgb(var(--surface-sunken))' }}
                    contentStyle={{
                      background: 'rgb(var(--surface-overlay))',
                      border: '1px solid rgb(var(--edge))',
                      borderRadius: 10,
                      fontSize: 12,
                    }}
                  />
                  <Bar dataKey="count" radius={[0, 6, 6, 0]}>
                    {stats.events_per_type.map((_, i) => (
                      <Cell key={i} fill={CHART_COLORS[i % CHART_COLORS.length]} />
                    ))}
                  </Bar>
                </BarChart>
              </ResponsiveContainer>
            )}
          </div>
        </Card>
      </div>

      <div className="grid gap-4 xl:grid-cols-3">
        <Card
          className="xl:col-span-2"
          title={t('dashboard.liveStream')}
          actions={
            <span className="flex items-center gap-1.5 text-[11px] text-ink-muted">
              <span
                className={`h-1.5 w-1.5 rounded-full ${
                  connected ? 'animate-pulse-dot bg-ok' : 'bg-danger'
                }`}
              />
              {connected ? t('dashboard.liveConnected') : t('dashboard.liveDisconnected')}
            </span>
          }
        >
          {events.length === 0 ? (
            <p className="px-5 py-10 text-center text-sm text-ink-muted">
              {t('dashboard.liveWaiting')}
            </p>
          ) : (
            <ul className="divide-y divide-edge">
              {events.map((ev) => (
                <li
                  key={ev.id}
                  className="animate-fade-in flex flex-wrap items-center gap-x-3 gap-y-1 px-5 py-2.5 text-sm"
                >
                  <span className="mono text-ink-subtle">{formatTime(ev.received_at)}</span>
                  <span className="font-medium text-ink">{ev.event_type}</span>
                  <Badge tone={statusTone(ev.status)}>{ev.status}</Badge>
                  <span className="text-xs text-ink-muted">{ev.source_tool}</span>
                  <span className="mono ml-auto text-xs text-ink-subtle">
                    {shortId(ev.event_id, 10)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Card>

        <Card title={t('dashboard.topSubscribers')}>
          {stats.top_subscribers.length === 0 ? (
            <p className="px-5 py-10 text-center text-sm text-ink-muted">{t('common.empty')}</p>
          ) : (
            <ul className="divide-y divide-edge">
              {stats.top_subscribers.map((s, i) => (
                <li key={s.name} className="flex items-center gap-3 px-5 py-2.5 text-sm">
                  <span
                    className="h-2 w-2 shrink-0 rounded-full"
                    style={{ background: CHART_COLORS[i % CHART_COLORS.length] }}
                  />
                  <span className="truncate text-ink">{s.name}</span>
                  <span className="mono ml-auto tabular-nums text-ink-muted">
                    {formatNumber(s.count)}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </div>
    </>
  )
}
