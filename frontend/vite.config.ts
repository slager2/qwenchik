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
    rollupOptions: {
      output: {
        // Stable vendor chunks: React and the markdown pipeline change far less
        // often than application code and stay cached across releases.
        manualChunks(id) {
          if (!id.includes('node_modules')) return undefined
          if (/node_modules\/(react|react-dom|scheduler)\//.test(id)) return 'react'
          if (/node_modules\/(react-markdown|remark-[^/]+|rehype-[^/]+|micromark[^/]*|mdast-[^/]+|hast-[^/]+|unist-[^/]+|unified|vfile[^/]*|property-information|space-separated-tokens|comma-separated-tokens|decode-named-character-reference|character-entities[^/]*|trim-lines|bail|is-plain-obj|trough|devlop|zwitch|longest-streak|markdown-table|ccount|escape-string-regexp|html-url-attributes|style-to-[^/]+|inline-style-parser|estree-util-[^/]+|@ungap)\//.test(id)) return 'markdown'
          return 'vendor'
        },
      },
    },
  },
})
