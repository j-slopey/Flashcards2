import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Proxy /api to the Go backend so the frontend never hardcodes a port and
// avoids CORS in dev. Override the target with VITE_API_TARGET if needed.
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': {
        target: process.env.VITE_API_TARGET ?? 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})
