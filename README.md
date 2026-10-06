# scp-client

A two-pane file manager for macOS. S1 supports local directories and copy jobs. SSH connections arrive in S2.

## Run

You need Go 1.25 or later and a Node.js version supported by Vite 8. The implementation is verified with Go 1.27.1 and Node.js 26.8.1.

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

## Copy

1. Open the source directory in one pane and the destination directory in the other.
2. Click a source row to place the cursor. Press Space to toggle selection. Use Cmd-click to toggle more rows or Shift-click to select a range.
3. Press F5 or click **Copy to other pane**. Without an explicit selection, F5 copies the cursor row.
4. If names already exist, choose **Replace** or **Cancel**. Esc cancels the prompt. Replace overwrites files and merges directories.

Tab switches panes. Up and Down move the cursor. Enter opens a directory. Backspace opens its parent.

The status panel shows the current file, files completed, total files, bytes written, skipped links, and errors. Both panes refresh when the job finishes. A failed file does not stop the remaining files.

Symbolic links appear in listings and are skipped during copy. Destination links are reported as errors. Regular files become visible after a complete copy. Failed copies remove their temporary files and preserve existing destination files. Directories cannot be copied into overlapping source or destination trees.

The app listens on `127.0.0.1:8787`. It runs with your macOS account's file permissions. It accepts the localhost app and development origins and rejects other browser origins. Jobs stay in memory and disappear when Go stops. The most recent 100 completed jobs remain available during a run.

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

## Plan

- [Product specification](docs/product-spec.md)
- [Architecture](docs/architecture.md)
- [Vertical slices](docs/slices.md)
