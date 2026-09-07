import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'

import { api, tokens } from './api'
import type { AdminUser } from './types'

interface AuthContextValue {
  user: AdminUser | null
  ready: boolean
  login: (email: string, password: string) => Promise<void>
  logout: () => Promise<void>
  refreshUser: () => Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

/**
 * AuthProvider — oturum durumu.
 *
 * Sayfa yenilendiğinde access token bellekte olmadığı için önce refresh
 * token ile sessizce yenileme denenir (`/auth/me` çağrısı 401 alırsa
 * api katmanı otomatik refresh yapar). Başarılıysa oturum sürer,
 * değilse login ekranına düşülür.
 */
export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<AdminUser | null>(null)
  const [ready, setReady] = useState(false)

  const refreshUser = useCallback(async () => {
    try {
      const me = await api.me()
      setUser(me)
    } catch {
      setUser(null)
    }
  }, [])

  useEffect(() => {
    tokens.onLogout(() => setUser(null))

    void (async () => {
      if (tokens.getRefresh()) {
        await refreshUser()
      }
      setReady(true)
    })()
  }, [refreshUser])

  const login = useCallback(async (email: string, password: string) => {
    const res = await api.login(email, password)
    tokens.setAccess(res.access_token)
    tokens.setRefresh(res.refresh_token)
    setUser(res.user)
  }, [])

  const logout = useCallback(async () => {
    try {
      await api.logout()
    } catch {
      /* sunucuya ulaşılamasa da yerel oturum kapatılır */
    }
    tokens.clear()
    setUser(null)
  }, [])

  const value = useMemo(
    () => ({ user, ready, login, logout, refreshUser }),
    [user, ready, login, logout, refreshUser],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth, AuthProvider içinde kullanılmalı')
  return ctx
}
