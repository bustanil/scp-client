import { execFileSync, spawn } from 'node:child_process'
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const desktop = dirname(dirname(fileURLToPath(import.meta.url)))
const source = join(desktop, 'node_modules', 'electron', 'dist', 'Electron.app')
const app = join(desktop, 'build', 'dev', 'scp-client.app')
const stampPath = join(desktop, 'build', 'dev', 'stamp')
const electronVersion = JSON.parse(readFileSync(join(desktop, 'node_modules', 'electron', 'package.json'), 'utf8')).version
const stamp = `${electronVersion}\n`

if (!existsSync(join(desktop, 'build', 'icon.icns'))) throw new Error('Missing build/icon.icns. Run npm run build first.')
if (!existsSync(stampPath) || readFileSync(stampPath, 'utf8') !== stamp || !existsSync(app)) {
  rmSync(app, { recursive: true, force: true })
  mkdirSync(dirname(app), { recursive: true })
  execFileSync('/usr/bin/ditto', [source, app], { stdio: 'inherit' })
  execFileSync('/bin/cp', [join(desktop, 'build', 'icon.icns'), join(app, 'Contents', 'Resources', 'electron.icns')])
  const plist = join(app, 'Contents', 'Info.plist')
  for (const [key, value] of [['CFBundleName', 'scp-client'], ['CFBundleDisplayName', 'scp-client'], ['CFBundleIdentifier', 'io.github.bustanil.scp-client']]) {
    execFileSync('/usr/libexec/PlistBuddy', ['-c', `Set :${key} ${value}`, plist], { stdio: 'inherit' })
  }
  execFileSync('/usr/bin/codesign', ['--force', '--sign', '-', app], { stdio: 'inherit' })
  writeFileSync(stampPath, stamp)
}

const env = { ...process.env }
delete env.ELECTRON_RUN_AS_NODE
const child = spawn(join(app, 'Contents', 'MacOS', 'Electron'), [desktop], { stdio: 'inherit', env })
child.on('exit', (code, signal) => {
  if (signal) process.kill(process.pid, signal)
  else process.exit(code ?? 0)
})
