import { useCallback, useEffect, useRef, useState } from 'react'
import { APIError, request, type Connected, type Connection } from './api'

type Fields = Omit<Connection, 'id'> & { secret: string }
const blank = (): Fields => ({ name: '', host: '', port: 22, username: '', startPath: '', auth: 'password', privateKeyPath: '', secret: '' })
const message = (error: unknown) => error instanceof Error ? error.message : 'Request failed'

interface Props {
  open: boolean
  paneName: string
  canConnect: boolean
  close: () => void
  connected: (session: Connected) => void
  countChanged: (count: number) => void
}

export default function ConnectionsDialog({ open, paneName, canConnect, close, connected, countChanged }: Props) {
  const dialog = useRef<HTMLDialogElement>(null)
  const [records, setRecords] = useState<Connection[]>([])
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const busyRef = useRef(false)
  const [error, setError] = useState('')
  const [editor, setEditor] = useState<{ id: string; fields: Fields } | null>(null)
  const [deleting, setDeleting] = useState<Connection | null>(null)
  const [challenge, setChallenge] = useState<{ record: Connection; type: 'trust' | 'secret'; fingerprint?: string; keyType?: string; passphrase?: boolean } | null>(null)
  const [loginSecret, setLoginSecret] = useState('')
  const [saveSecret, setSaveSecret] = useState(true)
  const retry = useRef<{ secret?: string; saveSecret?: boolean; trustFingerprint?: string }>({})

  const refresh = useCallback(async (signal?: AbortSignal) => {
    setLoading(true)
    try {
      const list = await request<Connection[]>('/api/connections', { signal })
      if (signal?.aborted) return
      setRecords(list)
      countChanged(list.length)
    } catch (issue) { if (!signal?.aborted) setError(message(issue)) }
    finally { if (!signal?.aborted) setLoading(false) }
  }, [countChanged])

  useEffect(() => {
    const controller = new AbortController()
    void refresh(controller.signal)
    return () => controller.abort()
  }, [refresh])

  useEffect(() => {
    if (open) {
      setError(''); setEditor(null); setDeleting(null); setChallenge(null); setLoginSecret(''); retry.current = {}
      dialog.current?.showModal()
      void refresh()
    } else { dialog.current?.close(); setEditor(null); setChallenge(null); setLoginSecret(''); retry.current = {} }
  }, [open, refresh])

  const perform = async (action: () => Promise<void>) => {
    if (busyRef.current) return
    busyRef.current = true; setBusy(true); setError('')
    try { await action() } catch (issue) { setError(message(issue)) }
    finally { busyRef.current = false; setBusy(false) }
  }

  const save = () => perform(async () => {
    if (!editor) return
    const { secret, ...fields } = editor.fields
    const body = secret ? { ...fields, secret } : fields
    await request<Connection>(editor.id ? `/api/connections/${editor.id}` : '/api/connections', {
      method: editor.id ? 'PUT' : 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    })
    setEditor(null)
    await refresh()
  })

  const connect = async (record: Connection, details: typeof retry.current = {}) => {
    if (busyRef.current || !canConnect) return
    busyRef.current = true; setBusy(true); setError('')
    retry.current = details
    try {
      const session = await request<Connected>('/api/sessions', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ connectionId: record.id, ...details }),
      })
      setChallenge(null); setLoginSecret(''); retry.current = {}
      connected(session)
    } catch (issue) {
      if (issue instanceof APIError && issue.code === 'unknown_host_key') {
        setChallenge({ record, type: 'trust', fingerprint: issue.fingerprint, keyType: issue.keyType })
      } else if (issue instanceof APIError && (issue.code === 'passphrase_required' || issue.code === 'secret_required')) {
        setLoginSecret('')
        setSaveSecret(true)
        setChallenge({ record, type: 'secret', passphrase: issue.code === 'passphrase_required' })
      } else setError(message(issue))
    } finally { busyRef.current = false; setBusy(false) }
  }

  const field = <K extends keyof Fields>(name: K, value: Fields[K]) => setEditor(previous => previous ? { ...previous, fields: { ...previous.fields, [name]: value } } : null)
  const cancelChallenge = () => { setChallenge(null); setError(''); setLoginSecret(''); retry.current = {} }

  return <dialog ref={dialog} className="connections-dialog" aria-label="SSH connections" onCancel={event => { event.preventDefault(); if (!busy) close() }}>
    <header className="connections-header"><div><div className="dialog-label">SSH CONNECTIONS</div><h2>{editor ? editor.id ? 'Edit connection' : 'New connection' : challenge?.type === 'trust' ? 'Trust this host?' : challenge?.type === 'secret' ? 'Unlock connection' : 'Saved connections'}</h2></div><button aria-label="Close connections" disabled={busy} onClick={close}>×</button></header>
    {error && <p className="connection-error" role="alert">{error}</p>}
    {editor ? <form className="connection-form" onSubmit={event => { event.preventDefault(); void save() }}>
      <div className="form-fields">
        <label className="full-field">Name<input autoFocus required maxLength={120} value={editor.fields.name} onChange={event => field('name', event.target.value)} placeholder="My server" /></label>
        <label>Host<input required value={editor.fields.host} onChange={event => field('host', event.target.value)} placeholder="example.com" spellCheck={false} /></label>
        <label>Port<input type="number" required min={1} max={65535} value={editor.fields.port} onChange={event => field('port', Number(event.target.value))} /></label>
        <label>Username<input required value={editor.fields.username} onChange={event => field('username', event.target.value)} autoComplete="off" /></label>
        <label>Start path<input value={editor.fields.startPath} onChange={event => field('startPath', event.target.value)} placeholder="Server default" spellCheck={false} /></label>
        <label className="full-field">Authentication<select aria-label="Authentication" value={editor.fields.auth} onChange={event => { field('auth', event.target.value as Fields['auth']); field('secret', '') }}><option value="password">Password</option><option value="privateKey">Private key</option></select></label>
        {editor.fields.auth === 'privateKey' && <label className="full-field">Private-key file<input aria-label="Private-key file" aria-describedby="key-file-help" required value={editor.fields.privateKeyPath} onChange={event => field('privateKeyPath', event.target.value)} placeholder="/Users/you/.ssh/id_ed25519" spellCheck={false} /><small id="key-file-help">The key stays in this file. Enter its absolute path on your Mac.</small></label>}
        <label className="full-field">{editor.fields.auth === 'password' ? 'Password' : 'Key passphrase (if encrypted)'}<input aria-label={editor.fields.auth === 'password' ? 'Password' : 'Key passphrase (if encrypted)'} aria-describedby="secret-help" type="password" value={editor.fields.secret} onChange={event => field('secret', event.target.value)} autoComplete="new-password" placeholder={editor.id ? 'Leave blank to keep the saved secret' : editor.fields.auth === 'password' ? 'Enter now or when connecting' : 'Leave blank for an unencrypted key'} /><small id="secret-help">Stored in macOS Keychain. Saved secrets are never displayed.</small></label>
      </div>
      <div className="dialog-actions"><button type="button" disabled={busy} onClick={() => { setEditor(null); setError('') }}>Cancel</button><button className="primary-button" disabled={busy}>{busy ? 'Saving…' : 'Save connection'}</button></div>
    </form> : challenge ? <div className="connection-challenge">
      <p><strong>{challenge.record.name}</strong> · {challenge.record.username}@{challenge.record.host}:{challenge.record.port}</p>
      {challenge.type === 'trust' ? <><p>Compare this fingerprint with the server’s key before trusting it. Trust saves the key for future connections.</p><div className="fingerprint"><strong>{challenge.keyType}</strong><code>{challenge.fingerprint}</code></div><div className="dialog-actions"><button autoFocus disabled={busy} onClick={cancelChallenge}>Cancel</button><button className="primary-button" disabled={busy} onClick={() => void connect(challenge.record, { ...retry.current, trustFingerprint: challenge.fingerprint })}>{busy ? 'Connecting…' : 'Trust and connect'}</button></div></> : <form onSubmit={event => { event.preventDefault(); void connect(challenge.record, { ...retry.current, secret: loginSecret, saveSecret }) }}><label>{challenge.passphrase ? 'Private-key passphrase' : 'Password'}<input type="password" autoFocus required value={loginSecret} onChange={event => setLoginSecret(event.target.value)} autoComplete="off" /></label><label className="checkbox-label"><input type="checkbox" checked={saveSecret} onChange={event => setSaveSecret(event.target.checked)} />Save in macOS Keychain</label><div className="dialog-actions"><button type="button" disabled={busy} onClick={cancelChallenge}>Cancel</button><button className="primary-button" disabled={busy}>{busy ? 'Connecting…' : 'Connect'}</button></div></form>}
    </div> : deleting ? <div className="connection-challenge"><p>Delete <strong>{deleting.name}</strong> and its saved secret?</p><p>This removes the saved connection from this Mac.</p><div className="dialog-actions"><button autoFocus disabled={busy} onClick={() => setDeleting(null)}>Cancel</button><button className="danger-button" disabled={busy} onClick={() => void perform(async () => { await request<void>(`/api/connections/${deleting.id}`, { method: 'DELETE' }); setDeleting(null); await refresh() })}>{busy ? 'Deleting…' : 'Delete connection'}</button></div></div> : <>
      <div className="connection-intro"><p>Connect into the <strong>{paneName.toLowerCase()}</strong>.</p><button className="primary-button" disabled={busy} onClick={() => { setEditor({ id: '', fields: blank() }); setError('') }}>+ Add connection</button></div>
      {!canConnect && <p className="connection-hint">Disconnect this pane before connecting another host.</p>}
      {loading ? <p className="connection-hint">Loading connections…</p> : records.length === 0 ? <div className="connections-empty"><span aria-hidden="true">⇄</span><h3>No saved connections</h3><p>Add an SSH host to browse its files beside your local folder.</p></div> : <div className="connection-list">{records.map(record => <article key={record.id} className="connection-card"><div><h3>{record.name}</h3><p>{record.username}@{record.host}:{record.port}</p><small>{record.auth === 'password' ? 'Password' : 'Private key'}{record.startPath ? ` · ${record.startPath}` : ''}</small></div><div className="connection-card-actions"><button className="primary-button" aria-label={`Connect ${record.name}`} disabled={busy || !canConnect} onClick={() => { retry.current = {}; void connect(record) }}>{busy ? 'Connecting…' : 'Connect'}</button><button aria-label={`Edit ${record.name}`} disabled={busy} onClick={() => { setEditor({ id: record.id, fields: { ...record, secret: '' } }); setError('') }}>Edit</button><button aria-label={`Delete ${record.name}`} disabled={busy} onClick={() => { setDeleting(record); setError('') }}>Delete</button></div></article>)}</div>}
    </>}
  </dialog>
}
