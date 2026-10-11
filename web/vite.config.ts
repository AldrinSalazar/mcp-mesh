import { writeFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { configDefaults, defineConfig } from 'vitest/config'
import type { Plugin } from 'vite'
import vue from '@vitejs/plugin-vue'
import { mockApi } from './mock/plugin.ts'

// The Go package embeds dist/, which must exist even before the first build,
// so the repository keeps dist/.gitkeep. Vite empties dist/ on every build;
// put the placeholder back afterwards.
function keepPlaceholder(): Plugin {
  return {
    name: 'keep-dist-placeholder',
    apply: 'build',
    closeBundle() {
      writeFileSync(resolve(import.meta.dirname, 'dist/.gitkeep'), '')
    },
  }
}

// MCPMESH_HUB points the dev server at a running hub's admin listener.
// MOCK=1 (or empty / authoff) serves fixture data instead (mock/plugin.ts).
const hub = process.env.MCPMESH_HUB ?? 'http://127.0.0.1:7000'
const mock = process.env.MOCK

export default defineConfig({
  plugins: [vue(), keepPlaceholder(), ...(mock ? [mockApi(mock)] : [])],
  resolve: { alias: { '@': resolve(import.meta.dirname, 'src') } },
  server: {
    proxy: mock ? {} : {
      // changeOrigin rewrites Host to the hub's, and the hub rejects
      // state-changing requests whose Origin differs from Host, so Origin is
      // rewritten to match.
      '/api': {
        target: hub,
        changeOrigin: true,
        configure(proxy) {
          proxy.on('proxyReq', (req) => {
            if (req.getHeader('origin')) req.setHeader('origin', hub)
          })
        },
      },
    },
  },
  build: { outDir: 'dist', emptyOutDir: true, assetsDir: 'assets' },
  // e2e/ holds the Playwright browser tests (npm run test:e2e).
  test: { environment: 'jsdom', exclude: [...configDefaults.exclude, 'e2e/**'] },
})
