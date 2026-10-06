# scp-client product spec

A local, Norton Commander style file manager for SSH hosts. One person uses it on their own Mac. The app listens only on localhost.

## Problem

Copying files to and from SSH servers means either a single-pane client or a terminal. This app keeps two directories on screen and copies between them.

## Users

One operator on macOS, with one or more SSH accounts (password or private key).

## v1 outcomes

1. Set up a connection once and have it still be there after a restart.
2. See two file panes at once. Each pane is a local folder or a connected host.
3. Copy the selection from the active pane into the other pane’s current folder, in either direction.

## Commander behavior

The window is one screen with two equal panes and a connection editor.

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
| F5 | Copy the selection into the other pane’s current directory. |
| Esc | Close the connection editor or the overwrite prompt. |

Each pane shows the current path, a parent row, and one row per entry: name, size, modified time, and whether it is a directory. Directories sort above files. Names sort case-insensitively.

The active pane has a visible focus ring. The other pane stays visible and keeps its own path and selection.

## Connections

A connection has:

| Field | Required | Notes |
| --- | --- | --- |
| Name | yes | Label in the connection list. |
| Host | yes | DNS name or IP. No path in this field. |
| Port | yes | Default 22. |
| Username | yes | |
| Start path | no | Directory opened on connect. Empty means the server default. |
| Auth | yes | Password, or a private-key file path. |

The user can add, edit, and delete a connection. The list is available as soon as the app opens.

Passwords and saved key passphrases are not shown again after save. Editing a connection leaves the stored secret in place unless the user types a new one.

Connect applies to the active pane. Disconnect returns that pane to the local folder it had before connect. The other pane is unchanged.

Failed login shows the server’s auth error and leaves the pane local. A changed host key stops the connection and tells the user the key no longer matches. It does not offer a one-click override.

## Copy

F5 copies every selected file and directory. If nothing is explicitly selected, F5 copies the cursor row.

- Local folder to local folder.
- Local to a connected host, and the reverse.
- One connected host to another, including two directories on the same host.

When a destination name already exists, the app asks once per job: Replace or Cancel. Replace overwrites files and merges into existing directories.

A copy reports the current file name, files finished, files total, and bytes written. When the job ends, both panes refresh. A failed file is listed by path. The rest of the job continues.

Symbolic links are listed and are not copied. The job records each skipped link.

## Host trust

The first connection to a host shows the key type and fingerprint and asks Trust or Cancel. Trust stores the key and connects. Later connections to that host and port succeed only when the key matches.

## Security the user can see

- The server accepts connections only from this Mac.
- A saved connection’s password never appears in the connection list or in the connection file.
- A private key stays in the file the user pointed at. The app stores the path.

## Out of scope for v1

Move, delete, rename, mkdir, drag-and-drop, a transfer queue across restarts, resume, sync, chmod, opening files in an editor, `~/.ssh/config` import, SSH agent, FTP, SFTP servers other than SSH subsystem SFTP, cloud storage, and a multi-user or remote deployment of this app.

## Done for v1

A restart still lists every saved connection. The user can put a local folder on one side and a host on the other, or two hosts, and F5 a file and a folder onto the opposite side. An unknown host asks before trusting. A wrong password does not open the pane.
