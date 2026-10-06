import { useCallback, useEffect, useRef, useState, type MouseEvent } from 'react'
import { APIError, request, sizeLabel, type Connected, type CopyRequest, type Entry, type Job, type Listing } from './api'
import ConnectionsDialog from './ConnectionsDialog'

interface PaneState extends Listing {
  kind: 'local' | 'sftp'
  sessionId: string
  connectionName: string
  localPath: string
  localHome: string
  cursor: number
  anchor: number
  selected: string[]
  loading: boolean
  error: string
}

const emptyPane = (): PaneState => ({ kind: 'local', sessionId: '', connectionName: '', localPath: '', localHome: '', path: '', parent: '', home: '', entries: [], cursor: 0, anchor: 0, selected: [], loading: true, error: '' })
const errorMessage = (error: unknown) => error instanceof Error ? error.message : 'Request failed'
const dateLabel = (value: string) => new Date(value).toLocaleString(undefined, { month: 'short', day: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit' })

export default function App() {
  const [panes, setPanes] = useState<[PaneState, PaneState]>([emptyPane(), emptyPane()])
  const [active, setActive] = useState(0)
  const [job, setJob] = useState<Job | null>(null)
  const [starting, setStarting] = useState(false)
  const [notice, setNotice] = useState('')
  const [pollError, setPollError] = useState('')
  const [conflict, setConflict] = useState<{ request: CopyRequest; names: string[] } | null>(null)
  const [connectionsOpen, setConnectionsOpen] = useState(false)
  const [connectionPane, setConnectionPane] = useState(0)
  const [connectionCount, setConnectionCount] = useState(0)
  const paneRefs = useRef<(HTMLElement | null)[]>([])
  const controllers = useRef<(AbortController | null)[]>([])
  const startingRef = useRef(false)
  const panesRef = useRef(panes)
  const dialogRef = useRef<HTMLDialogElement>(null)
  panesRef.current = panes

  const updatePane = useCallback((index: number, update: (pane: PaneState) => PaneState) => {
    setPanes(previous => previous.map((pane, i) => i === index ? update(pane) : pane) as [PaneState, PaneState])
  }, [])

  const load = useCallback(async (index: number, path: string, preserve = false, target?: Pick<PaneState, 'kind' | 'sessionId'>) => {
    controllers.current[index]?.abort()
    const controller = new AbortController()
    controllers.current[index] = controller
    updatePane(index, pane => ({ ...pane, loading: true, error: '' }))
    try {
      const endpoint = target ?? panesRef.current[index]
      const listing = await request<Listing>(`/api/list?kind=${endpoint.kind}&sessionId=${encodeURIComponent(endpoint.sessionId)}&path=${encodeURIComponent(path)}`, { signal: controller.signal })
      if (controller.signal.aborted) return
      updatePane(index, pane => {
        const cursorName = pane.entries[pane.cursor - 1]?.name
        const cursor = preserve && cursorName ? Math.max(0, listing.entries.findIndex(entry => entry.name === cursorName) + 1) : 0
        return { ...pane, ...endpoint, ...listing, localHome: endpoint.kind === 'local' ? listing.home : pane.localHome, cursor, anchor: cursor, selected: preserve ? pane.selected.filter(name => listing.entries.some(entry => entry.name === name)) : [], loading: false, error: '' }
      })
    } catch (error) {
      if (!controller.signal.aborted) updatePane(index, pane => ({ ...pane, loading: false, error: errorMessage(error) }))
    }
  }, [updatePane])

  useEffect(() => {
    void load(0, '')
    void load(1, '')
    return () => controllers.current.forEach(controller => controller?.abort())
  }, [load])

  useEffect(() => {
    const closeSessions = () => {
      panesRef.current.forEach(pane => {
        if (pane.sessionId) void fetch(`/api/sessions/${pane.sessionId}`, { method: 'DELETE', keepalive: true }).catch(() => {})
      })
    }
    window.addEventListener('pagehide', closeSessions)
    return () => window.removeEventListener('pagehide', closeSessions)
  }, [])

  useEffect(() => {
    if (conflict) dialogRef.current?.showModal()
  }, [conflict])

  const focusPane = useCallback((index: number) => {
    setActive(index)
    paneRefs.current[index]?.focus()
  }, [])

  const closeConnections = () => { setConnectionsOpen(false); focusPane(connectionPane) }
  const connected = (session: Connected) => {
    controllers.current[connectionPane]?.abort()
    updatePane(connectionPane, pane => ({ ...pane, ...session, kind: 'sftp', sessionId: session.sessionId, connectionName: session.name, localPath: pane.kind === 'local' ? pane.path : pane.localPath, cursor: 0, anchor: 0, selected: [], loading: false, error: '' }))
    setNotice('')
    closeConnections()
  }

  const disconnect = async (index: number) => {
    const pane = panesRef.current[index]
    updatePane(index, value => ({ ...value, loading: true, error: '' }))
    try {
      await request<void>(`/api/sessions/${pane.sessionId}`, { method: 'DELETE' })
      updatePane(index, value => ({ ...value, kind: 'local', sessionId: '', connectionName: '', path: pane.localPath, parent: pane.localPath.slice(0, pane.localPath.lastIndexOf('/')) || '/', home: pane.localHome, entries: [], selected: [], cursor: 0, anchor: 0 }))
      await load(index, pane.localPath, false, { kind: 'local', sessionId: '' })
      focusPane(index)
    } catch (error) { updatePane(index, value => ({ ...value, loading: false, error: errorMessage(error) })) }
  }

  const startCopy = useCallback(async (copy: CopyRequest) => {
    if (startingRef.current) return
    startingRef.current = true
    setStarting(true)
    setNotice('')
    setPollError('')
    try {
      const result = await request<Job>('/api/jobs', {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(copy),
      })
      setConflict(null)
      setJob(result)
    } catch (error) {
      if (error instanceof APIError && error.code === 'exists') setConflict({ request: copy, names: error.names })
      else { setConflict(null); setNotice(errorMessage(error)) }
    } finally {
      startingRef.current = false
      setStarting(false)
    }
  }, [])

  const running = job?.state === 'running'
  const copySelection = useCallback(() => {
    if (running || startingRef.current || conflict) return
    const source = panes[active]
    const destination = panes[1 - active]
    if (source.kind !== 'local' || destination.kind !== 'local') { setNotice('Copy is available between local folders.'); return }
    if (source.loading || destination.loading || !source.path || !destination.path) return
    const cursorEntry = source.entries[source.cursor - 1]
    const names = source.selected.length ? source.selected : cursorEntry ? [cursorEntry.name] : []
    if (!names.length) { setNotice('Select a file or folder to copy. The parent row cannot be copied.'); return }
    void startCopy({ from: { kind: 'local', path: source.path, names }, to: { kind: 'local', path: destination.path }, replace: false })
  }, [active, conflict, panes, running, startCopy])

  useEffect(() => {
    if (!job || job.state !== 'running') return
    let disposed = false
    let timer: ReturnType<typeof setTimeout>
    const controller = new AbortController()
    const poll = async () => {
      try {
        const next = await request<Job>(`/api/jobs/${job.id}`, { signal: controller.signal })
        if (disposed) return
        setJob(next)
        setPollError('')
        if (next.state !== 'running') {
          panesRef.current.forEach((pane, index) => { void load(index, pane.path, true) })
          return
        }
      } catch (error) {
        if (disposed) return
        setPollError(`Progress unavailable: ${errorMessage(error)}. Retrying…`)
      }
      timer = setTimeout(poll, 333)
    }
    void poll()
    return () => { disposed = true; controller.abort(); clearTimeout(timer) }
  }, [job?.id, job?.state, load])

  useEffect(() => {
    const handle = (event: KeyboardEvent) => {
      if (conflict || connectionsOpen || event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey) return
      const target = event.target as HTMLElement
      if (target.closest('input, textarea, select, dialog')) return
      if (event.key === 'Tab') {
        event.preventDefault()
        focusPane(1 - active)
        return
      }
      if (event.key === 'F5') { event.preventDefault(); copySelection(); return }
      if (target.closest('button') || panes[active].loading) return
      const pane = panes[active]
      if (event.key === 'ArrowUp' || event.key === 'ArrowDown') {
        event.preventDefault()
        const step = event.key === 'ArrowUp' ? -1 : 1
        updatePane(active, value => ({ ...value, cursor: Math.max(0, Math.min(value.entries.length, value.cursor + step)) }))
      } else if (event.key === 'Enter') {
        event.preventDefault()
        if (pane.cursor === 0) void load(active, pane.parent)
        else if (pane.entries[pane.cursor - 1]?.directory) void load(active, pane.entries[pane.cursor - 1].path)
      } else if (event.key === 'Backspace') {
        event.preventDefault()
        void load(active, pane.parent)
      } else if (event.key === ' ') {
        event.preventDefault()
        const name = pane.entries[pane.cursor - 1]?.name
        if (name) updatePane(active, value => ({ ...value, anchor: value.cursor, selected: value.selected.includes(name) ? value.selected.filter(item => item !== name) : [...value.selected, name] }))
      }
    }
    window.addEventListener('keydown', handle)
    return () => window.removeEventListener('keydown', handle)
  }, [active, conflict, connectionsOpen, copySelection, focusPane, load, panes, updatePane])

  useEffect(() => {
    paneRefs.current[active]?.querySelector('[data-cursor="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [active, panes[0].cursor, panes[1].cursor])

  const clickRow = (index: number, cursor: number, event: MouseEvent) => {
    focusPane(index)
    updatePane(index, pane => {
      const name = pane.entries[cursor - 1]?.name
      let selected: string[] = []
      if (name && event.shiftKey) {
        selected = pane.entries.slice(Math.max(1, Math.min(pane.anchor, cursor)) - 1, Math.max(pane.anchor, cursor)).map(entry => entry.name)
      } else if (name && (event.metaKey || event.ctrlKey)) {
        selected = pane.selected.includes(name) ? pane.selected.filter(item => item !== name) : [...pane.selected, name]
      }
      return { ...pane, cursor, anchor: event.shiftKey ? pane.anchor : cursor, selected }
    })
  }

  const closeConflict = () => { setConflict(null); focusPane(active) }
  const source = panes[active]
  const cursorEntry = source.entries[source.cursor - 1]
  const copyCount = source.selected.length || (cursorEntry ? 1 : 0)
  const localCopy = panes.every(pane => pane.kind === 'local')
  const ready = !running && !starting && !conflict && panes.every(pane => pane.kind === 'local' && pane.path && !pane.loading)

  return (
    <main className="app">
      <header className="app-header">
        <div className="brand"><span className="brand-mark" aria-hidden="true">⇄</span><div><h1>scp-client</h1><p>Two panes. One copy.</p></div></div>
        <div className="header-actions"><button disabled={running || starting} onClick={() => { setConnectionPane(active); setConnectionsOpen(true) }}>Connections <span className="connection-count">{connectionCount}</span></button><span className="local-badge"><span />LOCAL + SSH</span></div>
      </header>
      <div className="workspace-heading"><span>FILE WORKSPACE</span><p>Select on one side. Copy to the other.</p></div>
      <div className="panes">
        {panes.map((pane, index) => (
          <section key={index} className={`pane ${active === index ? 'active' : ''}`} tabIndex={0}
            ref={element => { paneRefs.current[index] = element }} aria-label={`${index === 0 ? 'Left' : 'Right'} file pane`}
            aria-activedescendant={`pane-${index}-row-${pane.cursor}`} onFocus={() => setActive(index)} onClick={() => setActive(index)}>
            <div className="pane-heading"><div><span className="pane-number">0{index + 1}</span><strong>{index === 0 ? 'Left pane' : 'Right pane'}</strong><span className={`location-badge ${pane.kind === 'sftp' ? 'remote-badge' : ''}`} title={pane.connectionName}>{pane.kind === 'sftp' ? pane.connectionName : 'This Mac'}</span></div><span className="active-label">{active === index ? 'ACTIVE' : ' '}</span></div>
            <PathBar path={pane.path} loading={pane.loading} index={index} navigate={path => void load(index, path)} />
            <div className="pane-tools"><button disabled={pane.loading || !pane.path || pane.path === pane.parent} onClick={() => { void load(index, pane.parent); focusPane(index) }}>↑ Parent</button><button disabled={pane.loading} onClick={() => { void load(index, pane.home); focusPane(index) }}>⌂ Home</button><button disabled={pane.loading} onClick={() => { void load(index, pane.path, true); focusPane(index) }}>↻ Refresh</button>{pane.kind === 'sftp' && <button className="disconnect-button" disabled={pane.loading} onClick={() => void disconnect(index)}>Disconnect</button>}{pane.loading && <span className="loading">Loading…</span>}</div>
            {pane.error && <div className="pane-error" role="alert">{pane.error}</div>}
            <div className="column-head"><span>Name</span><span>Size</span><span>Modified</span></div>
            <div className="file-list" role="listbox" aria-label={`${index === 0 ? 'Left' : 'Right'} directory`} aria-multiselectable="true" aria-busy={pane.loading}>
              <div id={`pane-${index}-row-0`} role="option" aria-selected={false} data-cursor={pane.cursor === 0} className="file-row parent-row" onClick={event => clickRow(index, 0, event)} onDoubleClick={() => void load(index, pane.parent)}><span className="file-name"><span className="file-icon">↰</span><span>..</span><span className="parent-note">Parent directory</span></span><span>—</span><span>—</span></div>
              {pane.entries.map((entry, row) => <FileRow key={entry.name} entry={entry} id={`pane-${index}-row-${row + 1}`} cursor={pane.cursor === row + 1} selected={pane.selected.includes(entry.name)} click={event => clickRow(index, row + 1, event)} open={() => { if (entry.directory) void load(index, entry.path) }} />)}
              {!pane.loading && !pane.entries.length && pane.path && <div className="empty-state">This folder is empty.<span>Copy files here from the other pane.</span></div>}
            </div>
            <footer className="pane-footer"><span>{pane.entries.filter(entry => entry.directory).length} folders · {pane.entries.filter(entry => !entry.directory).length} files</span><strong>{pane.selected.length ? `${pane.selected.length} selected` : 'Cursor selection'}</strong></footer>
          </section>
        ))}
      </div>
      <div className="copy-bar"><div><span className="copy-direction">{active === 0 ? 'LEFT → RIGHT' : 'RIGHT → LEFT'}</span><p>{!localCopy ? 'Copy is available between local folders' : copyCount ? `${copyCount} item${copyCount === 1 ? '' : 's'} into the opposite pane’s folder` : 'Choose a file or folder to copy'}</p></div><button className="copy-button" disabled={!ready || !copyCount} onClick={copySelection}><kbd>F5</kbd>{starting ? 'Preparing…' : running ? 'Copying…' : 'Copy to other pane'}<span aria-hidden="true">→</span></button></div>
      <div className="transfer-panel" aria-live="polite" aria-atomic="true">
        {notice && <p className="notice" role="alert">{notice}</p>}
        {job ? <><div className="transfer-summary"><span className={`status-dot ${running ? 'running' : job.errors.length || job.state === 'failed' ? 'error' : ''}`} /><strong>{running ? 'Copying' : job.state === 'failed' ? 'Copy failed' : job.errors.length ? 'Copy finished with errors' : 'Copy complete'}</strong><span>{job.filesDone} / {job.filesTotal} files · {sizeLabel(job.bytesDone)} written</span></div>{running && <><progress max={Math.max(1, job.filesTotal)} value={job.filesDone} /><p className="current-file">{job.current || 'Preparing files…'}</p></>}{pollError && <p className="notice">{pollError}</p>}{job.skipped.length > 0 && <p className="skipped">Skipped symbolic links: {job.skipped.join(', ')}</p>}{job.errors.length > 0 && <ul className="copy-errors">{job.errors.map((error, index) => <li key={index}><strong>{error.path}</strong>: {error.message}</li>)}</ul>}</> : <p className="idle-message"><span aria-hidden="true">⇄</span>Ready to copy. Symbolic links appear in the list and are skipped during copy.</p>}
      </div>
      <footer className="keyboard-help"><span><kbd>Tab</kbd> Switch pane</span><span><kbd>↑ ↓</kbd> Move cursor</span><span><kbd>Enter</kbd> Open folder</span><span><kbd>⌫</kbd> Parent</span><span><kbd>Space</kbd> Select</span><span><kbd>⌘ click</kbd> Toggle</span><span><kbd>⇧ click</kbd> Range</span></footer>
      {conflict && <dialog ref={dialogRef} className="overwrite-dialog" onCancel={event => { event.preventDefault(); closeConflict() }}>
        <div className="dialog-label">DESTINATION CONFLICT</div><h2>Replace existing items?</h2><p>These names already exist in the destination:</p><ul>{conflict.names.map(name => <li key={name}>{name}</li>)}</ul><p className="destination-path">{conflict.request.to.path}</p><p>Replace overwrites files and merges folders. Symbolic links are skipped or reported as errors.</p><div className="dialog-actions"><button autoFocus disabled={starting} onClick={closeConflict}>Cancel</button><button className="replace-button" disabled={starting} onClick={() => void startCopy({ ...conflict.request, replace: true })}>{starting ? 'Preparing…' : 'Replace'}</button></div>
      </dialog>}
      <ConnectionsDialog open={connectionsOpen} paneName={connectionPane === 0 ? 'Left pane' : 'Right pane'} canConnect={panes[connectionPane].kind === 'local'} close={closeConnections} connected={connected} countChanged={setConnectionCount} />
    </main>
  )
}

function PathBar({ path, loading, index, navigate }: { path: string; loading: boolean; index: number; navigate: (path: string) => void }) {
  const [draft, setDraft] = useState(path)
  useEffect(() => setDraft(path), [path])
  return <form className="path-bar" onSubmit={event => { event.preventDefault(); navigate(draft) }}><span aria-hidden="true">/</span><input aria-label={`${index === 0 ? 'Left' : 'Right'} directory path`} value={draft} placeholder="Loading home directory…" spellCheck={false} onChange={event => setDraft(event.target.value)} onKeyDown={event => { if (event.key === 'Escape') { setDraft(path); event.currentTarget.blur() } }} /><button aria-label={`Open ${index === 0 ? 'left' : 'right'} directory`} disabled={loading || !draft}>↵</button></form>
}

function FileRow({ entry, id, cursor, selected, click, open }: { entry: Entry; id: string; cursor: boolean; selected: boolean; click: (event: MouseEvent) => void; open: () => void }) {
  return <div id={id} role="option" aria-selected={selected} data-cursor={cursor} className={`file-row ${selected ? 'selected' : ''} ${entry.symlink ? 'symlink' : ''}`} onClick={click} onDoubleClick={open}><span className="file-name"><span className={`file-icon ${entry.directory ? 'folder-icon' : ''}`} aria-hidden="true">{entry.symlink ? '↗' : entry.directory ? '▰' : '▤'}</span><span title={entry.name}>{entry.name}</span>{entry.symlink && <span className="link-label">link</span>}</span><span>{entry.directory ? '<DIR>' : sizeLabel(entry.size)}</span><span title={entry.modified}>{dateLabel(entry.modified)}</span></div>
}
