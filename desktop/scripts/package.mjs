import { execFileSync } from 'node:child_process'
import { join } from 'node:path'
import { build, desktop } from './build.mjs'

const command = process.argv[2]
const arch = process.argv.slice(3).find(value => value.startsWith('--arch='))?.slice(7) || process.arch
if (!['package', 'make'].includes(command)) throw new Error('Choose package or make.')
build(arch)
const env = { ...process.env }
delete env.ELECTRON_RUN_AS_NODE
execFileSync(join(desktop, 'node_modules', '.bin', 'electron-forge'), [command, '--platform=darwin', `--arch=${arch}`], { cwd: desktop, stdio: 'inherit', env })
