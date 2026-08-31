import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    port: 9000,
    host: true,
    watch: {
      usePolling: process.env.CHOKIDAR_USEPOLLING === 'true',
    },
    proxy: {
      '/eudi-offers.json': {
        target: process.env.EUDI_API_TARGET ?? 'http://localhost:9409',
        changeOrigin: true,
      },
    },
  },
})
