import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import App from './App'
import { AuthProvider } from './lib/auth'
import { ThemeProvider } from './lib/theme'
import { ToastProvider } from './components/ui'
import './lib/i18n'
import './index.css'

// TanStack Query yapılandırması.
//
// staleTime = 10 sn: aynı veriyi 10 saniye içinde iki kez isteyen iki bileşen
// tek istek yapar. Operasyonel sayfalarda (event akışı, DLQ) bu değer
// sayfa bazında düşürülür.
//
// retry = 1: ağ dalgalanmalarında bir kez dene, ama 4xx hatalarında ısrar etme
// (aşağıdaki koşul 4xx'i eler — yanlış istek tekrarlanmaz).
const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      refetchOnWindowFocus: false,
      retry: (failureCount, error) => {
        const status = (error as { status?: number }).status
        if (status && status >= 400 && status < 500) return false
        return failureCount < 1
      },
    },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <ToastProvider>
          <AuthProvider>
            <BrowserRouter>
              <App />
            </BrowserRouter>
          </AuthProvider>
        </ToastProvider>
      </ThemeProvider>
    </QueryClientProvider>
  </StrictMode>,
)
