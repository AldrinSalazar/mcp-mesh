// Browser tests of the dashboard. The "mock" project (e2e/*.spec.ts) runs
// against the mock API (mock/plugin.ts) served by Vite: npm run test:e2e.
// The "hub" project (e2e/hub/) runs against a real hub, given by the VM
// end-to-end tests (e2e/dashboard_test.go): MCPMESH_E2E_HUB_URL, the admin
// password and the lab relay as a SOCKS5 proxy. Tests share one state, so
// they run in order in one worker.
import { defineConfig, devices } from '@playwright/test'

const port = 5179
const hubURL = process.env.MCPMESH_E2E_HUB_URL
const proxy = process.env.MCPMESH_E2E_PROXY

export default defineConfig({
  workers: 1,
  outputDir: '../e2e-results/playwright',
  reporter: [['list'], ['html', { outputFolder: '../e2e-results/playwright-report', open: 'never' }]],
  use: { trace: 'retain-on-failure', screenshot: 'only-on-failure' },
  projects: hubURL
    ? [{
        name: 'hub',
        testDir: './e2e/hub',
        use: { ...devices['Desktop Chrome'], baseURL: hubURL, ...(proxy ? { proxy: { server: proxy } } : {}) },
      }]
    : [{
        name: 'mock',
        testDir: './e2e',
        testIgnore: 'hub/**',
        use: { ...devices['Desktop Chrome'], baseURL: `http://127.0.0.1:${port}` },
      }],
  webServer: hubURL ? undefined : {
    command: `npx vite --port ${port} --strictPort --host 127.0.0.1`,
    env: { MOCK: '1' },
    url: `http://127.0.0.1:${port}`,
    reuseExistingServer: false,
    timeout: 60_000,
  },
})
