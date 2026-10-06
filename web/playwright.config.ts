import { defineConfig } from '@playwright/test'

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1,
  use: { baseURL: 'http://127.0.0.1:8787', viewport: { width: 1440, height: 900 }, trace: 'retain-on-failure' },
  webServer: {
    command: 'go run ./cmd/scp-client',
    cwd: '..',
    url: 'http://127.0.0.1:8787/api/list?kind=local',
    reuseExistingServer: false,
    timeout: 60000,
  },
})
