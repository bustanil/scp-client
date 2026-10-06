# scp-client architecture

Go serves a local HTTP API and the built React app. The browser is a two-pane commander. SSH and SFTP stay in Go.

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

Dev runs Vite on its own port and proxies `/api` to Go. A production run is one Go process: it serves `web/dist` and the API on `127.0.0.1:8787`.

## Why these pieces

| Piece | Choice | Role |
| --- | --- | --- |
| HTTP | `net/http` | Connections, listing, sessions, copy jobs. |
| SSH | `golang.org/x/crypto/ssh` | Dial, authenticate, host key check. |
| SFTP | `github.com/pkg/sftp` v1.13.11 | `sftp.NewClient` on that SSH connection. List, read, write. |
| Secrets | `github.com/zalando/go-keyring` | Service `scp-client`, account is the connection id. |
| UI | React + Vite | Two instances of one pane component. Plain CSS. |

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
```

## One location type

Both panes talk to the same interface. A pane is either a local path or a live session id.

```text
List(path) ([]Entry, error)
Stat(path) (Entry, error)
Open(path) (io.ReadCloser, error)
Create(path, mode, replace) (io.WriteCloser, error)
MkdirAll(path) error
```

`Local` uses the `os` package. `Remote` uses `*sftp.Client`. Transfer code copies from `Open` to `Create` and does not branch on which side is local.

S2 handles remote directory listings in `internal/session`. A remote implementation of the copy interface arrives in S3. Copy jobs remain local-only until that slice.

S1 implements `Local`. It writes each file to a temporary file in the destination directory, then publishes the completed copy. `replace=false` prevents a destination name created after preflight from being overwritten. A failed copy discards its temporary file. Destination links are errors. Source and destination directory trees must not overlap.

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

`state` is `running`, `done`, or `failed`. `failed` means the job stopped before walking (bad path, dead session). A per-file error is appended to `errors` and the walk continues. The UI polls `GET /api/jobs/{id}` about three times a second until the state is terminal.

## Copy walk

1. Resolve each selected name under `from.path`.
2. Skip symlinks and record them.
3. For a directory, `MkdirAll` the destination, then walk children.
4. For a file, `Open` the source and `Create` the destination, then `io.Copy`.
5. Best effort: set the destination mode from the source mode. A failure to chmod does not fail the file.

Same-host SFTP still goes through this process. There is no server-side `cp`.

## Pane state

The browser holds both panes: kind, session id, path, cursor index, selection, and the local directory used before connecting. Refreshing the page returns both panes to the user's home directory. On `pagehide`, the browser sends a keepalive DELETE for each session. This cleanup is best effort; a browser crash can leave a session open until process exit. Saved connections remain.

## Security

- `Listen` is `127.0.0.1`, not `0.0.0.0`.
- No CORS for other origins.
- Local requests require a localhost Host header. The API accepts the app origins on port 8787 and the Vite development origins on port 5173. It rejects other browser origins and cross-site requests.
- Connection file mode `0600`, directory mode `0700`.
- Host-key mismatch never writes `known_hosts`.
- Local list and copy use the OS user’s permissions. The app does not add a second sandbox. That is intentional: the local pane is this Mac’s disk.

## Build and run

```text
go run ./cmd/scp-client
```

Run `npm --prefix web ci` and `npm --prefix web run build` before starting Go for production. The binary embeds `web/dist` at compilation time. Without a frontend build, Go serves the API and a build instruction at `/`. In dev, Vite proxies `/api` to `:8787`.
