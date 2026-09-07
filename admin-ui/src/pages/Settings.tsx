import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, Monitor, Moon, Sun } from 'lucide-react'

import { api } from '../lib/api'
import { useAuth } from '../lib/auth'
import { useTheme } from '../lib/theme'
import type { ThemeChoice } from '../lib/theme'
import { setLanguage } from '../lib/i18n'
import { formatDateTime } from '../lib/format'
import { Card, Field, PageHeader, useToast } from '../components/ui'

export default function Settings() {
  const { t, i18n } = useTranslation()
  const { user, logout } = useAuth()
  const { choice, setChoice } = useTheme()
  const toast = useToast()

  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function onChangePassword(e: React.FormEvent) {
    e.preventDefault()
    setError(null)

    if (next !== confirm) {
      setError(t('settings.passwordMismatch'))
      return
    }

    setBusy(true)
    try {
      await api.changePassword(current, next)
      toast.push(t('settings.passwordChanged'), 'ok')
      // Şifre değişince backend tüm oturumları iptal eder; kullanıcıyı
      // yeniden giriş ekranına almak tutarlı davranış.
      setTimeout(() => void logout(), 1200)
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const themes: { value: ThemeChoice; label: string; icon: typeof Sun }[] = [
    { value: 'light', label: t('settings.themeLight'), icon: Sun },
    { value: 'dark', label: t('settings.themeDark'), icon: Moon },
    { value: 'system', label: t('settings.themeSystem'), icon: Monitor },
  ]

  return (
    <>
      <PageHeader title={t('settings.title')} subtitle={t('settings.subtitle')} />

      <div className="grid max-w-4xl gap-5 lg:grid-cols-2">
        <Card title={t('settings.profile')}>
          <dl className="divide-y divide-edge">
            <div className="flex items-center justify-between px-5 py-3 text-sm">
              <dt className="text-ink-muted">{t('login.email')}</dt>
              <dd className="mono text-ink">{user?.email}</dd>
            </div>
            <div className="flex items-center justify-between px-5 py-3 text-sm">
              <dt className="text-ink-muted">{t('settings.role')}</dt>
              <dd className="text-ink">{user?.role}</dd>
            </div>
            <div className="flex items-center justify-between px-5 py-3 text-sm">
              <dt className="text-ink-muted">{t('settings.lastLogin')}</dt>
              <dd className="text-ink">{formatDateTime(user?.last_login_at)}</dd>
            </div>
          </dl>
        </Card>

        <Card title={t('settings.appearance')}>
          <div className="card-pad">
            <Field label={t('settings.theme')}>
              <div className="grid grid-cols-3 gap-2">
                {themes.map(({ value, label, icon: Icon }) => (
                  <button
                    key={value}
                    type="button"
                    onClick={() => setChoice(value)}
                    className={`flex flex-col items-center gap-1.5 rounded-lg border px-3 py-3 text-xs transition-colors ${
                      choice === value
                        ? 'border-brand-500 bg-brand-500/10 text-brand-400'
                        : 'border-edge bg-surface-sunken text-ink-muted hover:text-ink'
                    }`}
                  >
                    <Icon className="h-4 w-4" />
                    {label}
                  </button>
                ))}
              </div>
            </Field>

            <Field label={t('settings.language')}>
              <div className="grid grid-cols-2 gap-2">
                {(['tr', 'en'] as const).map((lang) => (
                  <button
                    key={lang}
                    type="button"
                    onClick={() => setLanguage(lang)}
                    className={`flex items-center justify-center gap-1.5 rounded-lg border px-3 py-2.5 text-sm transition-colors ${
                      i18n.language === lang
                        ? 'border-brand-500 bg-brand-500/10 text-brand-400'
                        : 'border-edge bg-surface-sunken text-ink-muted hover:text-ink'
                    }`}
                  >
                    {i18n.language === lang && <Check className="h-3.5 w-3.5" />}
                    {lang === 'tr' ? 'Türkçe' : 'English'}
                  </button>
                ))}
              </div>
            </Field>
          </div>
        </Card>

        <Card title={t('settings.changePassword')} className="lg:col-span-2">
          <form onSubmit={onChangePassword} className="card-pad max-w-md">
            <Field label={t('settings.currentPassword')}>
              <input
                type="password"
                className="input"
                value={current}
                onChange={(e) => setCurrent(e.target.value)}
                autoComplete="current-password"
                required
              />
            </Field>

            <Field label={t('settings.newPassword')}>
              <input
                type="password"
                className="input"
                value={next}
                onChange={(e) => setNext(e.target.value)}
                autoComplete="new-password"
                minLength={8}
                required
              />
            </Field>

            <Field label={t('settings.confirmPassword')} error={error ?? undefined}>
              <input
                type="password"
                className="input"
                value={confirm}
                onChange={(e) => setConfirm(e.target.value)}
                autoComplete="new-password"
                minLength={8}
                required
              />
            </Field>

            <button type="submit" className="btn-primary" disabled={busy}>
              {t('settings.changePassword')}
            </button>
          </form>
        </Card>
      </div>
    </>
  )
}
