# scp-client architecture

Go serves a local HTTP API and the built React app. Electron displays the two-pane workspace in the packaged app. Browser mode remains available. SSH and SFTP stay in Go.

## System

```text
Browser (React, Vite)
  JSON over HTTP
  127.0.0.1 only
Go process
  connection file          ~/.scp-client/connections.json
  host keys                ~/.scp-client/known_hosts
  secrets                  macOS keychain via go-keyring
  sessions                 in memory, one SSH+SFTP client per connected pane
  local disk               os package
```

Dev runs Vite on its own port and proxies `/api` to Go. Browser mode uses one Go process: it serves `web/dist` and the API on `127.0.0.1:8787`.

Desktop mode starts the bundled Go executable with `--desktop`. Go binds `127.0.0.1:0`, then prints its assigned URL as a JSON readiness message. Electron verifies the URL and polls `/api/health` before opening the window. A random token travels through the child environment and Electron attaches it to requests. The renderer does not receive Node.js APIs or a preload bridge. Its session blocks requests and navigation outside the owned service and denies browser permission requests.

Electron keeps one app instance per user profile. Closing the window leaves the service running on macOS. Dock activation reopens the window. Cmd-Q sends SIGTERM, waits up to five seconds, and terminates an unresponsive child. Go shuts down HTTP and closes SSH sessions. Startup and unexpected process failures show an error and quit the app.

Electron Forge packages the main process into an ASAR application archive. The Go executable stays in `Contents/Resources/backend`, where Electron can execute it directly. Go embeds the React build. The resulting app includes the Electron runtime and requires no external Node.js or Go installation.

## Why these pieces

| Piece | Choice | Role |
| --- | --- | --- |
| HTTP | `net/http` | Connections, listing, sessions, copy jobs. |
| SSH | `golang.org/x/crypto/ssh` | Dial, authenticate, host key check. |
| SFTP | `github.com/pkg/sftp` v1.13.11 | `sftp.NewClient` on that SSH connection. List, read, write. |
| Secrets | `github.com/zalando/go-keyring` | Service `scp-client`, account is the connection id. |
| UI | React + Vite | Two instances of one pane component. Plain CSS. |
| Desktop | Electron + Electron Forge | Window, service lifecycle, app bundle, disk image. |

Directory listings use the SFTP subsystem. The legacy `scp` command cannot list a directory, so the file panes speak SFTP.

## Layout

```text
cmd/scp-client/main.go      listen address, wiring
internal/httpapi/           routes and JSON
internal/connections/       connection file
internal/secrets/           keychain wrapper
internal/knownhosts/        OpenSSH known_hosts
internal/location/          List, Open, Create, MkdirAll, Stat
internal/session/           SSH dial and SFTP client pool
internal/transfer/          copy job
web/                        React app
desktop/                    Electron main process, packaging, desktop tests
```

## One location type

Both panes talk to the same interface. A pane is either a local path or a live session id.

```text
List(path) ([]Entry, error)
Stat(path) (Entry, error)
Open(path) (io.ReadCloser, error)
Create(path, mode, replace) (io.WriteCloser, error)
MkdirAll(path) error
Identity() string
```

`Local` uses the `os` package. `Remote` uses `*sftp.Client`. Transfer code copies from `Open` to `Create` and does not branch on which side is local.

Remote directory browsing stays in `internal/session`. `location.Remote` implements the copy interface and binds each job to an existing live SSH session and a canonical remote directory. Jobs support local-to-local, upload, download, and host-to-host copy. Each pane has its own session, including two panes using one saved connection.

`Identity` distinguishes path namespaces for overlap checks. Local locations share one identity. Remote identity uses the live TCP peer's IP address and port. Two session IDs or saved labels on the same endpoint still share an identity, including DNS aliases that resolve to that endpoint. Editing a saved record does not change this identity. Remote overlap checks conservatively assume accounts on one endpoint share the absolute path namespace. This can reject matching paths in separate account chroots. Different endpoints may use identical directory paths.

S1 implements `Local`. It writes each file to a temporary file in the destination directory, then publishes the completed copy. `replace=false` prevents a destination name created after preflight from being overwritten. A failed copy discards its temporary file. Destination links are errors. Source and destination directory trees must not overlap.

`Remote` checks directory components and rejects source and destination links. For new uploads, `hardlink@openssh.com` publishes a temporary file without replacing an existing name. For replacements, `posix-rename@openssh.com` publishes the complete file atomically. Without the required extension, it opens the destination directly with exclusive creation or truncation. An interrupted direct replacement can leave a partial destination file. A dropped connection can leave a hidden upload temporary file because cleanup requires a live session. Permission changes are best effort.

An `Entry` is name, path, directory bit, symlink bit, size, mode, and mtime.

## Connection record

`connections.json` holds this and nothing secret:

```json
{
  "id": "uuid",
  "name": "prod web",
  "host": "example.com",
  "port": 22,
  "username": "deploy",
  "startPath": "/var/www",
  "auth": "password",
  "privateKeyPath": ""
}
```

`auth` is `password` or `privateKey`. The keychain item is the password, or the passphrase of an encrypted key. A missing item does not establish whether the key is encrypted. The app parses the file and prompts if it requires a passphrase. The API never returns secret fields. Changing auth type or private-key path clears the previous secret unless a replacement is supplied.

## Sessions

`POST /api/sessions` opens a TCP connection with `net.Dialer`, then uses `ssh.NewClientConn` and `sftp.NewClient`. TCP dial has a 10-second timeout. SSH handshake and initial SFTP listing have a 15-second deadline. Remote listing requests have a 15-second timeout. The host-key callback reads `known_hosts`.

| Result | HTTP | Body |
| --- | --- | --- |
| Known key, login ok | 201 | `{ "sessionId", "path" }` |
| Unknown key | 412 | `{ "code": "unknown_host_key", "keyType", "fingerprint" }` |
| Key mismatch | 409 | `{ "code": "host_key_changed" }` |
| Auth failed | 401 | `{ "code": "auth_failed", "message" }` |
| Secret needed | 428 | `{ "code": "secret_required" }` or `{ "code": "passphrase_required" }` |

Trust is a second `POST /api/sessions` with the same connection id and `trustFingerprint` set to the fingerprint from the 412. The host-key callback writes the unknown key only when the fingerprint matches the key presented during that handshake. It then allows that handshake to continue. A mismatched known key is rejected even when `trustFingerprint` is supplied.

The request can include an optional `secret` and `saveSecret`. A prompted secret is saved only after login and the initial listing succeed. A success response includes `sessionId`, `connectionId`, `name`, `path`, `parent`, `home`, and `entries`. Private-key files must be regular files no larger than 1 MiB.

A session lives until `DELETE /api/sessions/{id}` or process exit. Two panes may hold two sessions, including two sessions to the same host.

## HTTP API

All bodies are JSON. Errors use `{ "code", "message" }`.

| Method | Path | Purpose |
| --- | --- | --- |
| GET | `/api/connections` | List saved connections, no secrets. |
| POST | `/api/connections` | Create. Optional `secret` is written to the keychain and dropped. |
| PUT | `/api/connections/{id}` | Update. A missing `secret` keeps the stored one. |
| DELETE | `/api/connections/{id}` | Delete the record and the keychain item. |
| POST | `/api/sessions` | Connect the active pane. |
| DELETE | `/api/sessions/{id}` | Disconnect. |
| GET | `/api/list?kind=local\|sftp&sessionId=&path=` | Directory listing. |
| GET | `/api/health` | Startup readiness check. |
| POST | `/api/jobs` | Start a copy. |
| GET | `/api/jobs/{id}` | Progress. |

List path for `kind=local` is an absolute path on this Mac. For `kind=sftp` it is a path on that session. The server rejects a relative path and a `sessionId` that is not in memory.

In S1, an empty local listing path opens the user's home directory. The listing response includes `path`, `parent`, `home`, and `entries`. Paths resolve directory aliases before listing or copying. Invalid local copy requests return 400 before a job starts. An accepted job returns 202.

Copy body:

```json
{
  "from": { "kind": "local", "sessionId": "", "path": "/Users/me/src", "names": ["a.txt", "dir"] },
  "to": { "kind": "sftp", "sessionId": "…", "path": "/var/www" },
  "replace": false
}
```

If a destination name exists and `replace` is false, the job does not start. The response is 409 `{ "code": "exists", "names": ["a.txt"] }`. The UI asks, then posts again with `replace: true`.

Job payload:

```json
{
  "id": "…",
  "state": "running",
  "filesTotal": 4,
  "filesDone": 1,
  "bytesDone": 1024,
  "current": "dir/b.txt",
  "skipped": ["link"],
  "errors": []
}
```

`state` is `running`, `done`, or `failed`. Invalid paths and unavailable sessions during preparation reject the request without starting a job. A session lost during transfer appends a reconnect error and sets `state` to `failed`. Other per-file errors are appended to `errors` and the copy continues. The UI polls `GET /api/jobs/{id}` about three times a second until the state is terminal.

## Copy walk

1. Resolve each selected name under `from.path`.
2. Skip symlinks and record them.
3. For a directory, `MkdirAll` the destination, then walk children.
4. For a file, `Open` the source and `Create` the destination, then `io.Copy`.
5. Best effort: set the destination mode from the source mode. A failure to chmod does not fail the file.

Host-to-host transfers use this same process. Data streams through Go without a saved local copy. There is no server-side `cp`. Loss of either session stops the job. The other session remains available.

## Pane state

The browser holds both panes: kind, session id, path, cursor index, selection, and the local directory used before connecting. Refreshing the page returns both panes to the user's home directory. On `pagehide`, the browser sends a keepalive DELETE for each session. This cleanup is best effort; a browser crash can leave a session open until process exit. Saved connections remain.

## Security

- `Listen` is `127.0.0.1`, not `0.0.0.0`.
- No CORS for other origins.
- Local requests require a Host header matching the assigned listener or its localhost alias. Browser mode accepts app origins on port 8787 and Vite development origins on port 5173. Desktop mode accepts its assigned origin and requires `X-SCP-Desktop-Token` on every request. Both reject other browser origins and cross-site requests.
- The desktop renderer enables Chromium sandboxing and context isolation and disables Node.js integration. Go retains the macOS user's file permissions.
- Connection file mode `0600`, directory mode `0700`.
- Host-key mismatch never writes `known_hosts`.
- Local list and copy use the OS user’s permissions. The app does not add a second sandbox. That is intentional: the local pane is this Mac’s disk.

## Build and run

```text
go run ./cmd/scp-client
```

Run `npm --prefix web ci` and `npm --prefix web run build` before starting Go for production. The binary embeds `web/dist` at compilation time. Without a frontend build, Go serves the API and a build instruction at `/`. In dev, Vite proxies `/api` to `:8787`.

Run `npm --prefix desktop ci` and `npm --prefix desktop run make` to build the macOS app and disk image. The build script compiles React, cross-compiles Go for the selected macOS processor, and generates the icon before packaging. See the README for output paths, verification commands, and signing limitations.
