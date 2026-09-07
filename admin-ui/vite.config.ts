import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Vite seçimi (docs/03-tech-stack.md): esbuild tabanlı dev sunucusu, saniyenin
// altında soğuk başlangıç ve Rollup tabanlı üretim build'i. Next.js'in SSR
// yetenekleri bu iç yönetim paneli için gereksiz ağırlık olurdu.
export default defineConfig({
  plugins: [react()],

  server: {
    host: '0.0.0.0',
    port: 5173,
    // Yerel geliştirmede (npm run dev) API'yi container'daki admin-api'ye vekil et.
    proxy: {
      '/api': {
        target: process.env.VITE_DEV_API_TARGET ?? 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },

  build: {
    outDir: 'dist',
    sourcemap: false,
    // Büyük kütüphaneleri ayrı chunk'lara ayır: ilk yükleme küçülür ve
    // uygulama kodu değiştiğinde vendor chunk'ı tarayıcı önbelleğinde kalır.
    rollupOptions: {
      output: {
        manualChunks: {
          react: ['react', 'react-dom', 'react-router-dom'],
          charts: ['recharts'],
          query: ['@tanstack/react-query'],
        },
      },
    },
    chunkSizeWarningLimit: 900,
  },
})
