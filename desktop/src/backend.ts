import { spawn, type ChildProcess } from 'node:child_process'
import { randomBytes } from 'node:crypto'
import { createInterface } from 'node:readline'

type ExitListener = (code: number | null, signal: NodeJS.Signals | null) => void

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

export class Backend {
  readonly binary: string
  readonly env: NodeJS.ProcessEnv
  readonly timeout: number
  readonly token: string
  url = ''
  stopping = false
  closed = false
  stderr = ''
  private child: ChildProcess | undefined
  private exited: Promise<void> = Promise.resolve()
  private readonly exitListeners = new Set<ExitListener>()

  constructor(binary: string, env: NodeJS.ProcessEnv = process.env, timeout = 15000) {
    this.binary = binary
    this.env = env
    this.timeout = timeout
    this.token = randomBytes(32).toString('hex')
  }

  on(event: 'exit', listener: ExitListener): void {
    this.exitListeners.add(listener)
  }

  async start(): Promise<this> {
    const deadline = Date.now() + this.timeout
    const child = spawn(this.binary, ['--desktop'], {
      env: { ...this.env, SCP_CLIENT_DESKTOP_TOKEN: this.token },
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    this.child = child
    const output = child.stdout
    this.exited = new Promise(resolve => {
      child.once('close', (code, signal) => {
        this.closed = true
        resolve()
        for (const listener of this.exitListeners) listener(code, signal)
      })
    })
    child.stderr?.on('data', (data: Buffer | string) => {
      this.stderr = (this.stderr + data.toString()).slice(-8192)
    })
    try {
      if (output === null) throw new Error('Cannot start the file service: missing output.')
      const ready = await new Promise<string>((resolve, reject) => {
        const lines = createInterface({ input: output })
        let timer: ReturnType<typeof setTimeout> | undefined
        const finish = (error: Error | null, url?: string): void => {
          clearTimeout(timer)
          lines.close()
          child.removeListener('error', onError)
          child.removeListener('close', onExit)
          if (error) reject(error)
          else if (url !== undefined) resolve(url)
          else reject(new Error('The file service returned an invalid startup response.'))
        }
        timer = setTimeout(() => finish(new Error('The file service did not start in time.')), this.timeout)
        const onError = (error: Error): void => finish(new Error(`Cannot start the file service: ${error.message}`))
        const onExit = (): void => finish(new Error(`The file service exited before startup. ${this.stderr.trim()}`))
        child.once('error', onError)
        child.once('close', onExit)
        lines.on('line', line => {
          let parsed: unknown
          try {
            parsed = JSON.parse(line)
          } catch {
            finish(new Error('The file service returned an invalid startup response.'))
            return
          }
          if (isRecord(parsed) && parsed.event === 'ready' && typeof parsed.url === 'string') finish(null, parsed.url)
        })
      })
      const url = new URL(ready)
      if (url.protocol !== 'http:' || url.hostname !== '127.0.0.1' || !url.port || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
        throw new Error('The file service returned an invalid localhost address.')
      }
      this.url = url.origin
      while (Date.now() < deadline) {
        if (this.closed || this.stopping) throw new Error('The file service stopped during startup.')
        try {
          const response = await fetch(`${this.url}/api/health`, { headers: this.headers(), signal: AbortSignal.timeout(1000) })
          const body: unknown = await response.json()
          if (response.ok && isRecord(body) && body.status === 'ok') return this
        } catch {
          // The listener can be ready before it begins serving HTTP.
        }
        await new Promise(resolve => setTimeout(resolve, 50))
      }
      throw new Error('The file service did not become ready in time.')
    } catch (error) {
      await this.stop()
      throw error
    }
  }

  headers(): Record<string, string> {
    return { 'X-SCP-Desktop-Token': this.token }
  }

  async stop(timeout = 5000): Promise<void> {
    this.stopping = true
    const child = this.child
    if (!child || this.closed) return
    child.kill('SIGTERM')
    const timer = setTimeout(() => { child.kill('SIGKILL') }, timeout)
    try {
      await this.exited
    } finally {
      clearTimeout(timer)
    }
  }
}
