import { execFileSync } from 'node:child_process'
import { mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

export const desktop = dirname(dirname(fileURLToPath(import.meta.url)))

export function build(arch = process.arch) {
  if (process.platform !== 'darwin') throw new Error('Build the macOS app on macOS.')
  execFileSync(process.execPath, [join(desktop, 'node_modules', 'typescript', 'lib', 'tsc.js'), '--pretty', 'false'], { cwd: desktop, stdio: 'inherit' })
  const goArch = { arm64: 'arm64', x64: 'amd64' }[arch]
  if (!goArch) throw new Error('Choose arm64 or x64.')
  const root = dirname(desktop)
  mkdirSync(join(desktop, 'build', 'backend'), { recursive: true })
  execFileSync('npm', ['--prefix', join(root, 'web'), 'run', 'build'], { stdio: 'inherit' })
  execFileSync('go', ['build', '-trimpath', '-ldflags=-s -w', '-o', join(desktop, 'build', 'backend', 'scp-client'), './cmd/scp-client'], {
    cwd: root, stdio: 'inherit', env: { ...process.env, GOOS: 'darwin', GOARCH: goArch, CGO_ENABLED: '0' },
  })
  execFileSync('swift', [join(desktop, 'scripts', 'icon.swift'), join(desktop, 'build')], { stdio: 'inherit' })
  execFileSync('iconutil', ['-c', 'icns', join(desktop, 'build', 'icon.iconset'), '-o', join(desktop, 'build', 'icon.icns')], { stdio: 'inherit' })
}

if (process.argv[1] === fileURLToPath(import.meta.url)) build()
