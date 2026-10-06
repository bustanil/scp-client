import { test, expect, type Page, type Locator } from '@playwright/test'
import { mkdtemp, mkdir, writeFile, readFile, symlink, rm, stat } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

let fixture: string
let source: string
let dest: string

test.beforeEach(async () => {
  fixture = await mkdtemp(join(tmpdir(), 'scp-client-e2e-'))
  source = join(fixture, 'source')
  dest = join(fixture, 'destination')
  await mkdir(join(source, 'folder', 'nested', 'empty'), { recursive: true })
  await mkdir(dest)
  await writeFile(join(source, 'hello.txt'), 'hello file')
  await writeFile(join(source, 'z-last.txt'), 'last file')
  await writeFile(join(source, 'folder', 'nested', 'child.txt'), 'nested file')
  await symlink('hello.txt', join(source, 'link'))
  await symlink('missing', join(source, 'folder', 'broken'))
})

test.afterEach(async () => { await rm(fixture, { recursive: true, force: true }) })

async function navigate(page: Page, side: 'Left' | 'Right', path: string) {
  const input = page.getByRole('textbox', { name: `${side} directory path` })
  await input.fill(path)
  await input.press('Enter')
  await expect(input).toHaveValue(path.replace(/^\/var\//, '/private/var/'))
  await expect(page.getByRole('listbox', { name: `${side} directory` })).toHaveAttribute('aria-busy', 'false')
}

function row(pane: Locator, name: string) { return pane.getByRole('option').filter({ has: pane.page().getByText(name, { exact: true }) }) }

test('copy a file and nested folder, report links, refresh both panes', async ({ page }) => {
  const consoleErrors: string[] = []
  page.on('pageerror', error => consoleErrors.push(error.message))
  await page.goto('/')
  await expect(page.getByRole('heading', { name: 'scp-client', exact: true })).toBeVisible()
  await navigate(page, 'Left', source)
  await navigate(page, 'Right', dest)
  const left = page.getByRole('region', { name: 'Left file pane' })
  const right = page.getByRole('region', { name: 'Right file pane' })
  await row(left, 'folder').click()
  await page.keyboard.press('Space')
  await row(left, 'hello.txt').click({ modifiers: ['Meta'] })
  await row(left, 'link').click({ modifiers: ['Meta'] })
  await expect(left.getByText('3 selected')).toBeVisible()
  await page.keyboard.press('Tab')
  await expect(right).toHaveClass(/active/)
  await expect(left.getByText('3 selected')).toBeVisible()
  await page.keyboard.press('Tab')
  await page.keyboard.press('F5')
  await expect(page.getByText('Copy complete', { exact: true })).toBeVisible()
  await expect(page.getByText('2 / 2 files · 21 B written')).toBeVisible()
  await expect(page.getByText('Skipped symbolic links: folder/broken, link')).toBeVisible()
  await expect(row(right, 'hello.txt')).toBeVisible()
  await expect(row(right, 'folder')).toBeVisible()
  expect(await readFile(join(dest, 'hello.txt'), 'utf8')).toBe('hello file')
  expect(await readFile(join(dest, 'folder', 'nested', 'child.txt'), 'utf8')).toBe('nested file')
  expect((await stat(join(dest, 'folder', 'nested', 'empty'))).isDirectory()).toBe(true)
  await expect(stat(join(dest, 'link'))).rejects.toThrow()
  expect(consoleErrors).toEqual([])
  await page.screenshot({ path: 'test-results/local-copy.png', fullPage: true })
})

test('overwrite requires confirmation, Escape cancels, Replace updates file', async ({ page }) => {
  await writeFile(join(dest, 'hello.txt'), 'original')
  await page.goto('/')
  await navigate(page, 'Left', source)
  await navigate(page, 'Right', dest)
  const left = page.getByRole('region', { name: 'Left file pane' })
  await row(left, 'hello.txt').click()
  await page.keyboard.press('F5')
  await expect(page.getByRole('dialog')).toBeVisible()
  expect(await readFile(join(dest, 'hello.txt'), 'utf8')).toBe('original')
  await page.keyboard.press('Escape')
  await expect(page.getByRole('dialog')).toHaveCount(0)
  expect(await readFile(join(dest, 'hello.txt'), 'utf8')).toBe('original')
  await page.keyboard.press('F5')
  await expect(page.getByRole('dialog')).toBeVisible()
  await page.getByRole('button', { name: 'Replace', exact: true }).click()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByText('Copy complete', { exact: true })).toBeVisible()
  expect(await readFile(join(dest, 'hello.txt'), 'utf8')).toBe('hello file')
})

test('keyboard navigation, range selection, reverse copy, and path errors', async ({ page }) => {
  await writeFile(join(dest, 'return.txt'), 'return trip')
  await page.goto('/')
  await navigate(page, 'Left', source)
  await navigate(page, 'Right', dest)
  const left = page.getByRole('region', { name: 'Left file pane' })
  const right = page.getByRole('region', { name: 'Right file pane' })
  await left.focus()
  await page.keyboard.press('ArrowDown')
  await expect(row(left, 'folder')).toHaveAttribute('data-cursor', 'true')
  await page.keyboard.press('Enter')
  await expect(row(left, 'nested')).toBeVisible()
  await page.keyboard.press('Backspace')
  await expect(row(left, 'hello.txt')).toBeVisible()
  await row(left, 'hello.txt').click()
  await row(left, 'z-last.txt').click({ modifiers: ['Shift'] })
  await expect(left.getByText('3 selected')).toBeVisible()
  await row(right, 'return.txt').click()
  await page.keyboard.press('F5')
  await expect(page.getByText('Copy complete', { exact: true })).toBeVisible()
  await expect(row(left, 'return.txt')).toBeVisible()
  expect(await readFile(join(source, 'return.txt'), 'utf8')).toBe('return trip')
  const path = page.getByRole('textbox', { name: 'Left directory path' })
  await path.fill('/does-not-exist-scp-client')
  await path.press('Enter')
  await expect(left.getByRole('alert')).toBeVisible()
  await expect(row(left, 'return.txt')).toBeVisible()
  // A failed path request must keep the pane's previous directory.
  await left.focus()
  await row(left, 'hello.txt').click()
  await page.keyboard.press('F5')
  await expect(row(right, 'hello.txt')).toBeVisible()
  expect(await readFile(join(dest, 'hello.txt'), 'utf8')).toBe('hello file')
})

test('renders running progress, retries a poll failure, and reports file errors', async ({ page }) => {
  let polls = 0
  await page.route('**/api/jobs/*', async route => {
    polls++
    if (polls === 2) {
      await route.fulfill({ status: 503, json: { code: 'unavailable', message: 'Temporary failure' } })
      return
    }
    await route.fulfill({ json: {
      id: 'progress-fixture', state: polls < 3 ? 'running' : 'done',
      filesTotal: 2, filesDone: 1, bytesDone: 50,
      current: polls < 3 ? 'folder/nested/child.txt' : '',
      skipped: ['link'], errors: polls < 3 ? [] : [{ path: 'blocked.txt', message: 'Permission denied' }],
    } })
  })
  await page.goto('/')
  await navigate(page, 'Left', source)
  await navigate(page, 'Right', dest)
  await row(page.getByRole('region', { name: 'Left file pane' }), 'hello.txt').click()
  await page.keyboard.press('F5')
  await expect(page.getByText('1 / 2 files · 50 B written')).toBeVisible()
  await expect(page.locator('progress')).toHaveAttribute('value', '1')
  await expect(page.getByText('folder/nested/child.txt', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: /Copying/ })).toBeDisabled()
  await expect(page.getByText(/Progress unavailable: Temporary failure/)).toBeVisible()
  await expect(page.getByText('Copy finished with errors', { exact: true })).toBeVisible()
  await expect(page.getByText('blocked.txt: Permission denied')).toBeVisible()
  await expect(page.getByText(/Progress unavailable/)).toHaveCount(0)
})
