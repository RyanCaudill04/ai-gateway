import react from '@vitejs/plugin-react'
import { defineConfig } from 'vite'

// During `npm run dev`, send API calls to a router running locally on :8081
// (in Docker, the proxy service does this routing instead).
const ROUTER_URL = process.env.ROUTER_URL ?? 'http://localhost:8081'

// https://vite.dev/config/
export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/v1': ROUTER_URL,
      '/api': ROUTER_URL,
    },
  },
})
