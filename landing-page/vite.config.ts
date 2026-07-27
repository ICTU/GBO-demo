import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// Static page — no backend of its own, so no dev proxy. Every link it
// renders points at another service and is resolved at runtime from
// window.__GBO_RUNTIME_CONFIG__ (see src/config.ts).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 9000,
    host: true,
    watch: {
      usePolling: process.env.CHOKIDAR_USEPOLLING === 'true',
    },
  },
})
