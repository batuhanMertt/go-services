import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Languages, LogIn, Moon, Sun } from 'lucide-react'

import { useAuth } from '../lib/auth'
import { useTheme } from '../lib/theme'
import { setLanguage } from '../lib/i18n'
import { ApiError } from '../lib/api'
import { Field, Spinner } from '../components/ui'

export default function Login() {
  const { t, i18n } = useTranslation()
  const { login } = useAuth()
  const { resolved, toggle } = useTheme()

  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    setError(null)
    setBusy(true)

    try {
      await login(email.trim(), password)
    } catch (err) {
      // Kullanıcı numaralandırmasını (user enumeration) önlemek için backend
      // "kullanıcı yok" ile "şifre yanlış" arasında ayrım yapmaz; arayüz de
      // aynı mesajı gösterir.
      setError(err instanceof ApiError && err.status !== 401 ? err.message : t('login.failed'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden bg-surface-base px-4">
      {/* Marka dokusu: yumuşak radyal gradyanlar. Stok şablon görünümünden
          uzaklaşmak için bilinçli bir detay. */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-0 opacity-70"
        style={{
          background:
            'radial-gradient(60rem 40rem at 15% -10%, rgb(var(--brand-600) / 0.16), transparent 60%),' +
            'radial-gradient(45rem 35rem at 95% 110%, rgb(var(--brand-400) / 0.12), transparent 60%)',
        }}
      />

      <div className="absolute right-4 top-4 flex gap-2">
        <button
          type="button"
          onClick={() => setLanguage(i18n.language === 'tr' ? 'en' : 'tr')}
          className="btn-ghost btn-sm"
        >
          <Languages className="h-3.5 w-3.5" />
          {i18n.language === 'tr' ? 'TR' : 'EN'}
        </button>
        <button type="button" onClick={toggle} className="btn-ghost btn-sm" aria-label="theme">
          {resolved === 'dark' ? <Sun className="h-3.5 w-3.5" /> : <Moon className="h-3.5 w-3.5" />}
        </button>
      </div>

      <div className="relative w-full max-w-sm">
        <div className="mb-7 flex flex-col items-center text-center">
          <div className="mb-4 grid h-14 w-14 place-items-center rounded-2xl bg-brand-600 shadow-pop">
            <svg viewBox="0 0 32 32" className="h-8 w-8" aria-hidden>
              <path
                d="M9 16h4m6 0h4M16 9v4m0 6v4"
                stroke="white"
                strokeWidth="2.6"
                strokeLinecap="round"
              />
              <circle cx="16" cy="16" r="2.6" fill="white" />
            </svg>
          </div>
          <h1 className="text-lg font-semibold tracking-tight text-ink">
            {t('common.appName')}
          </h1>
          <p className="mt-1 text-sm text-ink-muted">{t('login.subtitle')}</p>
        </div>

        <form onSubmit={onSubmit} className="card card-pad">
          <Field label={t('login.email')}>
            <input
              type="email"
              className="input"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              autoComplete="username"
              autoFocus
              required
              placeholder="admin@mindmons.local"
            />
          </Field>

          <Field label={t('login.password')}>
            <input
              type="password"
              className="input"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="current-password"
              required
            />
          </Field>

          {error && (
            <div
              role="alert"
              className="mb-4 rounded-lg border border-danger/35 bg-danger/10 px-3 py-2 text-xs text-danger"
            >
              {error}
            </div>
          )}

          <button type="submit" className="btn-primary w-full" disabled={busy}>
            {busy ? <Spinner /> : <LogIn className="h-4 w-4" />}
            {busy ? t('login.submitting') : t('login.submit')}
          </button>
        </form>

        <p className="mt-5 text-center text-[11px] leading-relaxed text-ink-subtle">
          {t('common.appSub')}
        </p>
      </div>
    </div>
  )
}
