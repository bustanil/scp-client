const { test, expect, _electron } = require('@playwright/test')
const { mkdtemp, mkdir, writeFile, readFile, rm, access } = require('node:fs/promises')
const { spawn, execFile } = require('node:child_process')
const { promisify } = require('node:util')
const { randomUUID } = require('node:crypto')
const { tmpdir } = require('node:os')
const path = require('node:path')

let fixture
let application
const execute = promisify(execFile)

async function keychainValue(id) {
  const { stdout } = await execute('/usr/bin/security', ['find-generic-password', '-s', 'scp-client', '-a', id, '-w'])
  const stored = stdout.trim()
  const prefix = 'go-keyring-base64:'
  return stored.startsWith(prefix) ? Buffer.from(stored.slice(prefix.length), 'base64').toString('utf8') : stored
}

test.beforeEach(async () => { fixture = await mkdtemp(path.join(tmpdir(), 'scp-client-window-')) })
test.afterEach(async () => { if (application) await application.close(); application = null; await rm(fixture, { recursive: true, force: true }) })

async function launch() {
  const executablePath = process.env.SCP_CLIENT_APP_EXECUTABLE
  const env = {
    ...process.env,
    SCP_CLIENT_DATA_DIR: path.join(fixture, 'connections'),
    SCP_CLIENT_ELECTRON_DATA_DIR: path.join(fixture, 'electron'),
  }
  delete env.ELECTRON_RUN_AS_NODE
  if (executablePath) env.PATH = '/usr/bin:/bin:/usr/sbin:/sbin'
  application = await _electron.launch({
    ...(executablePath ? { executablePath, args: [] } : { args: [path.join(__dirname, '..')] }),
    env,
  })
  const page = await application.firstWindow()
  await expect(page.getByRole('heading', { name: 'scp-client', exact: true })).toBeVisible()
  await expect(page.getByRole('textbox', { name: 'Left directory path' })).not.toHaveValue('')
  return { page, env }
}

async function navigate(page, side, directory) {
  const field = page.getByRole('textbox', { name: `${side} directory path` })
  await field.fill(directory)
  await field.press('Enter')
  await expect(field).toHaveValue(directory.replace(/^\/var\//, '/private/var/'))
  await expect(page.getByRole('listbox', { name: `${side} directory` })).toHaveAttribute('aria-busy', 'false')
}

test('the app copies files and folders, confirms overwrite, and keeps Node out of the UI', async () => {
  const source = path.join(fixture, 'source')
  const destination = path.join(fixture, 'destination')
  await mkdir(path.join(source, 'folder', 'nested'), { recursive: true })
  await mkdir(destination)
  await writeFile(path.join(source, 'file.txt'), 'desktop bytes')
  await writeFile(path.join(source, 'folder', 'nested', 'child.txt'), 'child')
  await writeFile(path.join(destination, 'file.txt'), 'original')
  const { page } = await launch()
  const url = page.url()
  expect(new URL(url).hostname).toBe('127.0.0.1')
  expect(new URL(url).port).not.toBe('8787')
  expect(await page.evaluate(() => ({ process: typeof process, require: typeof require }))).toEqual({ process: 'undefined', require: 'undefined' })
  expect((await fetch(`${url}/api/health`)).status).toBe(401)
  await navigate(page, 'Left', source)
  await navigate(page, 'Right', destination)
  const left = page.getByRole('region', { name: 'Left file pane' })
  await left.getByText('folder', { exact: true }).click()
  await page.keyboard.press('Space')
  await left.getByText('file.txt', { exact: true }).click({ modifiers: ['Meta'] })
  await page.keyboard.press('F5')
  await expect(page.getByRole('heading', { name: 'Replace existing items?' })).toBeVisible()
  expect(await readFile(path.join(destination, 'file.txt'), 'utf8')).toBe('original')
  await page.getByRole('button', { name: 'Replace', exact: true }).click()
  await expect(page.getByText('Copy complete', { exact: true })).toBeVisible()
  expect(await readFile(path.join(destination, 'file.txt'), 'utf8')).toBe('desktop bytes')
  expect(await readFile(path.join(destination, 'folder', 'nested', 'child.txt'), 'utf8')).toBe('child')
  await page.screenshot({ path: 'test-results/desktop-copy.png', fullPage: true })
})

test('a second launch focuses one window and quitting closes the owned service', async () => {
  const { page, env } = await launch()
  const url = page.url()
  const executable = process.env.SCP_CLIENT_APP_EXECUTABLE || require('electron')
  const child = spawn(executable, process.env.SCP_CLIENT_APP_EXECUTABLE ? [] : [path.join(__dirname, '..')], { env, stdio: 'ignore' })
  const exited = new Promise((resolve, reject) => { child.once('exit', resolve); child.once('error', reject) })
  await expect.poll(() => application.windows().length).toBe(1)
  expect(await exited).toBe(0)
  await application.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows()[0].close())
  await expect.poll(() => application.windows().length).toBe(0)
  await application.evaluate(({ app }) => app.emit('activate'))
  const reopened = await application.firstWindow()
  await expect(reopened.getByRole('heading', { name: 'scp-client', exact: true })).toBeVisible()
  expect(reopened.url()).toBe(url)
  await application.close()
  application = null
  await expect.poll(async () => {
    try { await fetch(`${url}/api/health`, { signal: AbortSignal.timeout(500) }); return false } catch { return true }
  }).toBe(true)
})

test('saved connections persist across app restart', async () => {
  let { page } = await launch()
  await page.getByRole('button', { name: /Connections/ }).click()
  await page.getByRole('button', { name: '+ Add connection', exact: true }).click()
  await page.getByLabel('Name', { exact: true }).fill('Desktop fixture host')
  await page.getByLabel('Host', { exact: true }).fill('example.com')
  await page.getByLabel('Username', { exact: true }).fill('tester')
  await page.getByLabel('Authentication', { exact: true }).selectOption('privateKey')
  await page.getByLabel('Private-key file', { exact: true }).fill('/tmp/disposable-fixture-key')
  await page.getByRole('button', { name: 'Save connection', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Desktop fixture host', exact: true })).toBeVisible()
  await application.close()
  application = null
  ;({ page } = await launch())
  await page.getByRole('button', { name: /Connections/ }).click()
  await expect(page.getByRole('heading', { name: 'Desktop fixture host', exact: true })).toBeVisible()
  await access(path.join(fixture, 'connections', 'connections.json'))
})

test('a saved password stays in macOS Keychain and deletion removes it', async () => {
  test.skip(process.env.SCP_CLIENT_TEST_KEYCHAIN !== '1', 'Enable the disposable macOS Keychain check explicitly.')
  const secret = `desktop-test-${randomUUID()}`
  let id
  try {
    let { page } = await launch()
    await page.getByRole('button', { name: /Connections/ }).click()
    await page.getByRole('button', { name: '+ Add connection', exact: true }).click()
    await page.getByLabel('Name', { exact: true }).fill('Desktop Keychain fixture')
    await page.getByLabel('Host', { exact: true }).fill('example.com')
    await page.getByLabel('Username', { exact: true }).fill('tester')
    await page.getByLabel('Password', { exact: true }).fill(secret)
    await page.getByRole('button', { name: 'Save connection', exact: true }).click()
    await expect(page.getByRole('heading', { name: 'Desktop Keychain fixture', exact: true })).toBeVisible()
    const records = await readFile(path.join(fixture, 'connections', 'connections.json'), 'utf8')
    id = JSON.parse(records)[0].id
    expect(records).not.toContain(secret)
    expect(await keychainValue(id)).toBe(secret)
    await application.close()
    application = null
    ;({ page } = await launch())
    await page.getByRole('button', { name: /Connections/ }).click()
    await expect(page.getByRole('heading', { name: 'Desktop Keychain fixture', exact: true })).toBeVisible()
    expect(await keychainValue(id)).toBe(secret)
    await page.getByRole('button', { name: 'Delete Desktop Keychain fixture', exact: true }).click()
    await page.getByRole('button', { name: 'Delete connection', exact: true }).click()
    await expect(page.getByRole('heading', { name: 'No saved connections', exact: true })).toBeVisible()
    await expect(execute('/usr/bin/security', ['find-generic-password', '-s', 'scp-client', '-a', id])).rejects.toMatchObject({ code: 44 })
  } finally {
    if (!id) {
      try { id = JSON.parse(await readFile(path.join(fixture, 'connections', 'connections.json'), 'utf8'))[0]?.id } catch {}
    }
    if (id) await execute('/usr/bin/security', ['delete-generic-password', '-s', 'scp-client', '-a', id]).catch(error => { if (error.code !== 44) throw error })
  }
})
