const { app, BrowserWindow, dialog, Menu, session } = require('electron')
const { mkdirSync } = require('node:fs')
const path = require('node:path')
const { Backend } = require('./backend.cjs')

if (process.env.SCP_CLIENT_ELECTRON_DATA_DIR) {
  mkdirSync(process.env.SCP_CLIENT_ELECTRON_DATA_DIR, { recursive: true })
  app.setPath('userData', process.env.SCP_CLIENT_ELECTRON_DATA_DIR)
}

let backend
let window
let windowSession
let quitting = false
let exitAllowed = false

function createWindow() {
  if (window && !window.isDestroyed()) { window.show(); window.focus(); return }
  const createdWindow = new BrowserWindow({
    width: 1440, height: 940, minWidth: 760, minHeight: 600,
    title: 'scp-client', backgroundColor: '#f3f6f9', show: false,
    webPreferences: { session: windowSession, sandbox: true, contextIsolation: true, nodeIntegration: false, devTools: !app.isPackaged },
  })
  window = createdWindow
  createdWindow.once('ready-to-show', () => { if (!quitting && !createdWindow.isDestroyed()) createdWindow.show() })
  createdWindow.on('closed', () => { if (window === createdWindow) window = null })
  createdWindow.webContents.setWindowOpenHandler(() => ({ action: 'deny' }))
  const ownPage = target => {
    try { return new URL(target).origin === backend.url } catch { return false }
  }
  createdWindow.webContents.on('will-navigate', (event, target) => { if (!ownPage(target)) event.preventDefault() })
  createdWindow.webContents.on('will-redirect', (event, target) => { if (!ownPage(target)) event.preventDefault() })
  createdWindow.webContents.on('render-process-gone', () => {
    if (!quitting) { dialog.showErrorBox('scp-client closed unexpectedly', 'Restart the app to reopen the file workspace.'); app.quit() }
  })
  void createdWindow.loadURL(backend.url).catch(error => {
    if (!quitting && !createdWindow.isDestroyed()) { dialog.showErrorBox('Cannot open scp-client', error.message); app.quit() }
  })
}

async function start() {
  const binary = app.isPackaged
    ? path.join(process.resourcesPath, 'backend', 'scp-client')
    : path.join(__dirname, 'build', 'backend', 'scp-client')
  backend = new Backend(binary)
  let ready = false
  backend.on('exit', () => {
    if (ready && !backend.stopping && !quitting) {
      dialog.showErrorBox('The file service stopped', 'Restart scp-client to reconnect. Files already copied remain in place.')
      app.quit()
    }
  })
  await backend.start()
  if (quitting) return
  ready = true
  windowSession = session.fromPartition('scp-client-desktop')
  windowSession.setPermissionRequestHandler((_contents, _permission, callback) => callback(false))
  windowSession.setPermissionCheckHandler(() => false)
  windowSession.webRequest.onBeforeRequest((details, callback) => {
    // The workspace needs only its owned localhost service and embedded assets.
    callback({ cancel: new URL(details.url).origin !== backend.url })
  })
  windowSession.webRequest.onBeforeSendHeaders({ urls: [`${backend.url}/*`] }, (details, callback) => {
    callback({ requestHeaders: { ...details.requestHeaders, ...backend.headers() } })
  })
  Menu.setApplicationMenu(Menu.buildFromTemplate([
    { label: 'scp-client', submenu: [{ role: 'about' }, { type: 'separator' }, { role: 'hide' }, { role: 'hideOthers' }, { role: 'unhide' }, { type: 'separator' }, { role: 'quit' }] },
    { label: 'File', submenu: [{ role: 'close' }] },
    { label: 'Edit', submenu: [{ role: 'undo' }, { role: 'redo' }, { type: 'separator' }, { role: 'cut' }, { role: 'copy' }, { role: 'paste' }, { role: 'selectAll' }] },
    { label: 'View', submenu: [{ role: 'resetZoom' }, { role: 'zoomIn' }, { role: 'zoomOut' }, { type: 'separator' }, { role: 'togglefullscreen' }] },
    { role: 'windowMenu' },
  ]))
  createWindow()
}

if (!app.requestSingleInstanceLock()) {
  app.exit(0)
} else {
  app.on('second-instance', () => { if (window) { if (window.isMinimized()) window.restore(); window.show(); window.focus() } })
  app.on('activate', () => { if (windowSession && !quitting) createWindow() })
  app.on('before-quit', event => {
    if (exitAllowed || !backend) return
    event.preventDefault()
    if (quitting) return
    quitting = true
    void backend.stop().finally(() => { exitAllowed = true; app.quit() })
  })
  app.on('window-all-closed', () => { if (process.platform !== 'darwin') app.quit() })
  void app.whenReady().then(start).catch(error => {
    if (!quitting) { dialog.showErrorBox('Cannot start scp-client', error.message); app.quit() }
  })
}
