const { test } = require('node:test')
const assert = require('node:assert/strict')
const { mkdtemp, rm, writeFile } = require('node:fs/promises')
const { tmpdir } = require('node:os')
const path = require('node:path')
const { Backend } = require('../backend.cjs')

const binary = path.join(__dirname, '..', 'build', 'backend', 'scp-client')

test('owned backends use independent ports, require tokens, and stop cleanly', async t => {
  const fixture = await mkdtemp(path.join(tmpdir(), 'scp-client-desktop-'))
  const env = { ...process.env, SCP_CLIENT_DATA_DIR: fixture }
  const a = new Backend(binary, env)
  const b = new Backend(binary, env)
  t.after(async () => { await Promise.all([a.stop(), b.stop()]); await rm(fixture, { recursive: true, force: true }) })
  await Promise.all([a.start(), b.start()])
  assert.notEqual(a.url, b.url)
  assert.notEqual(a.token, b.token)
  assert.notEqual(new URL(a.url).port, '8787')
  assert.equal((await fetch(`${a.url}/api/health`)).status, 401)
  assert.equal((await fetch(`${a.url}/api/health`, { headers: b.headers() })).status, 401)
  const listing = await fetch(`${a.url}/api/list?kind=local`, { headers: a.headers() })
  assert.equal(listing.status, 200)
  assert.ok((await listing.json()).path.startsWith('/'))
  await a.stop()
  await assert.rejects(fetch(`${a.url}/api/health`, { headers: a.headers(), signal: AbortSignal.timeout(1000) }))
  assert.equal((await fetch(`${b.url}/api/health`, { headers: b.headers() })).status, 200)
})

test('startup reports a missing bundled executable', async () => {
  const backend = new Backend(path.join(__dirname, 'missing-file-service'))
  await assert.rejects(backend.start(), /Cannot start the file service/)
  assert.equal(backend.closed, true)
})

test('startup timeout terminates an unresponsive child', async t => {
  const fixture = await mkdtemp(path.join(tmpdir(), 'scp-client-desktop-hang-'))
  const script = path.join(fixture, 'silent-service')
  await writeFile(script, '#!/bin/sh\nexec /bin/sleep 30\n', { mode: 0o700 })
  const backend = new Backend(script, process.env, 150)
  t.after(async () => { await backend.stop(); await rm(fixture, { recursive: true, force: true }) })
  await assert.rejects(backend.start(), /did not start in time/)
  assert.equal(backend.closed, true)
})

test('invalid service readiness cannot send the token to another host', async t => {
  const fixture = await mkdtemp(path.join(tmpdir(), 'scp-client-desktop-ready-'))
  const script = path.join(fixture, 'invalid-service')
  await writeFile(script, '#!/bin/sh\nprintf \'{"event":"ready","url":"https://example.com"}\\n\'\nexec /bin/sleep 30\n', { mode: 0o700 })
  const backend = new Backend(script)
  t.after(async () => { await backend.stop(); await rm(fixture, { recursive: true, force: true }) })
  await assert.rejects(backend.start(), /invalid localhost address/)
  assert.equal(backend.closed, true)
})
