# Vertical slices

Each slice is one user-visible path through the UI and the API. Later slices build on earlier ones. Do not start a slice by finishing every backend package in advance.

Dependencies run in order: S1, then S2, then S3, then S4.

## S1 — Two local panes, copy between them

Status: implemented. Go API checks and browser scenarios verify the slice. See the README for run and verification commands.

The app opens on two panes of the home directory. The user can move around both and F5 a file and a folder to the other side.

Includes:

- Go process on `127.0.0.1:8787`, React shell with two panes, active pane, path bar.
- `GET /api/list` for `kind=local` only.
- Keyboard and mouse behavior from the product spec, except connect.
- `POST /api/jobs` and `GET /api/jobs/{id}` when both sides are local.
- Overwrite prompt (409 `exists`, then `replace: true`).
- Progress line and a refresh of both panes when the job ends.
- Symlinks listed and skipped.

Done when a file and a nested folder copy between two local directories, an existing name asks before replace, and a symlink is skipped with its name shown.

## S2 — Save a connection and browse the host

Status: implemented. Real loopback SSH/SFTP tests cover password and private-key hosts, trust, auth failures, changed keys, and persisted connections. A macOS Keychain round trip and browser connection scenarios verify the remaining behavior. See the README for verification commands.

The user adds a host, trusts the key, and that host fills the active pane. After a restart the connection is still in the list and can connect again.

Includes:

- Connection editor: add, edit, delete, list.
- `connections.json` without secrets. Password or key passphrase in the keychain.
- Private-key path stored on the connection. Prompt when the key is encrypted and nothing is saved yet.
- `POST /api/sessions` and `DELETE /api/sessions/{id}`.
- Unknown-host dialog (412) and mismatch message (409) with no trust button.
- Auth failure (401) leaves the pane on its previous local path.
- `GET /api/list` for `kind=sftp`.
- Disconnect restores the local path that pane had before connect.

Done when a password host and a private-key host each open in one pane, the other pane stays local, a restart still shows the connections, and the JSON file contains no password.

## S3 — Copy between this Mac and a host

Status: not started.

F5 uploads the local selection and downloads the remote selection.

Includes:

- The same job API. `from` and `to` may differ in `kind`.
- Recursive directories, overwrite prompt, progress, skip symlinks.
- A dropped session fails the job with a clear error.

Done when a file and a folder upload, and a file and a folder download, into the directory the other pane is showing.

## S4 — Two hosts, copy between them

Status: not started.

Both panes are connected. F5 copies from one host to the other. Two panes on the same host, different directories, also copy.

Includes:

- A second session at the same time as the first.
- Transfer still uses `Open` then `Create` in the Go process.
- Editing or deleting a connection does not drop a live session until disconnect. Delete of a connection that still has a session is refused with `code: in_use`.

Done when a file copied from host A shows up in host B’s pane, and a copy between two directories on one host shows up without using the local disk as a saved file.

## After v1

Not scheduled here: move, delete, rename, mkdir, SSH agent, `~/.ssh/config`, resume, and a durable transfer queue.
