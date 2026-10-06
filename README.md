# scp-client

A two-pane file manager for macOS. Browse local folders and saved SSH connections. Copy files and folders between local directories, between this Mac and a host, or between two hosts.

## Desktop app

The Electron app bundles the Go service and the built React UI. You do not need Go, Node.js, or a terminal to run the packaged app. It uses the same saved connections, trusted host keys, and macOS Keychain items as browser mode.

Build on macOS with Go, Node.js, and the Xcode command line tools installed:

```sh
npm --prefix web ci
npm --prefix desktop ci
npm --prefix desktop run make
```

The build creates these Apple Silicon artifacts:

- `desktop/out/scp-client-darwin-arm64/scp-client.app`
- `desktop/out/make/dmg/arm64/scp-client.dmg`

Open the disk image and drag `scp-client.app` into Applications. Open the app to start browsing. Use **scp-client > Quit scp-client** or Cmd-Q to stop its file service and SSH sessions. Closing the window keeps the app in the Dock. Click its Dock icon to reopen the workspace.

The app starts its own service on a free localhost port. It can run beside browser mode on port 8787. Opening another copy focuses the existing app window. Reopening a closed window starts both panes at your home directory; saved connections remain.

The current build has no Developer ID signing or notarization. Configure signing and notarization before distributing a release to other Macs. Apple Silicon packaging and execution are verified. The build supports `npm --prefix desktop run make -- --arch=x64` for Intel, but Intel execution is not verified.

For desktop development:

```sh
npm --prefix desktop start
```

### GitHub Actions builds

The [Build macOS DMG workflow](.github/workflows/build-dmg.yml) builds an Apple Silicon installer on pushes to `main`, tags beginning with `v`, pull requests targeting `main`, and manual runs. It checks the Go backend and desktop service lifecycle, verifies the disk image, and uploads the installer and SHA-256 checksum.

Open the repository's **Actions** tab, select a successful **Build macOS DMG** run, and download **scp-client-macos-arm64** under **Artifacts**. Extract the archive to get `scp-client.dmg` and `scp-client.dmg.sha256`. Artifacts expire after 14 days. These builds have no Developer ID signing or notarization.

To build manually, select **Build macOS DMG > Run workflow**. To verify a downloaded installer, run this command in the extracted directory:

```sh
shasum -a 256 -c scp-client.dmg.sha256
```

## Browser mode

You need Go 1.26 or later and a Node.js version supported by Vite 8. The implementation is verified with Go 1.27.1 and Node.js 26.8.1.

Run these commands from the project directory:

```sh
npm --prefix web ci
npm --prefix web run build
go run ./cmd/scp-client
```

Open <http://127.0.0.1:8787>. Both panes start in your home directory. Enter an absolute directory path in either path bar. Press Enter to open it.

Build the frontend before starting Go. Go embeds the frontend at compilation time. Restart Go after rebuilding the frontend.

For development, run Go in one terminal and Vite in another:

```sh
go run ./cmd/scp-client
```

```sh
npm --prefix web run dev
```

Open <http://127.0.0.1:5173>. Vite forwards `/api` to the Go server.

## SSH connections

1. Select the pane where you want to browse the host.
2. Click **Connections**, then **Add connection**.
3. Enter a name, host, port, and username. Port defaults to 22. Leave the start path empty to open the server's default directory, or enter an absolute remote path.
4. Choose password or private-key authentication. For a key, enter the absolute path to its file on your Mac. Enter a password or passphrase now, or supply it when connecting.
5. Save the connection, then click **Connect**. On first use, verify the key fingerprint with the server before choosing **Trust and connect**.

The active pane shows the remote directory. The other pane keeps its path and selection. **Disconnect** returns the connected pane to its previous local folder. A failed login leaves the local pane in place. A changed host key blocks the connection and offers no override.

The connection editor lets you edit and delete saved hosts. Leave the secret field blank to keep the saved secret. Changing the authentication type or private-key path clears the previous secret unless you provide a replacement. You must disconnect a live connection before deleting it. Editing a record does not close an existing session.

Records stay in `~/.scp-client/connections.json`. Trusted keys stay in `~/.scp-client/known_hosts`. Passwords and saved key passphrases stay in macOS Keychain under service `scp-client`. A prompted secret can be used without saving it. The private key stays in its original file. Set `SCP_CLIENT_DATA_DIR` to use a different configuration directory.

Refresh returns both panes to local home directories. The browser requests session cleanup when leaving the page. If the browser exits without sending that request, the session remains until the Go process stops. Saved connections and trusted host keys survive a restart.

Connect one pane to upload or download. Connect both panes to copy between hosts. You can connect the same saved host in both panes and open different directories. Each pane has its own session. Disconnecting one pane leaves the other connected. The copy bar shows the direction.

Host-to-host copies stream through the Go process on your Mac. They do not save an intermediate local file or execute a shell command on either server. Editing a saved connection does not change either live session. Deleting it remains blocked while any pane uses it.

## Copy

1. Open the source directory in one pane and the destination directory in the other.
2. Click a source row to place the cursor. Press Space to toggle selection. Use Cmd-click to toggle more rows or Shift-click to select a range.
3. Press F5 or click **Copy to other pane**. Without an explicit selection, F5 copies the cursor row.
4. If names already exist, choose **Replace** or **Cancel**. Esc cancels the prompt. Replace overwrites files and merges directories.

Tab switches panes. Up and Down move the cursor. Enter opens a directory. Backspace opens its parent.

The status panel shows the current file, files completed, total files, bytes written, skipped links, and errors. Both panes refresh when the job finishes. A failed file does not stop the remaining files. A dropped SSH session stops the job and shows a reconnect message. Completed files remain in place; jobs do not resume after reconnecting.

Symbolic links appear in listings and are skipped during copy. Destination links are reported as errors. Local destinations publish regular files only after a complete copy and preserve existing files when a copy fails. On the same location, source and destination directories cannot overlap. Remote overlap checks compare the live SSH endpoint's IP address and port across sessions, including different saved labels and DNS names that resolve to that endpoint. These checks assume that accounts on the same endpoint share the absolute path namespace. Identical path names on separate endpoints, or on this Mac and a host, are allowed.

Remote destinations use temporary files when the server supports the OpenSSH hard-link extension for new files or the atomic-rename extension for replacements. Otherwise copies write directly to the destination. On those servers, an interrupted replacement can leave a partial destination file. A lost connection can also prevent removal of a hidden `.scp-client-*` temporary file. File permissions are copied on a best-effort basis.

Browser mode listens on `127.0.0.1:8787`. Desktop mode chooses a free port on `127.0.0.1` and requires a startup token attached by Electron. The app runs with your macOS account's file permissions. It accepts its own localhost origin and rejects other browser origins. Browser mode also accepts the Vite development origin. Jobs stay in memory and disappear when Go stops. The most recent 100 completed jobs remain available during a run.

## Verify

```sh
go test -race ./...
go vet ./...
npm --prefix web run build
cd web
npx playwright install chromium
npm run test:e2e
```

Browser tests start and stop their own Go server. Port 8787 must be free before you run them. They use temporary directories and remove them afterward.

Backend integration tests use real loopback SSH/SFTP servers with password and private-key authentication. They verify upload, download, host-to-host copy in both directions, same-host copying and overlap checks, recursive folders, overwrite conflicts, links, permission failures, extension compatibility, and dropped connections. A host-to-host copy succeeds with the local temporary directory unavailable. Connection lifecycle tests verify two sessions on one record and independent disconnects. Browser tests verify local copy, connection editing, pane behavior, login dialogs, and remote copy requests and status. Browser SSH scenarios use controlled API responses. Connection-editing tests use the production storage API in an isolated configuration directory.

On macOS, run the real Keychain check with a disposable test item:

```sh
SCP_CLIENT_TEST_KEYCHAIN=1 go test ./internal/secrets -v
```

The check removes its test item afterward.

Verify the desktop service controller and Electron window:

```sh
npm --prefix desktop run build
npm --prefix desktop test
npm --prefix desktop run test:e2e
```

Verify the packaged Apple Silicon app, including a disposable Keychain item:

```sh
SCP_CLIENT_TEST_KEYCHAIN=1 \
SCP_CLIENT_APP_EXECUTABLE="$PWD/desktop/out/scp-client-darwin-arm64/scp-client.app/Contents/MacOS/scp-client" \
npm --prefix desktop run test:e2e
```

Desktop tests use isolated connection and Electron profile directories. They verify real file and folder copies, overwrite confirmation, connection persistence, window reopening, single-instance behavior, and service shutdown. Packaged tests launch with a system-only `PATH` to verify that Go and Node.js installations are not needed. The optional Keychain test creates and removes only its own item. Service tests verify startup failures, timeout cleanup, independent ports, and token enforcement.

## Plan

- [Product specification](docs/product-spec.md)
- [Architecture](docs/architecture.md)
- [Vertical slices](docs/slices.md)
