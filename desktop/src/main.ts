import { app, BrowserWindow, dialog, Menu, nativeImage, session, type MenuItemConstructorOptions, type Session } from 'electron'
import { mkdirSync } from 'node:fs'
import path from 'node:path'
import { Backend } from './backend.js'

if (process.env.SCP_CLIENT_ELECTRON_DATA_DIR) {
  mkdirSync(process.env.SCP_CLIENT_ELECTRON_DATA_DIR, { recursive: true })
  app.setPath('userData', process.env.SCP_CLIENT_ELECTRON_DATA_DIR)
}

let backend: Backend | undefined
let mainWindow: BrowserWindow | null = null
let windowSession: Session | undefined
let quitting = false
let exitAllowed = false

function useAppIcon(): void {
  if (app.isPackaged || process.platform !== 'darwin' || !app.dock) return
  for (const candidate of [path.join(__dirname, '..', 'build', 'icon.icns'), path.join(__dirname, '..', 'build', 'icon.iconset', 'icon_512x512@2x.png')]) {
    const icon = nativeImage.createFromPath(candidate)
    if (icon.isEmpty()) continue
    app.dock.setIcon(icon)
    return
  }
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : 'Unknown error'
}

function createWindow(service: Backend): void {
  if (mainWindow && !mainWindow.isDestroyed()) {
    mainWindow.show()
    mainWindow.focus()
    return
  }
  const createdWindow = new BrowserWindow({
    width: 1440,
    height: 940,
    minWidth: 760,
    minHeight: 600,
    title: 'scp-client',
    backgroundColor: '#f3f6f9',
    show: false,
    webPreferences: {
      session: windowSession,
      sandbox: true,
      contextIsolation: true,
      nodeIntegration: false,
      devTools: !app.isPackaged,
    },
  })
  mainWindow = createdWindow
  createdWindow.once('ready-to-show', () => {
    if (!quitting && !createdWindow.isDestroyed()) createdWindow.show()
  })
  createdWindow.on('closed', () => {
    if (mainWindow === createdWindow) mainWindow = null
  })
  createdWindow.webContents.setWindowOpenHandler(() => ({ action: 'deny' }))
  const ownPage = (target: string): boolean => {
    try {
      return new URL(target).origin === service.url
    } catch {
      return false
    }
  }
  createdWindow.webContents.on('will-navigate', (event, target) => {
    if (!ownPage(target)) event.preventDefault()
  })
  createdWindow.webContents.on('will-redirect', (event, target) => {
    if (!ownPage(target)) event.preventDefault()
  })
  createdWindow.webContents.on('render-process-gone', () => {
    if (!quitting) {
      dialog.showErrorBox('scp-client closed unexpectedly', 'Restart the app to reopen the file workspace.')
      app.quit()
    }
  })
  void createdWindow.loadURL(service.url).catch((error: unknown) => {
    if (!quitting && !createdWindow.isDestroyed()) {
      dialog.showErrorBox('Cannot open scp-client', errorText(error))
      app.quit()
    }
  })
}

async function start(): Promise<void> {
  useAppIcon()
  const binary = app.isPackaged
    ? path.join(process.resourcesPath, 'backend', 'scp-client')
    : path.join(__dirname, '..', 'build', 'backend', 'scp-client')
  const service = new Backend(binary)
  backend = service
  let ready = false
  service.on('exit', () => {
    if (ready && !service.stopping && !quitting) {
      dialog.showErrorBox('The file service stopped', 'Restart scp-client to reconnect. Files already copied remain in place.')
      app.quit()
    }
  })
  await service.start()
  if (quitting) return
  ready = true
  windowSession = session.fromPartition('scp-client-desktop')
  windowSession.setPermissionRequestHandler((_contents, _permission, callback) => callback(false))
  windowSession.setPermissionCheckHandler(() => false)
  windowSession.webRequest.onBeforeRequest((details, callback) => {
    callback({ cancel: new URL(details.url).origin !== service.url })
  })
  windowSession.webRequest.onBeforeSendHeaders({ urls: [`${service.url}/*`] }, (details, callback) => {
    callback({ requestHeaders: { ...details.requestHeaders, ...service.headers() } })
  })
  const menu: MenuItemConstructorOptions[] = [
    { label: 'scp-client', submenu: [{ role: 'about' }, { type: 'separator' }, { role: 'hide' }, { role: 'hideOthers' }, { role: 'unhide' }, { type: 'separator' }, { role: 'quit' }] },
    { label: 'File', submenu: [{ role: 'close' }] },
    { label: 'Edit', submenu: [{ role: 'undo' }, { role: 'redo' }, { type: 'separator' }, { role: 'cut' }, { role: 'copy' }, { role: 'paste' }, { role: 'selectAll' }] },
    { label: 'View', submenu: [{ role: 'resetZoom' }, { role: 'zoomIn' }, { role: 'zoomOut' }, { type: 'separator' }, { role: 'togglefullscreen' }] },
    { role: 'windowMenu' },
  ]
  Menu.setApplicationMenu(Menu.buildFromTemplate(menu))
  createWindow(service)
}

if (!app.requestSingleInstanceLock()) {
  app.exit(0)
} else {
  app.on('second-instance', () => {
    if (!mainWindow) return
    if (mainWindow.isMinimized()) mainWindow.restore()
    mainWindow.show()
    mainWindow.focus()
  })
  app.on('activate', () => {
    if (windowSession && !quitting && backend) createWindow(backend)
  })
  app.on('before-quit', event => {
    if (exitAllowed || !backend) return
    event.preventDefault()
    if (quitting) return
    quitting = true
    void backend.stop().finally(() => {
      exitAllowed = true
      app.quit()
    })
  })
  app.on('window-all-closed', () => {
    if (process.platform !== 'darwin') app.quit()
  })
  void app.whenReady().then(start).catch((error: unknown) => {
    if (!quitting) {
      dialog.showErrorBox('Cannot start scp-client', errorText(error))
      app.quit()
    }
  })
}
