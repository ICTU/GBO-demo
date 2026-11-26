import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  // Outside node_modules: the AppHost's installers run `npm ci`, which would
  // wipe Vite's dependency pre-bundling on every start.
  cacheDir: '.vite',
  plugins: [react()],
  server: {
    port: 9000,
    // Fail instead of moving to the next free port: the other apps link here.
    strictPort: true,
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
