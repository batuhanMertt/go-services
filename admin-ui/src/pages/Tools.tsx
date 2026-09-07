import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { KeyRound, Plus, RefreshCw, ShieldAlert, Trash2 } from 'lucide-react'

import { api, ApiError } from '../lib/api'
import { formatDateTime, formatRelative } from '../lib/format'
import type { ApiKeyIssued, Tool, ToolKind } from '../lib/types'
import {
  Badge,
  Card,
  ConfirmDialog,
  CopyButton,
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

const emptyForm = {
  slug: '',
  name: '',
  description: '',
  kind: 'publisher' as ToolKind,
  contact_email: '',
}

export default function Tools() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const toast = useToast()

  const [offset, setOffset] = useState(0)
  const [kindFilter, setKindFilter] = useState('')
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Tool | null>(null)
  const [form, setForm] = useState(emptyForm)
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [keysFor, setKeysFor] = useState<Tool | null>(null)
  const [issued, setIssued] = useState<ApiKeyIssued | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<Tool | null>(null)

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['tools', kindFilter, offset],
    queryFn: () => api.listTools({ kind: kindFilter || undefined, limit: LIMIT, offset }),
  })

  const invalidate = () => void qc.invalidateQueries({ queryKey: ['tools'] })

  const saveMutation = useMutation({
    mutationFn: async () => {
      const body = { ...form }
      return editing ? api.updateTool(editing.id, body) : api.createTool(body)
    },
    onSuccess: () => {
      setFormOpen(false)
      setFieldErrors({})
      invalidate()
      toast.push(editing ? t('common.save') : t('common.create'), 'ok')
    },
    onError: (err) => {
      if (err instanceof ApiError && err.details) setFieldErrors(err.details)
      else toast.push(err instanceof Error ? err.message : String(err), 'danger')
    },
  })

  const deleteMutation = useMutation({
    mutationFn: (id: string) => api.deleteTool(id),
    onSuccess: () => {
      setDeleteTarget(null)
      invalidate()
      toast.push(t('common.delete'), 'ok')
    },
    onError: (err) => toast.push(err instanceof Error ? err.message : String(err), 'danger'),
  })

  function openCreate() {
    setEditing(null)
    setForm(emptyForm)
    setFieldErrors({})
    setFormOpen(true)
  }

  function openEdit(tool: Tool) {
    setEditing(tool)
    setForm({
      slug: tool.slug,
      name: tool.name,
      description: tool.description,
      kind: tool.kind,
      contact_email: tool.contact_email,
    })
    setFieldErrors({})
    setFormOpen(true)
  }

  return (
    <>
      <PageHeader
        title={t('tools.title')}
        subtitle={t('tools.subtitle')}
        actions={
          <>
            <select
              className="input w-auto py-1.5 text-xs"
              value={kindFilter}
              onChange={(e) => {
                setKindFilter(e.target.value)
                setOffset(0)
              }}
            >
              <option value="">{t('common.all')}</option>
              <option value="publisher">{t('tools.publisher')}</option>
              <option value="subscriber">{t('tools.subscriber')}</option>
              <option value="both">{t('tools.both')}</option>
            </select>
            <button type="button" className="btn-primary btn-sm" onClick={openCreate}>
              <Plus className="h-3.5 w-3.5" />
              {t('tools.new')}
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
                <th className="table-head">{t('tools.slug')}</th>
                <th className="table-head">{t('common.name')}</th>
                <th className="table-head">{t('tools.kind')}</th>
                <th className="table-head">{t('common.status')}</th>
                <th className="table-head">{t('common.createdAt')}</th>
                <th className="table-head text-right">{t('common.actions')}</th>
              </THead>
              <TBody>
                {data.items.map((tool) => (
                  <TR key={tool.id}>
                    <td className="table-cell">
                      <span className="mono text-ink">{tool.slug}</span>
                    </td>
                    <td className="table-cell">
                      <p className="text-ink">{tool.name}</p>
                      {tool.description && (
                        <p className="mt-0.5 max-w-md truncate text-xs text-ink-subtle">
                          {tool.description}
                        </p>
                      )}
                    </td>
                    <td className="table-cell">
                      <Badge tone={tool.kind === 'publisher' ? 'brand' : tool.kind === 'subscriber' ? 'info' : 'neutral'}>
                        {t(`tools.${tool.kind}`)}
                      </Badge>
                    </td>
                    <td className="table-cell">
                      <Badge tone={tool.status === 'active' ? 'ok' : 'warn'} dot>
                        {tool.status}
                      </Badge>
                    </td>
                    <td className="table-cell text-xs text-ink-muted">
                      {formatDateTime(tool.created_at)}
                    </td>
                    <td className="table-cell">
                      <div className="flex justify-end gap-1.5">
                        <button
                          type="button"
                          className="btn-ghost btn-sm"
                          onClick={() => setKeysFor(tool)}
                        >
                          <KeyRound className="h-3.5 w-3.5" />
                          {t('tools.keys')}
                        </button>
                        <button
                          type="button"
                          className="btn-ghost btn-sm"
                          onClick={() => openEdit(tool)}
                        >
                          {t('common.edit')}
                        </button>
                        <button
                          type="button"
                          className="btn-danger btn-sm"
                          onClick={() => setDeleteTarget(tool)}
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
            <Pagination
              total={data.meta.total}
              limit={LIMIT}
              offset={offset}
              onChange={setOffset}
            />
          </>
        )}
      </Card>

      {/* --- Tool oluştur / düzenle --- */}
      <Modal
        open={formOpen}
        onClose={() => setFormOpen(false)}
        title={editing ? `${t('common.edit')}: ${editing.slug}` : t('tools.new')}
        footer={
          <>
            <button type="button" className="btn-ghost" onClick={() => setFormOpen(false)}>
              {t('common.cancel')}
            </button>
            <button
              type="button"
              className="btn-primary"
              disabled={saveMutation.isPending}
              onClick={() => saveMutation.mutate()}
            >
              {t('common.save')}
            </button>
          </>
        }
      >
        <Field label={t('tools.slug')} hint={t('tools.slugHint')} error={fieldErrors.slug}>
          <input
            className="input mono"
            value={form.slug}
            disabled={!!editing}
            onChange={(e) => setForm({ ...form, slug: e.target.value })}
            placeholder="billing-service"
          />
        </Field>

        <Field label={t('common.name')} error={fieldErrors.name}>
          <input
            className="input"
            value={form.name}
            onChange={(e) => setForm({ ...form, name: e.target.value })}
          />
        </Field>

        <Field label={t('tools.kind')} error={fieldErrors.kind}>
          <select
            className="input"
            value={form.kind}
            onChange={(e) => setForm({ ...form, kind: e.target.value as ToolKind })}
          >
            <option value="publisher">{t('tools.publisher')}</option>
            <option value="subscriber">{t('tools.subscriber')}</option>
            <option value="both">{t('tools.both')}</option>
          </select>
        </Field>

        <Field label={t('tools.description')}>
          <textarea
            className="input min-h-[72px]"
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
          />
        </Field>

        <Field label={t('tools.contact')}>
          <input
            type="email"
            className="input"
            value={form.contact_email}
            onChange={(e) => setForm({ ...form, contact_email: e.target.value })}
          />
        </Field>
      </Modal>

      {keysFor && (
        <KeysModal
          tool={keysFor}
          onClose={() => setKeysFor(null)}
          onIssued={(k) => setIssued(k)}
        />
      )}

      {/* --- Tek seferlik secret gösterimi --- */}
      <Modal
        open={!!issued}
        onClose={() => setIssued(null)}
        title={t('tools.issueKey')}
        wide
        footer={
          <button type="button" className="btn-primary" onClick={() => setIssued(null)}>
            {t('common.close')}
          </button>
        }
      >
        {issued && (
          <>
            <div className="mb-4 flex items-start gap-2.5 rounded-lg border border-warn/40 bg-warn/10 px-3.5 py-3 text-sm text-warn">
              <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0" />
              <p>{t('tools.secretOnce')}</p>
            </div>

            <Field label={t('tools.apiKey')}>
              <div className="flex gap-2">
                <input className="input mono" readOnly value={issued.api_key} />
                <CopyButton text={issued.api_key} label="" />
              </div>
            </Field>

            <Field label={t('tools.hmacSecret')}>
              <div className="flex gap-2">
                <input className="input mono" readOnly value={issued.hmac_secret} />
                <CopyButton text={issued.hmac_secret} label="" />
              </div>
            </Field>

            {issued.warning && <p className="text-xs text-ink-muted">{issued.warning}</p>}
          </>
        )}
      </Modal>

      <ConfirmDialog
        open={!!deleteTarget}
        title={`${t('common.delete')}: ${deleteTarget?.slug ?? ''}`}
        message={t('common.deleteConfirm')}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => deleteTarget && deleteMutation.mutate(deleteTarget.id)}
      />
    </>
  )
}

/* ------------------------------------------------------- API anahtarları modalı */

function KeysModal({
  tool,
  onClose,
  onIssued,
}: {
  tool: Tool
  onClose: () => void
  onIssued: (k: ApiKeyIssued) => void
}) {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const toast = useToast()
  const [grace, setGrace] = useState('24h')

  const { data, isLoading } = useQuery({
    queryKey: ['keys', tool.id],
    queryFn: () => api.listKeys(tool.id),
  })

  const refresh = () => void qc.invalidateQueries({ queryKey: ['keys', tool.id] })

  const issue = useMutation({
    mutationFn: () => api.issueKey(tool.id),
    onSuccess: (k) => {
      onIssued(k)
      refresh()
    },
    onError: (e) => toast.push(e instanceof Error ? e.message : String(e), 'danger'),
  })

  const rotate = useMutation({
    mutationFn: () => api.rotateKey(tool.id, grace),
    onSuccess: (k) => {
      onIssued(k)
      refresh()
    },
    onError: (e) => toast.push(e instanceof Error ? e.message : String(e), 'danger'),
  })

  const revoke = useMutation({
    mutationFn: (keyId: string) => api.revokeKey(tool.id, keyId),
    onSuccess: () => {
      refresh()
      toast.push(t('tools.revokeKey'), 'ok')
    },
  })

  return (
    <Modal open onClose={onClose} title={`${t('tools.keys')} · ${tool.slug}`} wide>
      <div className="mb-4 flex flex-wrap items-end gap-2">
        <button
          type="button"
          className="btn-primary btn-sm"
          disabled={issue.isPending}
          onClick={() => issue.mutate()}
        >
          <Plus className="h-3.5 w-3.5" />
          {t('tools.issueKey')}
        </button>

        <div className="flex items-end gap-2">
          <div>
            <label className="label">{t('tools.gracePeriod')}</label>
            <input
              className="input w-28"
              value={grace}
              onChange={(e) => setGrace(e.target.value)}
              placeholder="24h"
            />
          </div>
          <button
            type="button"
            className="btn-ghost btn-sm"
            disabled={rotate.isPending}
            onClick={() => rotate.mutate()}
          >
            <RefreshCw className="h-3.5 w-3.5" />
            {t('tools.rotateKey')}
          </button>
        </div>
      </div>
      <p className="mb-4 text-xs text-ink-subtle">{t('tools.gracePeriodHint')}</p>

      {isLoading ? (
        <LoadingBlock />
      ) : !data || data.items.length === 0 ? (
        <EmptyState />
      ) : (
        <Table>
          <THead>
            <th className="table-head">{t('tools.keyPrefix')}</th>
            <th className="table-head">{t('common.status')}</th>
            <th className="table-head">{t('tools.lastUsed')}</th>
            <th className="table-head">{t('tools.graceUntil')}</th>
            <th className="table-head text-right">{t('common.actions')}</th>
          </THead>
          <TBody>
            {data.items.map((k) => (
              <TR key={k.id}>
                <td className="table-cell mono">{k.key_prefix}</td>
                <td className="table-cell">
                  <Badge
                    tone={k.status === 'active' ? 'ok' : k.status === 'rotating' ? 'warn' : 'danger'}
                  >
                    {k.status}
                  </Badge>
                </td>
                <td className="table-cell text-xs text-ink-muted">
                  {k.last_used_at ? formatRelative(k.last_used_at) : t('common.never')}
                </td>
                <td className="table-cell text-xs text-ink-muted">
                  {k.grace_until ? formatDateTime(k.grace_until) : '—'}
                </td>
                <td className="table-cell text-right">
                  {k.status !== 'revoked' && (
                    <button
                      type="button"
                      className="btn-danger btn-sm"
                      onClick={() => revoke.mutate(k.id)}
                    >
                      {t('tools.revokeKey')}
                    </button>
                  )}
                </td>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
    </Modal>
  )
}
