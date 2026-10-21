import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8080',
    },
  },
  build: {
    assetsInlineLimit: 0,
    outDir: '../internal/api/web/dist',
    emptyOutDir: true,
  },
})
