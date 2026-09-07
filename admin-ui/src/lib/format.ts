import i18n from './i18n'

/** Tarihi kullanıcının diline göre biçimlendirir. */
export function formatDateTime(value?: string | null): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return '—'

  return new Intl.DateTimeFormat(i18n.language === 'en' ? 'en-GB' : 'tr-TR', {
    dateStyle: 'short',
    timeStyle: 'medium',
  }).format(d)
}

export function formatTime(value?: string | null): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return '—'

  return new Intl.DateTimeFormat(i18n.language === 'en' ? 'en-GB' : 'tr-TR', {
    timeStyle: 'medium',
  }).format(d)
}

/** "3 dk önce" gibi göreli zaman. */
export function formatRelative(value?: string | null): string {
  if (!value) return '—'
  const d = new Date(value)
  if (Number.isNaN(d.getTime())) return '—'

  const diffSec = Math.round((d.getTime() - Date.now()) / 1000)
  const rtf = new Intl.RelativeTimeFormat(i18n.language === 'en' ? 'en' : 'tr', {
    numeric: 'auto',
  })

  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ['year', 31536000],
    ['month', 2592000],
    ['day', 86400],
    ['hour', 3600],
    ['minute', 60],
    ['second', 1],
  ]

  for (const [unit, seconds] of units) {
    if (Math.abs(diffSec) >= seconds || unit === 'second') {
      return rtf.format(Math.round(diffSec / seconds), unit)
    }
  }
  return '—'
}

export function formatNumber(n: number): string {
  return new Intl.NumberFormat(i18n.language === 'en' ? 'en-GB' : 'tr-TR').format(n)
}

export function formatPercent(ratio: number): string {
  return new Intl.NumberFormat(i18n.language === 'en' ? 'en-GB' : 'tr-TR', {
    style: 'percent',
    maximumFractionDigits: 1,
  }).format(Number.isFinite(ratio) ? ratio : 0)
}

export function formatMs(ms: number): string {
  if (ms < 1000) return `${ms} ms`
  return `${(ms / 1000).toFixed(2)} s`
}

/** Uzun kimlikleri kısaltır: "3f9a1c72…" */
export function shortId(id?: string | null, len = 8): string {
  if (!id) return '—'
  return id.length <= len ? id : `${id.slice(0, len)}…`
}

/** JSON'u okunabilir biçimde yazdırır. */
export function prettyJson(value: unknown): string {
  if (value === null || value === undefined) return '—'
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

export async function copyToClipboard(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}
