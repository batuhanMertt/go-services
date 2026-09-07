import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
} from 'react'
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import {
  AlertTriangle,
  Check,
  ChevronLeft,
  ChevronRight,
  Copy,
  Inbox,
  Loader2,
  X,
} from 'lucide-react'

import { copyToClipboard, prettyJson } from '../lib/format'

/* ---------------------------------------------------------------- Sayfa başlığı */

export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string
  subtitle?: string
  actions?: ReactNode
}) {
  return (
    <div className="mb-6 flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 className="text-xl font-semibold tracking-tight text-ink">{title}</h1>
        {subtitle && <p className="mt-1 text-sm text-ink-muted">{subtitle}</p>}
      </div>
      {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
    </div>
  )
}

/* ---------------------------------------------------------------------- Kartlar */

export function Card({
  children,
  className = '',
  title,
  actions,
}: {
  children: ReactNode
  className?: string
  title?: string
  actions?: ReactNode
}) {
  return (
    <section className={`card ${className}`}>
      {(title || actions) && (
        <header className="flex items-center justify-between gap-3 border-b border-edge px-5 py-3.5">
          {title && (
            <h2 className="text-xs font-semibold uppercase tracking-wider text-ink-muted">
              {title}
            </h2>
          )}
          {actions}
        </header>
      )}
      {children}
    </section>
  )
}

export function StatCard({
  label,
  value,
  hint,
  tone = 'neutral',
  icon,
}: {
  label: string
  value: string | number
  hint?: string
  tone?: 'neutral' | 'ok' | 'warn' | 'danger' | 'brand'
  icon?: ReactNode
}) {
  const toneClass = {
    neutral: 'text-ink',
    ok: 'text-ok',
    warn: 'text-warn',
    danger: 'text-danger',
    brand: 'text-brand-400',
  }[tone]

  return (
    <div className="card card-pad animate-fade-in">
      <div className="flex items-start justify-between gap-3">
        <p className="text-xs font-medium uppercase tracking-wider text-ink-muted">{label}</p>
        {icon && <span className="text-ink-subtle">{icon}</span>}
      </div>
      <p className={`mt-2 text-2xl font-semibold tabular-nums tracking-tight ${toneClass}`}>
        {value}
      </p>
      {hint && <p className="mt-1 text-xs text-ink-subtle">{hint}</p>}
    </div>
  )
}

/* ---------------------------------------------------------------------- Rozetler */

type Tone = 'neutral' | 'ok' | 'warn' | 'danger' | 'info' | 'brand'

const toneStyles: Record<Tone, string> = {
  neutral: 'border-edge bg-surface-sunken text-ink-muted',
  ok: 'border-ok/35 bg-ok/10 text-ok',
  warn: 'border-warn/35 bg-warn/10 text-warn',
  danger: 'border-danger/35 bg-danger/10 text-danger',
  info: 'border-info/35 bg-info/10 text-info',
  brand: 'border-brand-500/35 bg-brand-500/10 text-brand-400',
}

export function Badge({
  children,
  tone = 'neutral',
  dot = false,
}: {
  children: ReactNode
  tone?: Tone
  dot?: boolean
}) {
  return (
    <span className={`chip ${toneStyles[tone]}`}>
      {dot && <span className="h-1.5 w-1.5 rounded-full bg-current" />}
      {children}
    </span>
  )
}

/** HTTP durum koduna göre renklendirilmiş rozet. */
export function StatusBadge({ status }: { status?: number | null }) {
  if (status === null || status === undefined) return <Badge tone="danger">ERR</Badge>

  const tone: Tone =
    status < 300 ? 'ok' : status < 400 ? 'info' : status < 500 ? 'warn' : 'danger'
  return <Badge tone={tone}>{status}</Badge>
}

/* ------------------------------------------------------------------ Yükleniyor */

export function Spinner({ className = '' }: { className?: string }) {
  return <Loader2 className={`h-4 w-4 animate-spin ${className}`} aria-hidden />
}

export function LoadingBlock({ label }: { label?: string }) {
  const { t } = useTranslation()
  return (
    <div className="flex items-center justify-center gap-2 px-5 py-14 text-sm text-ink-muted">
      <Spinner />
      {label ?? t('common.loading')}
    </div>
  )
}

export function EmptyState({ message, icon }: { message?: string; icon?: ReactNode }) {
  const { t } = useTranslation()
  return (
    <div className="flex flex-col items-center justify-center gap-3 px-5 py-16 text-center">
      <span className="text-ink-subtle">{icon ?? <Inbox className="h-7 w-7" />}</span>
      <p className="max-w-sm text-sm text-ink-muted">{message ?? t('common.empty')}</p>
    </div>
  )
}

export function ErrorBlock({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const { t } = useTranslation()
  const message = error instanceof Error ? error.message : String(error)

  return (
    <div className="flex flex-col items-center gap-3 px-5 py-12 text-center">
      <AlertTriangle className="h-7 w-7 text-danger" />
      <p className="text-sm text-ink">{t('common.error')}</p>
      <p className="max-w-lg text-xs text-ink-muted">{message}</p>
      {onRetry && (
        <button type="button" className="btn-ghost btn-sm" onClick={onRetry}>
          {t('common.retry')}
        </button>
      )}
    </div>
  )
}

/* --------------------------------------------------------------------- Tablolar */

export function Table({ children }: { children: ReactNode }) {
  return (
    <div className="overflow-x-auto">
      <table className="w-full border-collapse">{children}</table>
    </div>
  )
}

export function THead({ children }: { children: ReactNode }) {
  return (
    <thead className="border-b border-edge bg-surface-sunken/60">
      <tr>{children}</tr>
    </thead>
  )
}

export function TBody({ children }: { children: ReactNode }) {
  return <tbody className="divide-y divide-edge">{children}</tbody>
}

export function TR({ children, onClick }: { children: ReactNode; onClick?: () => void }) {
  return (
    <tr
      className={onClick ? 'cursor-pointer transition-colors hover:bg-surface-sunken/60' : ''}
      onClick={onClick}
    >
      {children}
    </tr>
  )
}

/* ------------------------------------------------------------------- Sayfalama */

export function Pagination({
  total,
  limit,
  offset,
  onChange,
}: {
  total: number
  limit: number
  offset: number
  onChange: (offset: number) => void
}) {
  const { t } = useTranslation()
  const page = Math.floor(offset / limit) + 1
  const pages = Math.max(1, Math.ceil(total / limit))

  if (total <= limit) return null

  return (
    <div className="flex items-center justify-between gap-3 border-t border-edge px-5 py-3">
      <p className="text-xs text-ink-muted">
        {t('common.total')}: <span className="tabular-nums">{total}</span>
      </p>
      <div className="flex items-center gap-2">
        <button
          type="button"
          className="btn-ghost btn-sm"
          disabled={page <= 1}
          onClick={() => onChange(Math.max(0, offset - limit))}
        >
          <ChevronLeft className="h-3.5 w-3.5" />
          {t('common.prev')}
        </button>
        <span className="text-xs tabular-nums text-ink-muted">
          {page} / {pages}
        </span>
        <button
          type="button"
          className="btn-ghost btn-sm"
          disabled={page >= pages}
          onClick={() => onChange(offset + limit)}
        >
          {t('common.next')}
          <ChevronRight className="h-3.5 w-3.5" />
        </button>
      </div>
    </div>
  )
}

/* ---------------------------------------------------------------------- Formlar */

export function Field({
  label,
  hint,
  error,
  children,
}: {
  label: string
  hint?: string
  error?: string
  children: ReactNode
}) {
  return (
    <div className="mb-4">
      <label className="label">{label}</label>
      {children}
      {error ? (
        <p className="mt-1 text-xs text-danger">{error}</p>
      ) : (
        hint && <p className="mt-1 text-xs text-ink-subtle">{hint}</p>
      )}
    </div>
  )
}

/* ----------------------------------------------------------------------- Modal */

export function Modal({
  open,
  onClose,
  title,
  children,
  footer,
  wide = false,
}: {
  open: boolean
  onClose: () => void
  title: string
  children: ReactNode
  footer?: ReactNode
  wide?: boolean
}) {
  const { t } = useTranslation()
  const titleId = useId()
  const ref = useRef<HTMLDivElement>(null)

  // Escape ile kapatma + arka planın kaydırılmasını engelleme.
  useEffect(() => {
    if (!open) return

    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', onKey)

    const prevOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'
    ref.current?.focus()

    return () => {
      document.removeEventListener('keydown', onKey)
      document.body.style.overflow = prevOverflow
    }
  }, [open, onClose])

  if (!open) return null

  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center overflow-y-auto bg-black/55 p-4 backdrop-blur-sm sm:p-8"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        ref={ref}
        tabIndex={-1}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className={`animate-fade-in my-auto w-full rounded-card border border-edge bg-surface-overlay shadow-pop outline-none ${
          wide ? 'max-w-3xl' : 'max-w-lg'
        }`}
      >
        <header className="flex items-center justify-between gap-3 border-b border-edge px-5 py-3.5">
          <h2 id={titleId} className="text-sm font-semibold text-ink">
            {title}
          </h2>
          <button
            type="button"
            onClick={onClose}
            aria-label={t('common.close')}
            className="rounded-md p-1 text-ink-muted transition-colors hover:bg-surface-sunken hover:text-ink"
          >
            <X className="h-4 w-4" />
          </button>
        </header>

        <div className="max-h-[65vh] overflow-y-auto px-5 py-4">{children}</div>

        {footer && (
          <footer className="flex items-center justify-end gap-2 border-t border-edge px-5 py-3.5">
            {footer}
          </footer>
        )}
      </div>
    </div>
  )
}

/* ----------------------------------------------------------------- JSON gösterim */

export function JsonBlock({ value, maxHeight = 320 }: { value: unknown; maxHeight?: number }) {
  const text = useMemo(() => prettyJson(value), [value])

  return (
    <div className="relative">
      <pre
        className="mono overflow-auto rounded-lg border border-edge bg-surface-sunken p-3 leading-relaxed text-ink-muted"
        style={{ maxHeight }}
      >
        {text}
      </pre>
      <div className="absolute right-2 top-2">
        <CopyButton text={text} />
      </div>
    </div>
  )
}

export function CopyButton({ text, label }: { text: string; label?: string }) {
  const { t } = useTranslation()
  const [copied, setCopied] = useState(false)

  const onCopy = useCallback(async () => {
    if (await copyToClipboard(text)) {
      setCopied(true)
      setTimeout(() => setCopied(false), 1600)
    }
  }, [text])

  return (
    <button
      type="button"
      onClick={onCopy}
      className="btn-ghost btn-sm"
      title={copied ? t('common.copied') : t('common.copy')}
    >
      {copied ? <Check className="h-3.5 w-3.5 text-ok" /> : <Copy className="h-3.5 w-3.5" />}
      {label ?? (copied ? t('common.copied') : t('common.copy'))}
    </button>
  )
}

/* ------------------------------------------------------------------ Bildirimler */

interface Toast {
  id: number
  message: string
  tone: 'ok' | 'danger' | 'info'
}

interface ToastContextValue {
  push: (message: string, tone?: Toast['tone']) => void
}

const ToastContext = createContext<ToastContextValue | null>(null)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const counter = useRef(0)

  const push = useCallback((message: string, tone: Toast['tone'] = 'info') => {
    const id = ++counter.current
    setToasts((prev) => [...prev, { id, message, tone }])
    setTimeout(() => setToasts((prev) => prev.filter((x) => x.id !== id)), 4500)
  }, [])

  const value = useMemo(() => ({ push }), [push])

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div
        className="pointer-events-none fixed bottom-5 right-5 z-[60] flex w-full max-w-sm flex-col gap-2"
        role="status"
        aria-live="polite"
      >
        {toasts.map((toast) => (
          <div
            key={toast.id}
            className={`animate-fade-in pointer-events-auto rounded-lg border px-4 py-3 text-sm shadow-pop backdrop-blur ${
              toast.tone === 'ok'
                ? 'border-ok/40 bg-ok/15 text-ok'
                : toast.tone === 'danger'
                  ? 'border-danger/40 bg-danger/15 text-danger'
                  : 'border-edge bg-surface-overlay text-ink'
            }`}
          >
            {toast.message}
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  )
}

export function useToast(): ToastContextValue {
  const ctx = useContext(ToastContext)
  if (!ctx) throw new Error('useToast, ToastProvider içinde kullanılmalı')
  return ctx
}

/* --------------------------------------------------------------- Onay diyaloğu */

export function ConfirmDialog({
  open,
  title,
  message,
  onConfirm,
  onCancel,
  danger = true,
}: {
  open: boolean
  title: string
  message: string
  onConfirm: () => void
  onCancel: () => void
  danger?: boolean
}) {
  const { t } = useTranslation()

  return (
    <Modal
      open={open}
      onClose={onCancel}
      title={title}
      footer={
        <>
          <button type="button" className="btn-ghost" onClick={onCancel}>
            {t('common.cancel')}
          </button>
          <button
            type="button"
            className={danger ? 'btn-danger' : 'btn-primary'}
            onClick={onConfirm}
          >
            {t('common.confirm')}
          </button>
        </>
      }
    >
      <p className="text-sm text-ink-muted">{message}</p>
    </Modal>
  )
}
