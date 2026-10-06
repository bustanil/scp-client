import type { FullConfig } from '@playwright/test'
import { rm } from 'node:fs/promises'

export default async function teardown(config: FullConfig) {
  await rm(config.metadata.dataDir as string, { recursive: true, force: true })
}
