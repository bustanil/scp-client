const { defineConfig } = require('@playwright/test')

module.exports = defineConfig({
  testDir: './tests',
  testMatch: 'desktop.spec.cjs',
  workers: 1,
  timeout: 30000,
  use: { trace: 'retain-on-failure' },
})
