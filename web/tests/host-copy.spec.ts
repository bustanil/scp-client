import { test, expect, type Page } from '@playwright/test'

const hostA = { id: 'host-a', name: 'Host A', host: 'a.example.com', port: 22, username: 'tester', startPath: '/srv', auth: 'password', privateKeyPath: '' }
const hostB = { ...hostA, id: 'host-b', name: 'Host B', host: 'b.example.com' }
const file = (name: string, directory = false) => ({ name, path: `/srv/${name}`, directory, symlink: false, size: 12, mode: directory ? 2147484141 : 420, modified: '2026-10-06T05:00:00Z' })
const local = { path: '/local', parent: '/', home: '/local', entries: [] }
const listingA = { path: '/srv', parent: '/', home: '/srv', entries: [file('folder', true), file('hello.txt')] }
const listingB = { ...listingA, entries: [file('return.txt')] }

async function connect(page: Page, side: 'Left' | 'Right', name: string) {
  await page.getByRole('region', { name: `${side} file pane` }).click()
  await page.getByRole('button', { name: /Connections/ }).click()
  await page.getByRole('button', { name: `Connect ${name}`, exact: true }).click()
  await expect(page.getByRole('region', { name: `${side} file pane` }).getByText(name, { exact: true })).toBeVisible()
}

test('copy folders between hosts, confirm overwrite, refresh, and reverse direction', async ({ page }) => {
  await page.route('**/api/connections', route => route.fulfill({ json: [hostA, hostB] }))
  await page.route('**/api/sessions', route => {
    const a = route.request().postDataJSON().connectionId === hostA.id
    return route.fulfill({ status: 201, json: { ...(a ? listingA : listingB), sessionId: a ? 'session-a' : 'session-b', connectionId: a ? hostA.id : hostB.id, name: a ? hostA.name : hostB.name } })
  })
  await page.route('**/api/sessions/*', route => route.fulfill({ status: 204 }))
  await page.route('**/api/list?kind=local**', route => route.fulfill({ json: local }))
  let copied = false
  const refreshed: string[] = []
  await page.route('**/api/list?kind=sftp**', route => {
    const id = new URL(route.request().url()).searchParams.get('sessionId')!
    refreshed.push(id)
    return route.fulfill({ json: id === 'session-a' ? listingA : copied ? { ...listingB, entries: [file('folder', true), file('hello.txt'), file('return.txt')] } : listingB })
  })
  const copies: Record<string, unknown>[] = []
  const job = { id: 'relay-job', state: 'running', filesTotal: 2, filesDone: 0, bytesDone: 0, current: '', skipped: [], errors: [] }
  await page.route('**/api/jobs', route => {
    const body = route.request().postDataJSON()
    copies.push(body)
    return route.fulfill(copies.length <= 2 && !body.replace ? { status: 409, json: { code: 'exists', message: 'Destination exists', names: ['hello.txt'] } } : { status: 202, json: { ...job, filesTotal: copies.length === 4 ? 1 : 2 } })
  })
  await page.route('**/api/jobs/relay-job', route => {
    copied = true
    return route.fulfill({ json: { ...job, state: 'done', filesTotal: copies.length === 4 ? 1 : 2, filesDone: copies.length === 4 ? 1 : 2, bytesDone: copies.length === 4 ? 12 : 24 } })
  })
  await page.goto('/')
  await connect(page, 'Left', 'Host A')
  const left = page.getByRole('region', { name: 'Left file pane' })
  const right = page.getByRole('region', { name: 'Right file pane' })
  await left.getByText('folder', { exact: true }).click()
  await page.keyboard.press('Space')
  await left.getByText('hello.txt', { exact: true }).click({ modifiers: ['Meta'] })
  await connect(page, 'Right', 'Host B')
  await expect(left.getByText('2 selected', { exact: true })).toBeVisible()
  await left.click()
  await expect(page.locator('.copy-direction')).toHaveText('LEFT → RIGHT · HOST TO HOST')
  await expect(page.getByRole('button', { name: /Copy to other pane/ })).toBeEnabled()
  await page.keyboard.press('F5')
  await expect(page.getByRole('heading', { name: 'Replace existing items?' })).toBeVisible()
  await page.keyboard.press('Escape')
  expect(copies).toHaveLength(1)
  await page.keyboard.press('F5')
  await page.getByRole('button', { name: 'Replace', exact: true }).click()
  await expect(page.getByText('Copy complete', { exact: true })).toBeVisible()
  await expect(right.getByText('hello.txt', { exact: true })).toBeVisible()
  expect(copies[2]).toEqual({ from: { kind: 'sftp', sessionId: 'session-a', path: '/srv', names: ['folder', 'hello.txt'] }, to: { kind: 'sftp', sessionId: 'session-b', path: '/srv' }, replace: true })
  expect(refreshed).toEqual(expect.arrayContaining(['session-a', 'session-b']))
  await page.screenshot({ path: 'test-results/host-to-host.png', fullPage: true })
  await right.getByText('return.txt', { exact: true }).click()
  await expect(page.locator('.copy-direction')).toHaveText('RIGHT → LEFT · HOST TO HOST')
  const nextRequest = page.waitForRequest(request => request.url().endsWith('/api/jobs') && request.method() === 'POST')
  await page.keyboard.press('F5')
  expect((await nextRequest).postDataJSON()).toEqual({ from: { kind: 'sftp', sessionId: 'session-b', path: '/srv', names: ['return.txt'] }, to: { kind: 'sftp', sessionId: 'session-a', path: '/srv' }, replace: false })
  await expect(page.getByText('1 / 1 files · 12 B written')).toBeVisible()
})

test('two panes on one saved host use separate sessions and disconnect independently', async ({ page }) => {
  await page.route('**/api/connections', route => route.fulfill({ json: [hostA] }))
  await page.route('**/api/list?kind=local**', route => route.fulfill({ json: local }))
  let opened = 0
  await page.route('**/api/sessions', route => {
    opened++
    return route.fulfill({ status: 201, json: { ...listingA, sessionId: `same-host-${opened}`, connectionId: hostA.id, name: hostA.name } })
  })
  const disconnected: string[] = []
  await page.route('**/api/sessions/*', route => { disconnected.push(route.request().url().split('/').pop()!); return route.fulfill({ status: 204 }) })
  await page.route('**/api/list?kind=sftp**', route => {
    const directory = new URL(route.request().url()).searchParams.get('path')!
    return route.fulfill({ json: { ...listingA, path: directory, entries: directory === '/srv/destination' ? [] : listingA.entries } })
  })
  const job = { id: 'same-host-job', state: 'running', filesTotal: 1, filesDone: 0, bytesDone: 0, current: '', skipped: [], errors: [] }
  const copies: unknown[] = []
  await page.route('**/api/jobs', route => { copies.push(route.request().postDataJSON()); return route.fulfill({ status: 202, json: job }) })
  await page.route('**/api/jobs/same-host-job', route => route.fulfill({ json: { ...job, state: 'done', filesDone: 1, bytesDone: 12 } }))
  await page.goto('/')
  await connect(page, 'Left', 'Host A')
  await connect(page, 'Right', 'Host A')
  const rightPath = page.getByRole('textbox', { name: 'Right directory path' })
  await rightPath.fill('/srv/destination')
  await rightPath.press('Enter')
  await expect(rightPath).toHaveValue('/srv/destination')
  await expect(page.getByRole('listbox', { name: 'Right directory' })).toHaveAttribute('aria-busy', 'false')
  const left = page.getByRole('region', { name: 'Left file pane' })
  await left.getByText('hello.txt', { exact: true }).click()
  await page.keyboard.press('F5')
  await expect(page.getByText('Copy complete', { exact: true })).toBeVisible()
  expect(copies[0]).toEqual({ from: { kind: 'sftp', sessionId: 'same-host-1', path: '/srv', names: ['hello.txt'] }, to: { kind: 'sftp', sessionId: 'same-host-2', path: '/srv/destination' }, replace: false })
  await left.getByRole('button', { name: 'Disconnect', exact: true }).click()
  await expect(page.getByRole('textbox', { name: 'Left directory path' })).toHaveValue('/local')
  await expect(rightPath).toHaveValue('/srv/destination')
  await expect(page.getByRole('region', { name: 'Right file pane' }).getByText('Host A', { exact: true })).toBeVisible()
  expect(disconnected).toEqual(['same-host-1'])
})
