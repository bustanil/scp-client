import { defineConfig } from '@playwright/test'
import { mkdtempSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const dataDir = mkdtempSync(join(tmpdir(), 'scp-client-browser-data-'))

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  workers: 1,
  metadata: { dataDir },
  globalTeardown: './tests/global-teardown.ts',
  use: { baseURL: 'http://127.0.0.1:8787', viewport: { width: 1440, height: 900 }, trace: 'retain-on-failure' },
  webServer: {
    command: 'go run ./cmd/scp-client',
    cwd: '..',
    url: 'http://127.0.0.1:8787/api/list?kind=local',
    reuseExistingServer: false,
    timeout: 60000,
    env: { SCP_CLIENT_DATA_DIR: dataDir },
  },
})
