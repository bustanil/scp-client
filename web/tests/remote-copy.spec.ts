import { test, expect, type Page } from '@playwright/test'

const host = { id: 'copy-host', name: 'Copy host', host: 'example.com', port: 22, username: 'tester', startPath: '/remote', auth: 'password', privateKeyPath: '' }
const file = (name: string, base: string, directory = false) => ({ name, path: `${base}/${name}`, directory, symlink: false, size: 12, mode: directory ? 2147484141 : 420, modified: '2026-10-06T05:00:00Z' })
const local = { path: '/local', parent: '/', home: '/local', entries: [file('folder', '/local', true), file('upload.txt', '/local')] }
const remote = { path: '/remote', parent: '/', home: '/remote', entries: [file('remote.txt', '/remote')] }

async function connectRemote(page: Page, side: 'Left' | 'Right') {
  await page.getByRole('region', { name: `${side} file pane` }).focus()
  await page.getByRole('button', { name: /Connections/ }).click()
  await page.getByRole('button', { name: 'Connect Copy host', exact: true }).click()
  await expect(page.getByRole('textbox', { name: `${side} directory path` })).toHaveValue('/remote')
}

test.beforeEach(async ({ page }) => {
  await page.route('**/api/connections', route => route.fulfill({ json: [host] }))
  await page.route('**/api/sessions', route => route.fulfill({ status: 201, json: { ...remote, sessionId: 'copy-session', connectionId: host.id, name: host.name } }))
  await page.route('**/api/sessions/copy-session', route => route.fulfill({ status: 204 }))
  await page.route('**/api/list?kind=local**', route => route.fulfill({ json: local }))
  await page.route('**/api/list?kind=sftp**', route => route.fulfill({ json: remote }))
})

test('upload selected file and folder with overwrite confirmation and refresh', async ({ page }) => {
  const copies: Record<string, unknown>[] = []
  let finished = false
  const initial = { id: 'upload-job', state: 'running', filesTotal: 2, filesDone: 0, bytesDone: 0, current: '', skipped: [], errors: [] }
  await page.route('**/api/jobs', async route => {
    const body = route.request().postDataJSON()
    copies.push(body)
    await route.fulfill(body.replace ? { status: 202, json: initial } : { status: 409, json: { code: 'exists', message: 'Destination exists', names: ['upload.txt'] } })
  })
  await page.route('**/api/jobs/upload-job', async route => { finished = true; await route.fulfill({ json: { ...initial, state: 'done', filesDone: 2, bytesDone: 24 } }) })
  await page.route('**/api/list?kind=sftp**', route => route.fulfill({ json: finished ? { ...remote, entries: [...remote.entries, file('folder', '/remote', true), file('upload.txt', '/remote')] } : remote }))
  await page.goto('/')
  await connectRemote(page, 'Right')
  const left = page.getByRole('region', { name: 'Left file pane' })
  await left.getByRole('option').filter({ hasText: 'folder' }).click()
  await page.keyboard.press('Space')
  await left.getByRole('option').filter({ hasText: 'upload.txt' }).click({ modifiers: ['Meta'] })
  await expect(page.locator('.copy-direction')).toHaveText('LEFT → RIGHT · UPLOAD')
  await expect(page.getByRole('button', { name: /Copy to other pane/ })).toBeEnabled()
  await page.keyboard.press('F5')
  await expect(page.getByRole('heading', { name: 'Replace existing items?' })).toBeVisible()
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  expect(copies).toHaveLength(1)
  await page.keyboard.press('F5')
  await page.getByRole('button', { name: 'Replace', exact: true }).click()
  await expect(page.getByText('Copy complete', { exact: true })).toBeVisible()
  await expect(page.getByRole('region', { name: 'Right file pane' }).getByText('upload.txt', { exact: true })).toBeVisible()
  expect(copies[2]).toEqual({ from: { kind: 'local', sessionId: '', path: '/local', names: ['folder', 'upload.txt'] }, to: { kind: 'sftp', sessionId: 'copy-session', path: '/remote' }, replace: true })
  await page.screenshot({ path: 'test-results/remote-upload.png', fullPage: true })
})

test('download cursor row with F5 and show a dropped session as failed', async ({ page }) => {
  let copy: unknown
  let completed = false
  const job = { id: 'download-job', state: 'running', filesTotal: 1, filesDone: 0, bytesDone: 0, current: 'remote.txt', skipped: [], errors: [] }
  await page.route('**/api/jobs', async route => { copy = route.request().postDataJSON(); await route.fulfill({ status: 202, json: job }) })
  await page.route('**/api/jobs/download-job', route => route.fulfill({ json: completed ? { ...job, state: 'failed', bytesDone: 12, current: '', errors: [{ path: 'remote.txt', message: 'SSH session ended during copy. Disconnect and connect again' }] } : job }))
  await page.goto('/')
  await connectRemote(page, 'Left')
  await page.getByRole('region', { name: 'Left file pane' }).getByText('remote.txt', { exact: true }).click()
  await expect(page.locator('.copy-direction')).toHaveText('LEFT → RIGHT · DOWNLOAD')
  await page.keyboard.press('F5')
  await expect(page.getByText('Copying', { exact: true })).toBeVisible()
  expect(copy).toEqual({ from: { kind: 'sftp', sessionId: 'copy-session', path: '/remote', names: ['remote.txt'] }, to: { kind: 'local', sessionId: '', path: '/local' }, replace: false })
  completed = true
  await expect(page.getByText('Copy failed', { exact: true })).toBeVisible()
  await expect(page.getByText('remote.txt: SSH session ended during copy. Disconnect and connect again')).toBeVisible()
})
