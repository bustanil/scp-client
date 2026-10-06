export interface Entry {
  name: string
  path: string
  directory: boolean
  symlink: boolean
  size: number
  mode: number
  modified: string
}

export interface Listing {
  path: string
  parent: string
  home: string
  entries: Entry[]
}

export interface Connection {
  id: string
  name: string
  host: string
  port: number
  username: string
  startPath: string
  auth: 'password' | 'privateKey'
  privateKeyPath: string
}

export interface Connected extends Listing {
  sessionId: string
  connectionId: string
  name: string
}

export interface CopyRequest {
  from: { kind: 'local'; path: string; names: string[] }
  to: { kind: 'local'; path: string }
  replace: boolean
}

export interface Job {
  id: string
  state: 'running' | 'done' | 'failed'
  filesTotal: number
  filesDone: number
  bytesDone: number
  current: string
  skipped: string[]
  errors: { path: string; message: string }[]
}

export class APIError extends Error {
  code: string
  names: string[]
  fingerprint: string
  keyType: string
  constructor(body: { message?: string; code?: string; names?: string[]; fingerprint?: string; keyType?: string }, status: number) {
    super(body.message ?? `Request failed (${status})`)
    this.code = body.code ?? 'request_failed'
    this.names = body.names ?? []
    this.fingerprint = body.fingerprint ?? ''
    this.keyType = body.keyType ?? ''
  }
}

export async function request<T>(url: string, init?: RequestInit): Promise<T> {
  const response = await fetch(url, init)
  if (response.status === 204) return undefined as T
  const body = await response.json()
  if (!response.ok) throw new APIError(body, response.status)
  return body as T
}

export function sizeLabel(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const units = ['KiB', 'MiB', 'GiB', 'TiB']
  let value = bytes / 1024
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++ }
  return `${value.toFixed(value < 10 ? 1 : 0)} ${units[unit]}`
}
