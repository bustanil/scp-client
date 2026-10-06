const { spawn } = require('node:child_process')
const { randomBytes } = require('node:crypto')
const { EventEmitter } = require('node:events')
const { createInterface } = require('node:readline')

class Backend extends EventEmitter {
  constructor(binary, env = process.env, timeout = 15000) {
    super()
    this.binary = binary
    this.env = env
    this.timeout = timeout
    this.token = randomBytes(32).toString('hex')
    this.url = ''
    this.stopping = false
    this.closed = false
    this.stderr = ''
  }

  async start() {
    const deadline = Date.now() + this.timeout
    this.child = spawn(this.binary, ['--desktop'], {
      env: { ...this.env, SCP_CLIENT_DESKTOP_TOKEN: this.token },
      stdio: ['ignore', 'pipe', 'pipe'],
    })
    this.exited = new Promise(resolve => {
      this.child.once('close', (code, signal) => {
        this.closed = true
        resolve()
        this.emit('exit', code, signal)
      })
    })
    this.child.stderr.on('data', data => { this.stderr = (this.stderr + data.toString()).slice(-8192) })
    try {
      const ready = await new Promise((resolve, reject) => {
        const lines = createInterface({ input: this.child.stdout })
        const timer = setTimeout(() => finish(new Error('The file service did not start in time.')), this.timeout)
        const onError = error => finish(new Error(`Cannot start the file service: ${error.message}`))
        const onExit = () => finish(new Error(`The file service exited before startup. ${this.stderr.trim()}`))
        const finish = (error, url) => {
          clearTimeout(timer)
          lines.close()
          this.child.removeListener('error', onError)
          this.child.removeListener('close', onExit)
          error ? reject(error) : resolve(url)
        }
        this.child.once('error', onError)
        this.child.once('close', onExit)
        lines.on('line', line => {
          try {
            const value = JSON.parse(line)
            if (value.event === 'ready') finish(null, value.url)
          } catch { finish(new Error('The file service returned an invalid startup response.')) }
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
          if (response.ok && (await response.json()).status === 'ok') return this
        } catch { /* The listener can be ready before it begins serving HTTP. */ }
        await new Promise(resolve => setTimeout(resolve, 50))
      }
      throw new Error('The file service did not become ready in time.')
    } catch (error) {
      await this.stop()
      throw error
    }
  }

  headers() { return { 'X-SCP-Desktop-Token': this.token } }

  async stop(timeout = 5000) {
    this.stopping = true
    if (!this.child || this.closed) return
    this.child.kill('SIGTERM')
    const timer = setTimeout(() => this.child.kill('SIGKILL'), timeout)
    try { await this.exited } finally { clearTimeout(timer) }
  }
}

module.exports = { Backend }
