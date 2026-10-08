# scp-client product spec

A Norton Commander style file manager for one person on a Mac. Two directories stay on screen. Each one is a local folder or a saved SSH host. Copy goes from the active pane into the other pane’s current folder.

The packaged app is the way to open it. Browser mode is the same workspace on `127.0.0.1:8787`.

## What you can do

- Browse two folders at once. Each pane has its own path, cursor, and selection.
- Save SSH connections and open them again after a restart.
- Connect one pane, or both. The same saved host can be open twice, in different directories. Each pane has its own session.
- Copy files and folders in either direction: local to local, this Mac to a host, a host to this Mac, or one host to another.
- Trust a host key on first connect. A later key change blocks the connection.
- Run the Apple Silicon app without installing Go or Node.js. Closing the window leaves it in the Dock. Cmd-Q stops the file service and SSH sessions. A second launch focuses the existing window.

## Window

One screen, two equal panes, a connection editor, and a copy status panel.

| Action | Result |
| --- | --- |
| Click a pane | That pane becomes active. |
| Tab | Switch the active pane. |
| Up / Down | Move the cursor in the active pane. |
| Enter | Open the cursor’s directory. |
| Backspace | Go to the parent directory. |
| Space | Toggle the cursor row in the selection. |
| Shift-click | Select a range. |
| Cmd-click | Toggle one row without clearing the rest. |
| F5, or Copy to other pane | Copy the selection into the other pane’s current directory. |
| Parent, Home, Refresh | Leave the directory, open that pane’s home, or reload the current directory. |
| Path bar, then Enter | Open an absolute path in that pane. |
| Esc | Close the connection editor or the overwrite prompt. |

Each row shows the name, size, and modified time. Directories show `<DIR>` and a folder mark. Directories sort above files. Names sort case-insensitively. Symbolic links are marked.

The active pane has a visible focus ring. The other pane stays visible and keeps its path and selection.

Both panes open in the home directory. Reloading the page, or reopening a closed desktop window, returns both panes to that home directory. Saved connections stay.

## Connections

| Field | Required | Notes |
| --- | --- | --- |
| Name | yes | Label in the connection list. |
| Host | yes | DNS name or IP. No path in this field. |
| Port | yes | Default 22. |
| Username | yes | |
| Start path | no | Directory opened on connect. Empty means the server default. |
| Auth | yes | Password, or a private-key file path. |

The user can add, edit, and delete a connection. The list is available as soon as the app opens.

A password or key passphrase can be saved in the macOS Keychain under the service `scp-client`, or typed only for that connect. Saved secrets are not shown again. Editing a connection keeps the stored secret unless a new one is typed. Changing the authentication type or the private-key path clears the previous secret unless a replacement is provided.

Connect applies to the active pane. Disconnect returns that pane to the local folder it had before connect. The other pane is unchanged. Editing a saved connection does not close a live session. Deleting a connection is refused while either pane is using it.

A failed login shows the error and leaves the pane on its previous local folder. A changed host key stops the connection and does not offer a one-click override.

Records stay in `~/.scp-client/connections.json`. Trusted keys stay in `~/.scp-client/known_hosts`. The private key stays in the file the user named. The connection file stores the path, not the key. `SCP_CLIENT_DATA_DIR` selects a different configuration directory.

## Copy

F5 copies every selected file and directory. If nothing is explicitly selected, F5 copies the cursor row. The parent row cannot be copied. The copy bar shows the direction: left to right or right to left, and whether the job is a local copy, an upload, a download, or host to host.

- Local folder to local folder.
- This Mac to a connected host, and the reverse.
- One connected host to another, including two directories on the same host.

Host-to-host copies stream through this Mac. They do not leave a saved intermediate file and they do not run a shell command on either server.

When a destination name already exists, the app asks once per job: Replace or Cancel. Replace overwrites files and merges into existing directories. On the same location, a copy whose source and destination directories overlap is refused. For two SSH sessions, that check uses the server’s IP address and port, so two saved names for the same endpoint still count as one place. The same path on this Mac and on a host is allowed.

A copy reports the current file name, files finished, files total, and bytes written. When the job ends, both panes refresh. A failed file is listed by path. The rest of the job continues. A dropped SSH session stops the job. Files already copied stay in place. The job does not resume.

Symbolic links are listed and are not copied. Each skipped link is named. A link at the destination is reported as an error. On this Mac, a destination file appears only after that file has been copied in full. An interrupted local copy leaves the previous file in place.

File permissions are copied when the destination allows it. A failure to set permissions does not fail the file. When the SSH server supports the OpenSSH hard-link or atomic-rename extension, a remote copy uses a hidden `.scp-client-*` temporary file and then publishes it. Otherwise the copy writes the destination directly. On those servers, an interrupted replacement can leave a partial file, and a lost connection can leave the temporary file behind.

Jobs stay in memory for the current run. The most recent 100 completed jobs can still be read until the app quits.

## Host trust

The first connection to a host shows the key type and fingerprint and asks Trust or Cancel. Trust stores the key and connects. Later connections to that host and port succeed only when the key matches.

## Desktop app

The Apple Silicon app bundles the file service and the workspace. It chooses a free localhost port, so it can run beside browser mode. The Dock icon is the teal scp-client mark.

The build is ad-hoc signed. That checks the app was not altered after packaging. A downloaded copy still needs Developer ID signing and notarization before Gatekeeper opens it under the default settings. An Intel build can be produced. Intel execution is not verified.

GitHub Actions builds the Apple Silicon disk image on `main`, on tags that start with `v`, on pull requests to `main`, and on a manual run. The artifact includes the disk image and its SHA-256 checksum, and expires after 14 days. Those builds are also unsigned for Gatekeeper.

## Security the user can see

- The file service accepts connections only from this Mac.
- Desktop requests must carry the startup token created for that launch.
- A saved secret never appears in the connection list or in the connection file.
- A private key stays in the file the user pointed at.

## Not included

Move, delete, rename, mkdir, drag-and-drop, a transfer queue that survives quitting, resume, sync, chmod, opening a file in an editor, importing `~/.ssh/config`, an SSH agent, FTP, cloud storage, and sharing this app with another user or over the network.
