import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { FlaskConical, Plus, Trash2 } from 'lucide-react'

import { api, ApiError } from '../lib/api'
import { prettyJson } from '../lib/format'
import type { RoutingRule } from '../lib/types'
import {
  Badge,
  Card,
  ConfirmDialog,
  EmptyState,
  ErrorBlock,
  Field,
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

const CONDITION_TEMPLATE = `{
  "op": "and",
  "rules": [
    { "path": "payload.status", "op": "eq", "value": "active" }
  ]
}`

const SAMPLE_TEMPLATE = `{
  "event_type": "user.created",
  "source_tool": "mock-publisher-1",
  "payload": {
    "status": "active",
    "email": "ada@mindmons.local"
  },
  "metadata": {}
}`

export default function RoutingRules() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const toast = useToast()

  const [offset, setOffset] = useState(0)
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<RoutingRule | null>(null)
  const [name, setName] = useState('')
  const [subscriptionId, setSubscriptionId] = useState('')
  const [priority, setPriority] = useState(100)
  const [conditionText, setConditionText] = useState(CONDITION_TEMPLATE)
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [deleteTarget, setDeleteTarget] = useState<RoutingRule | null>(null)
  const [detail, setDetail] = useState<RoutingRule | null>(null)

  const [testOpen, setTestOpen] = useState(false)
  const [sampleText, setSampleText] = useState(SAMPLE_TEMPLATE)
  const [testResult, setTestResult] = useState<boolean | null>(null)

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['routing-rules', offset],
    queryFn: () => api.listRules({ limit: LIMIT, offset }),
  })

  const { data: subs } = useQuery({
    queryKey: ['subscriptions', 'for-select'],
    queryFn: () => api.listSubscriptions({ limit: 500 }),
    enabled: formOpen,
  })

  const invalidate = () => void qc.invalidateQueries({ queryKey: ['routing-rules'] })

  const save = useMutation({
    mutationFn: async () => {
      let condition: unknown
      try {
        condition = JSON.parse(conditionText || '{}')
      } catch {
        throw new Error('Koşul geçerli JSON değil')
      }

      const body = { name, subscription_id: subscriptionId, condition, priority }
      return editing ? api.updateRule(editing.id, body) : api.createRule(body)
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
    mutationFn: (id: string) => api.deleteRule(id),
    onSuccess: () => {
      setDeleteTarget(null)
      invalidate()
      toast.push(t('common.delete'), 'ok')
    },
  })

  const runTest = useMutation({
    mutationFn: async () => {
      const condition = JSON.parse(conditionText || '{}')
      const sample = JSON.parse(sampleText || '{}')
      return api.testRule(condition, sample)
    },
    onSuccess: (res) => setTestResult(res.matched),
    onError: (err) => toast.push(err instanceof Error ? err.message : String(err), 'danger'),
  })

  function openCreate() {
    setEditing(null)
    setName('')
    setSubscriptionId('')
    setPriority(100)
    setConditionText(CONDITION_TEMPLATE)
    setFieldErrors({})
    setFormOpen(true)
  }

  function openEdit(rule: RoutingRule) {
    setEditing(rule)
    setName(rule.name)
    setSubscriptionId(rule.subscription_id)
    setPriority(rule.priority)
    setConditionText(prettyJson(rule.condition))
    setFieldErrors({})
    setFormOpen(true)
  }

  const isUnconditional = (c: unknown) =>
    !c || (typeof c === 'object' && Object.keys(c as object).length === 0)

  return (
    <>
      <PageHeader
        title={t('rules.title')}
        subtitle={t('rules.subtitle')}
        actions={
          <button type="button" className="btn-primary btn-sm" onClick={openCreate}>
            <Plus className="h-3.5 w-3.5" />
            {t('rules.new')}
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
                <th className="table-head">{t('common.name')}</th>
                <th className="table-head">{t('subscriptions.eventType')}</th>
                <th className="table-head">{t('subscriptions.endpoint')}</th>
                <th className="table-head">{t('rules.condition')}</th>
                <th className="table-head">{t('rules.priority')}</th>
                <th className="table-head text-right">{t('common.actions')}</th>
              </THead>
              <TBody>
                {data.items.map((rule) => (
                  <TR key={rule.id}>
                    <td className="table-cell">
                      <p className="text-ink">{rule.name}</p>
                      <Badge tone={rule.is_active ? 'ok' : 'neutral'}>
                        {rule.is_active ? t('common.active') : t('common.inactive')}
                      </Badge>
                    </td>
                    <td className="table-cell mono text-xs text-ink-muted">
                      {rule.event_type_name ?? '—'}
                    </td>
                    <td className="table-cell mono max-w-[220px] truncate text-xs text-ink-muted">
                      {rule.endpoint_url ?? '—'}
                    </td>
                    <td className="table-cell">
                      {isUnconditional(rule.condition) ? (
                        <Badge tone="info">koşulsuz</Badge>
                      ) : (
                        <button
                          type="button"
                          className="btn-ghost btn-sm"
                          onClick={() => setDetail(rule)}
                        >
                          {t('common.details')}
                        </button>
                      )}
                    </td>
                    <td className="table-cell tabular-nums text-xs text-ink-muted">
                      {rule.priority}
                    </td>
                    <td className="table-cell">
                      <div className="flex justify-end gap-1.5">
                        <button
                          type="button"
                          className="btn-ghost btn-sm"
                          onClick={() => openEdit(rule)}
                        >
                          {t('common.edit')}
                        </button>
                        <button
                          type="button"
                          className="btn-danger btn-sm"
                          onClick={() => setDeleteTarget(rule)}
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
        title={editing ? `${t('common.edit')}: ${editing.name}` : t('rules.new')}
        wide
        footer={
          <>
            <button
              type="button"
              className="btn-ghost mr-auto"
              onClick={() => {
                setTestResult(null)
                setTestOpen(true)
              }}
            >
              <FlaskConical className="h-4 w-4" />
              {t('rules.test')}
            </button>
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
        <Field label={t('common.name')} error={fieldErrors.name}>
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} />
        </Field>

        <Field label={t('rules.subscription')} error={fieldErrors.subscription_id}>
          <select
            className="input"
            value={subscriptionId}
            disabled={!!editing}
            onChange={(e) => setSubscriptionId(e.target.value)}
          >
            <option value="">—</option>
            {subs?.items.map((s) => (
              <option key={s.id} value={s.id}>
                {s.subscriber_slug} ← {s.event_type_name}@{s.event_type_version}
              </option>
            ))}
          </select>
        </Field>

        <Field label={t('rules.priority')}>
          <input
            type="number"
            className="input"
            value={priority}
            onChange={(e) => setPriority(Number(e.target.value) || 100)}
          />
        </Field>

        <Field
          label={t('rules.condition')}
          hint={t('rules.conditionHint')}
          error={fieldErrors.condition}
        >
          <textarea
            className="input mono min-h-[200px]"
            spellCheck={false}
            value={conditionText}
            onChange={(e) => setConditionText(e.target.value)}
          />
        </Field>
      </Modal>

      {/* Kural test aracı: kural canlıya çıkmadan doğrulanır. */}
      <Modal
        open={testOpen}
        onClose={() => setTestOpen(false)}
        title={t('rules.test')}
        wide
        footer={
          <>
            <button type="button" className="btn-ghost" onClick={() => setTestOpen(false)}>
              {t('common.close')}
            </button>
            <button
              type="button"
              className="btn-primary"
              disabled={runTest.isPending}
              onClick={() => runTest.mutate()}
            >
              <FlaskConical className="h-4 w-4" />
              {t('rules.test')}
            </button>
          </>
        }
      >
        <Field label={t('rules.condition')}>
          <textarea
            className="input mono min-h-[140px]"
            spellCheck={false}
            value={conditionText}
            onChange={(e) => setConditionText(e.target.value)}
          />
        </Field>

        <Field label={t('rules.testSample')}>
          <textarea
            className="input mono min-h-[160px]"
            spellCheck={false}
            value={sampleText}
            onChange={(e) => setSampleText(e.target.value)}
          />
        </Field>

        {testResult !== null && (
          <div
            className={`rounded-lg border px-4 py-3 text-sm font-medium ${
              testResult
                ? 'border-ok/40 bg-ok/10 text-ok'
                : 'border-warn/40 bg-warn/10 text-warn'
            }`}
          >
            {t('rules.testResult')}: {testResult ? t('rules.matched') : t('rules.notMatched')}
          </div>
        )}
      </Modal>

      <Modal
        open={!!detail}
        onClose={() => setDetail(null)}
        title={detail?.name ?? ''}
        wide
      >
        {detail && <JsonBlock value={detail.condition} maxHeight={420} />}
      </Modal>

      <ConfirmDialog
        open={!!deleteTarget}
        title={`${t('common.delete')}: ${deleteTarget?.name ?? ''}`}
        message={t('common.deleteConfirm')}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => deleteTarget && remove.mutate(deleteTarget.id)}
      />
    </>
  )
}
