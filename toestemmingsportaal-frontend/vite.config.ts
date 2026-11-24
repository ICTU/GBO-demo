import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

// No other site may frame the portal: its consent screen grants with one
// click, so a framed page could trick a citizen into it (clickjacking).
// nginx.conf sends the same headers for the built portal.
export const FRAME_HEADERS = {
  'Content-Security-Policy': "frame-ancestors 'none'",
  'X-Frame-Options': 'DENY',
}

export default defineConfig({
  plugins: [react()],
  preview: {
    headers: FRAME_HEADERS,
  },
  server: {
    port: 9002,
    host: true,
    headers: FRAME_HEADERS,
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
