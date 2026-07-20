import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 9002,
    host: true,
    watch: {
      usePolling: process.env.CHOKIDAR_USEPOLLING === 'true',
    },
    proxy: {
      '/portal-api': {
        target: process.env.PORTAL_API_TARGET ?? 'http://localhost:9405',
        rewrite: (path) => path.replace(/^\/portal-api/, ''),
        changeOrigin: true,
      },
    },
  },
})
