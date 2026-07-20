import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 9003,
    host: true,
    watch: {
      usePolling: process.env.CHOKIDAR_USEPOLLING === 'true',
    },
    proxy: {
      '/api/dev': {
        target: process.env.DEV_API_TARGET ?? 'http://localhost:9407',
        rewrite: (p) => p.replace(/^\/api\/dev/, ''),
        changeOrigin: true,
        // SSE: don't buffer — events must hit the browser as they arrive.
        configure: (proxy) => {
          proxy.on('proxyRes', (proxyRes) => {
            if (proxyRes.headers['content-type']?.includes('text/event-stream')) {
              delete proxyRes.headers['content-length']
              proxyRes.headers['cache-control'] = 'no-cache'
            }
          })
        },
      },
      '/portal-api': {
        target: process.env.PORTAL_API_TARGET ?? 'http://localhost:9405',
        rewrite: (p) => p.replace(/^\/portal-api/, ''),
        changeOrigin: true,
      },
      '/dvtp-api': {
        target: process.env.DVTP_API_TARGET ?? 'http://localhost:9406',
        rewrite: (p) => p.replace(/^\/dvtp-api/, ''),
        changeOrigin: true,
      },
      '/eudi-api': {
        target: process.env.EUDI_API_TARGET ?? 'http://localhost:9409',
        rewrite: (p) => p.replace(/^\/eudi-api/, ''),
        changeOrigin: true,
      },
      '/jaeger-api': {
        target: process.env.JAEGER_API_TARGET ?? 'http://localhost:9686',
        rewrite: (p) => p.replace(/^\/jaeger-api/, ''),
        changeOrigin: true,
      },
    },
  },
})
