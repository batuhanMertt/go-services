import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { Plus, Trash2 } from 'lucide-react'

import { api, ApiError } from '../lib/api'
import { formatDateTime, prettyJson } from '../lib/format'
import type { EventType } from '../lib/types'
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

const SCHEMA_TEMPLATE = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "required": ["id"],
  "additionalProperties": true,
  "properties": {
    "id": { "type": "string", "minLength": 1 }
  }
}`

export default function EventTypes() {
  const { t } = useTranslation()
  const qc = useQueryClient()
  const toast = useToast()

  const [offset, setOffset] = useState(0)
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<EventType | null>(null)
  const [name, setName] = useState('')
  const [version, setVersion] = useState(1)
  const [description, setDescription] = useState('')
  const [schemaText, setSchemaText] = useState(SCHEMA_TEMPLATE)
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({})
  const [detail, setDetail] = useState<EventType | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<EventType | null>(null)

  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['event-types', offset],
    queryFn: () => api.listEventTypes({ limit: LIMIT, offset }),
  })

  const invalidate = () => void qc.invalidateQueries({ queryKey: ['event-types'] })

  const save = useMutation({
    mutationFn: async () => {
      let json_schema: unknown
      try {
        json_schema = JSON.parse(schemaText)
      } catch {
        throw new Error('JSON Schema geçerli JSON değil')
      }

      const body = { name, version, description, json_schema }
      return editing ? api.updateEventType(editing.id, body) : api.createEventType(body)
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
    mutationFn: (id: string) => api.deleteEventType(id),
    onSuccess: () => {
      setDeleteTarget(null)
      invalidate()
      toast.push(t('common.delete'), 'ok')
    },
    onError: (err) => toast.push(err instanceof Error ? err.message : String(err), 'danger'),
  })

  function openCreate() {
    setEditing(null)
    setName('')
    setVersion(1)
    setDescription('')
    setSchemaText(SCHEMA_TEMPLATE)
    setFieldErrors({})
    setFormOpen(true)
  }

  function openEdit(et: EventType) {
    setEditing(et)
    setName(et.name)
    setVersion(et.version)
    setDescription(et.description)
    setSchemaText(prettyJson(et.json_schema))
    setFieldErrors({})
    setFormOpen(true)
  }

  return (
    <>
      <PageHeader
        title={t('eventTypes.title')}
        subtitle={t('eventTypes.subtitle')}
        actions={
          <button type="button" className="btn-primary btn-sm" onClick={openCreate}>
            <Plus className="h-3.5 w-3.5" />
            {t('eventTypes.new')}
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
                <th className="table-head">{t('eventTypes.version')}</th>
                <th className="table-head">{t('tools.description')}</th>
                <th className="table-head">{t('common.status')}</th>
                <th className="table-head">{t('common.updatedAt')}</th>
                <th className="table-head text-right">{t('common.actions')}</th>
              </THead>
              <TBody>
                {data.items.map((et) => (
                  <TR key={et.id}>
                    <td className="table-cell mono text-ink">{et.name}</td>
                    <td className="table-cell">
                      <Badge tone="brand">v{et.version}</Badge>
                    </td>
                    <td className="table-cell max-w-sm truncate text-xs text-ink-muted">
                      {et.description || '—'}
                    </td>
                    <td className="table-cell">
                      <Badge tone={et.is_active ? 'ok' : 'neutral'} dot>
                        {et.is_active ? t('common.active') : t('common.inactive')}
                      </Badge>
                    </td>
                    <td className="table-cell text-xs text-ink-muted">
                      {formatDateTime(et.updated_at)}
                    </td>
                    <td className="table-cell">
                      <div className="flex justify-end gap-1.5">
                        <button
                          type="button"
                          className="btn-ghost btn-sm"
                          onClick={() => setDetail(et)}
                        >
                          {t('eventTypes.schema')}
                        </button>
                        <button
                          type="button"
                          className="btn-ghost btn-sm"
                          onClick={() => openEdit(et)}
                        >
                          {t('common.edit')}
                        </button>
                        <button
                          type="button"
                          className="btn-danger btn-sm"
                          onClick={() => setDeleteTarget(et)}
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
        title={editing ? `${t('common.edit')}: ${editing.name}@${editing.version}` : t('eventTypes.new')}
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
        <div className="grid gap-x-4 sm:grid-cols-[1fr,120px]">
          <Field label={t('common.name')} hint={t('eventTypes.nameHint')} error={fieldErrors.name}>
            <input
              className="input mono"
              value={name}
              disabled={!!editing}
              onChange={(e) => setName(e.target.value)}
              placeholder="invoice.paid"
            />
          </Field>

          <Field label={t('eventTypes.version')}>
            <input
              type="number"
              min={1}
              className="input"
              value={version}
              disabled={!!editing}
              onChange={(e) => setVersion(Number(e.target.value) || 1)}
            />
          </Field>
        </div>

        <Field label={t('tools.description')}>
          <input
            className="input"
            value={description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </Field>

        <Field
          label={t('eventTypes.schema')}
          hint={t('eventTypes.schemaHint')}
          error={fieldErrors.json_schema}
        >
          <textarea
            className="input mono min-h-[280px]"
            spellCheck={false}
            value={schemaText}
            onChange={(e) => setSchemaText(e.target.value)}
          />
        </Field>
      </Modal>

      <Modal
        open={!!detail}
        onClose={() => setDetail(null)}
        title={detail ? `${detail.name}@${detail.version}` : ''}
        wide
      >
        {detail && <JsonBlock value={detail.json_schema} maxHeight={460} />}
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
