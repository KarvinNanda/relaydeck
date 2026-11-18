import { defineConfig } from 'vitest/config'
import vue from '@vitejs/plugin-vue'

export default defineConfig({
  plugins: [vue()],
  build: {
    // The Go binary embeds this directory (internal/webui). emptyOutDir
    // removes the committed .gitkeep, and public/.gitkeep puts it back,
    // because Vite copies public/ after it empties the directory.
    outDir: '../internal/webui/dist',
    emptyOutDir: true,
    // Never inline assets as data: URIs. The CSP allows fonts only from
    // 'self', so an inlined font would be blocked.
    assetsInlineLimit: 0,
  },
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
  },
})
