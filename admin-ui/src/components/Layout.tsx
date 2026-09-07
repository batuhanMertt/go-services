import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import {
  Activity,
  BookOpen,
  Boxes,
  FileClock,
  Gauge,
  Languages,
  LogOut,
  Menu,
  Moon,
  Send,
  ServerCrash,
  Settings as SettingsIcon,
  Share2,
  Sun,
  X,
} from 'lucide-react'

import { useAuth } from '../lib/auth'
import { useTheme } from '../lib/theme'
import { setLanguage } from '../lib/i18n'

interface NavItem {
  to: string
  labelKey: string
  icon: typeof Gauge
}

const controlPlane: NavItem[] = [
  { to: '/', labelKey: 'nav.dashboard', icon: Gauge },
  { to: '/tools', labelKey: 'nav.tools', icon: Boxes },
  { to: '/event-types', labelKey: 'nav.eventTypes', icon: BookOpen },
  { to: '/subscriptions', labelKey: 'nav.subscriptions', icon: Share2 },
  { to: '/routing-rules', labelKey: 'nav.routingRules', icon: Activity },
]

const operations: NavItem[] = [
  { to: '/events', labelKey: 'nav.events', icon: Send },
  { to: '/deliveries', labelKey: 'nav.deliveries', icon: FileClock },
  { to: '/dlq', labelKey: 'nav.dlq', icon: ServerCrash },
  { to: '/audit', labelKey: 'nav.audit', icon: FileClock },
  { to: '/settings', labelKey: 'nav.settings', icon: SettingsIcon },
]

function BrandMark() {
  return (
    <div className="flex items-center gap-2.5">
      <div className="grid h-9 w-9 place-items-center rounded-xl bg-brand-600 shadow-sm">
        {/* Marka işareti: merkezden dört yöne dağılan bağlantılar —
            platformun "orkestra şefi" rolünü anlatır. */}
        <svg viewBox="0 0 32 32" className="h-5 w-5" aria-hidden>
          <path
            d="M9 16h4m6 0h4M16 9v4m0 6v4"
            stroke="white"
            strokeWidth="2.6"
            strokeLinecap="round"
          />
          <circle cx="16" cy="16" r="2.6" fill="white" />
        </svg>
      </div>
      <div className="leading-tight">
        <p className="text-sm font-semibold tracking-tight text-ink">Integration Hub</p>
        <p className="text-[10.5px] uppercase tracking-[0.14em] text-ink-subtle">MindMons</p>
      </div>
    </div>
  )
}

function NavSection({ titleKey, items, onNavigate }: { titleKey: string; items: NavItem[]; onNavigate: () => void }) {
  const { t } = useTranslation()

  return (
    <div className="mb-6">
      <p className="mb-2 px-3 text-[10.5px] font-semibold uppercase tracking-[0.14em] text-ink-subtle">
        {t(titleKey)}
      </p>
      <nav className="space-y-0.5">
        {items.map((item) => {
          const Icon = item.icon
          return (
            <NavLink
              key={item.to}
              to={item.to}
              end={item.to === '/'}
              onClick={onNavigate}
              className={({ isActive }) =>
                `flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm transition-colors ${
                  isActive
                    ? 'bg-brand-600/15 font-medium text-brand-400'
                    : 'text-ink-muted hover:bg-surface-sunken hover:text-ink'
                }`
              }
            >
              <Icon className="h-4 w-4 shrink-0" />
              {t(item.labelKey)}
            </NavLink>
          )
        })}
      </nav>
    </div>
  )
}

export default function Layout() {
  const { t, i18n } = useTranslation()
  const { user, logout } = useAuth()
  const { resolved, toggle } = useTheme()
  const location = useLocation()
  const [mobileOpen, setMobileOpen] = useState(false)

  // Sayfa değişince mobil menüyü kapat.
  useEffect(() => setMobileOpen(false), [location.pathname])

  const sidebar = (
    <>
      <div className="px-3 py-5">
        <BrandMark />
      </div>

      <div className="flex-1 overflow-y-auto px-3">
        <NavSection titleKey="nav.groupControl" items={controlPlane} onNavigate={() => setMobileOpen(false)} />
        <NavSection titleKey="nav.groupOps" items={operations} onNavigate={() => setMobileOpen(false)} />
      </div>

      <div className="border-t border-edge px-3 py-3">
        <div className="mb-2 px-2">
          <p className="truncate text-xs font-medium text-ink">{user?.email}</p>
          <p className="text-[11px] uppercase tracking-wider text-ink-subtle">{user?.role}</p>
        </div>
        <button
          type="button"
          onClick={() => void logout()}
          className="flex w-full items-center gap-2.5 rounded-lg px-3 py-2 text-sm text-ink-muted transition-colors hover:bg-danger/10 hover:text-danger"
        >
          <LogOut className="h-4 w-4" />
          {t('nav.logout')}
        </button>
      </div>
    </>
  )

  return (
    <div className="flex min-h-screen bg-surface-base">
      {/* Masaüstü kenar çubuğu */}
      <aside className="sticky top-0 hidden h-screen w-60 shrink-0 flex-col border-r border-edge bg-surface-raised lg:flex">
        {sidebar}
      </aside>

      {/* Mobil çekmece */}
      {mobileOpen && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="absolute inset-0 bg-black/55" onClick={() => setMobileOpen(false)} />
          <aside className="absolute left-0 top-0 flex h-full w-64 flex-col border-r border-edge bg-surface-raised">
            {sidebar}
          </aside>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-30 flex items-center gap-3 border-b border-edge bg-surface-raised/85 px-4 py-3 backdrop-blur sm:px-6">
          <button
            type="button"
            className="btn-ghost btn-sm lg:hidden"
            onClick={() => setMobileOpen((v) => !v)}
            aria-label="menu"
          >
            {mobileOpen ? <X className="h-4 w-4" /> : <Menu className="h-4 w-4" />}
          </button>

          <div className="flex-1" />

          <button
            type="button"
            onClick={() => setLanguage(i18n.language === 'tr' ? 'en' : 'tr')}
            className="btn-ghost btn-sm"
            title={t('settings.language')}
          >
            <Languages className="h-3.5 w-3.5" />
            {i18n.language === 'tr' ? 'TR' : 'EN'}
          </button>

          <button
            type="button"
            onClick={toggle}
            className="btn-ghost btn-sm"
            title={t('settings.theme')}
            aria-label={t('settings.theme')}
          >
            {resolved === 'dark' ? <Sun className="h-3.5 w-3.5" /> : <Moon className="h-3.5 w-3.5" />}
          </button>
        </header>

        <main className="min-w-0 flex-1 px-4 py-6 sm:px-6 lg:px-8">
          <Outlet />
        </main>
      </div>
    </div>
  )
}
