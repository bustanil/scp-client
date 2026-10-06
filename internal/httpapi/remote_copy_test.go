package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"scp-client/internal/connections"
	"scp-client/internal/session"
	"scp-client/internal/transfer"
)

func remoteCopyFixture(t *testing.T, writable bool) (string, *sshFixture, http.Handler, transfer.Endpoint) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	fixture := sshServer(t, root, nil, writable)
	_, _, handler := fixtureServices(t, t.TempDir(), &memorySecrets{data: map[string]string{}})
	password := "fixture-password"
	record := savedConnection(t, handler, connections.Input{Record: connections.Record{Name: "Copy host", Host: "127.0.0.1", Port: fixture.port, Username: "tester", Auth: "password"}, Secret: &password})
	w := call(t, handler, "POST", "/api/sessions", session.Request{ConnectionID: record.ID, TrustFingerprint: ssh.FingerprintSHA256(fixture.signer.PublicKey())})
	if w.Code != http.StatusCreated {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	var connected session.Connected
	if err := json.Unmarshal(w.Body.Bytes(), &connected); err != nil {
		t.Fatal(err)
	}
	return root, fixture, handler, transfer.Endpoint{Kind: "sftp", SessionID: connected.SessionID, Path: root}
}

func TestUploadAndDownloadFilesAndNestedDirectories(t *testing.T) {
	root, _, handler, remote := remoteCopyFixture(t, true)
	source, destination := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "folder", "nested", "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "hello.txt"), "upload bytes")
	writeFile(t, filepath.Join(source, "folder", "nested", "child.txt"), "nested bytes")
	writeFile(t, filepath.Join(source, "zero.txt"), "")
	if err := os.Symlink("missing", filepath.Join(source, "folder", "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("hello.txt", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	input := transfer.Request{From: transfer.Endpoint{Kind: "local", Path: source, Names: []string{"folder", "hello.txt", "zero.txt", "link"}}, To: remote}
	job := waitJob(t, handler, call(t, handler, "POST", "/api/jobs", input))
	if job.State != "done" || len(job.Errors) != 0 || job.FilesDone != 3 || job.BytesDone != 24 || len(job.Skipped) != 2 {
		t.Fatalf("upload: %+v", job)
	}
	expectRemoteFile(t, filepath.Join(root, "folder", "nested", "child.txt"), "nested bytes")
	expectRemoteFile(t, filepath.Join(root, "hello.txt"), "upload bytes")
	if info, err := os.Stat(filepath.Join(root, "folder", "nested", "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty folder: %v", err)
	}
	if err := os.Symlink("hello.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(root, "folder", "broken")); err != nil {
		t.Fatal(err)
	}
	remote.Names = input.From.Names
	input = transfer.Request{From: remote, To: transfer.Endpoint{Kind: "local", Path: destination}}
	job = waitJob(t, handler, call(t, handler, "POST", "/api/jobs", input))
	if job.State != "done" || len(job.Errors) != 0 || job.FilesDone != 3 || job.BytesDone != 24 || len(job.Skipped) != 2 {
		t.Fatalf("download: %+v", job)
	}
	expectRemoteFile(t, filepath.Join(destination, "hello.txt"), "upload bytes")
	expectRemoteFile(t, filepath.Join(destination, "folder", "nested", "child.txt"), "nested bytes")
	expectRemoteFile(t, filepath.Join(destination, "zero.txt"), "")
	if _, err := os.Lstat(filepath.Join(destination, "link")); !os.IsNotExist(err) {
		t.Fatal("download followed a link")
	}
}

func TestRemoteCopyOverwritePreflightAndFolderMerge(t *testing.T) {
	for _, upload := range []bool{true, false} {
		t.Run(map[bool]string{true: "upload", false: "download"}[upload], func(t *testing.T) {
			root, _, handler, remote := remoteCopyFixture(t, true)
			local := t.TempDir()
			source, destination := local, root
			input := transfer.Request{From: transfer.Endpoint{Kind: "local", Path: local}, To: remote}
			if !upload {
				source, destination = root, local
				input.From, input.To = remote, input.From
			}
			for _, directory := range []string{source, destination} {
				if err := os.Mkdir(filepath.Join(directory, "folder"), 0755); err != nil {
					t.Fatal(err)
				}
			}
			writeFile(t, filepath.Join(source, "same.txt"), "replacement")
			writeFile(t, filepath.Join(source, "new.txt"), "new")
			writeFile(t, filepath.Join(source, "folder", "child.txt"), "new child")
			writeFile(t, filepath.Join(destination, "same.txt"), "original")
			writeFile(t, filepath.Join(destination, "folder", "keep.txt"), "retained")
			input.From.Names = []string{"new.txt", "same.txt", "folder"}
			w := call(t, handler, "POST", "/api/jobs", input)
			if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "same.txt") || !strings.Contains(w.Body.String(), "folder") {
				t.Fatalf("conflict: %d %s", w.Code, w.Body.String())
			}
			expectRemoteFile(t, filepath.Join(destination, "same.txt"), "original")
			if _, err := os.Stat(filepath.Join(destination, "new.txt")); !os.IsNotExist(err) {
				t.Fatal("preflight wrote an unconfirmed file")
			}
			input.Replace = true
			job := waitJob(t, handler, call(t, handler, "POST", "/api/jobs", input))
			if job.FilesDone != 3 || len(job.Errors) != 0 {
				t.Fatalf("replace: %+v", job)
			}
			expectRemoteFile(t, filepath.Join(destination, "same.txt"), "replacement")
			expectRemoteFile(t, filepath.Join(destination, "folder", "child.txt"), "new child")
			expectRemoteFile(t, filepath.Join(destination, "folder", "keep.txt"), "retained")
		})
	}
}

func TestRemoteCopyRejectsDestinationLinksAndContinuesFiles(t *testing.T) {
	root, _, handler, remote := remoteCopyFixture(t, true)
	source, outside := t.TempDir(), t.TempDir()
	writeFile(t, filepath.Join(source, "blocked.txt"), "new")
	writeFile(t, filepath.Join(source, "okay.txt"), "okay")
	writeFile(t, filepath.Join(outside, "keep.txt"), "original")
	if err := os.Mkdir(filepath.Join(source, "folder"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "folder", "keep.txt"), "new")
	if err := os.Symlink(filepath.Join(outside, "keep.txt"), filepath.Join(root, "blocked.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "folder")); err != nil {
		t.Fatal(err)
	}
	job := waitJob(t, handler, call(t, handler, "POST", "/api/jobs", transfer.Request{From: transfer.Endpoint{Kind: "local", Path: source, Names: []string{"blocked.txt", "folder", "okay.txt"}}, To: remote, Replace: true}))
	if job.State != "done" || job.FilesDone != 1 || len(job.Errors) != 3 {
		t.Fatalf("links: %+v", job)
	}
	expectRemoteFile(t, filepath.Join(outside, "keep.txt"), "original")
	expectRemoteFile(t, filepath.Join(root, "okay.txt"), "okay")
}

func TestRemoteCopyPermissionFailureAndUnavailableSession(t *testing.T) {
	_, _, handler, remote := remoteCopyFixture(t, false)
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "file.txt"), "data")
	input := transfer.Request{From: transfer.Endpoint{Kind: "local", Path: source, Names: []string{"file.txt"}}, To: remote}
	job := waitJob(t, handler, call(t, handler, "POST", "/api/jobs", input))
	if job.FilesDone != 0 || len(job.Errors) != 1 || !strings.Contains(strings.ToLower(job.Errors[0].Message), "permission denied") {
		t.Fatalf("permission: %+v", job)
	}
	call(t, handler, "DELETE", "/api/sessions/"+remote.SessionID, nil)
	w := call(t, handler, "POST", "/api/jobs", input)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "SSH session ended") {
		t.Fatalf("dead session: %d %s", w.Code, w.Body.String())
	}
}

func TestUploadToServerWithoutAtomicExtensions(t *testing.T) {
	// This package runs its tests serially. Extension negotiation finishes at connect.
	if err := sftp.SetSFTPExtensions(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sftp.SetSFTPExtensions("hardlink@openssh.com", "posix-rename@openssh.com", "statvfs@openssh.com"); err != nil {
			t.Error(err)
		}
	})
	root, _, handler, remote := remoteCopyFixture(t, true)
	source := t.TempDir()
	writeFile(t, filepath.Join(source, "file.txt"), "initial")
	input := transfer.Request{From: transfer.Endpoint{Kind: "local", Path: source, Names: []string{"file.txt"}}, To: remote}
	job := waitJob(t, handler, call(t, handler, "POST", "/api/jobs", input))
	if job.FilesDone != 1 || len(job.Errors) != 0 {
		t.Fatalf("create: %+v", job)
	}
	expectRemoteFile(t, filepath.Join(root, "file.txt"), "initial")
	writeFile(t, filepath.Join(source, "file.txt"), "replacement")
	input.Replace = true
	job = waitJob(t, handler, call(t, handler, "POST", "/api/jobs", input))
	if job.FilesDone != 1 || len(job.Errors) != 0 {
		t.Fatalf("replace: %+v", job)
	}
	expectRemoteFile(t, filepath.Join(root, "file.txt"), "replacement")
}

func TestDroppedSSHConnectionFailsUploadAndDownload(t *testing.T) {
	for _, upload := range []bool{true, false} {
		t.Run(map[bool]string{true: "upload", false: "download"}[upload], func(t *testing.T) {
			root, fixture, handler, remote := remoteCopyFixture(t, true)
			local := t.TempDir()
			source, destination := local, root
			input := transfer.Request{From: transfer.Endpoint{Kind: "local", Path: local}, To: remote, Replace: true}
			if !upload {
				source, destination = root, local
				input.From, input.To = remote, input.From
			}
			file, err := os.Create(filepath.Join(source, "large.bin"))
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(128 << 20); err != nil {
				t.Fatal(err)
			}
			file.Close()
			writeFile(t, filepath.Join(destination, "large.bin"), "original")
			input.From.Names = []string{"large.bin"}
			w := call(t, handler, "POST", "/api/jobs", input)
			if w.Code != http.StatusAccepted {
				t.Fatalf("start: %d %s", w.Code, w.Body.String())
			}
			var job transfer.Job
			json.Unmarshal(w.Body.Bytes(), &job)
			deadline := time.Now().Add(5 * time.Second)
			for {
				progress := call(t, handler, "GET", "/api/jobs/"+job.ID, nil)
				json.Unmarshal(progress.Body.Bytes(), &job)
				if job.BytesDone > 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("copy made no progress")
				}
				time.Sleep(time.Millisecond)
			}
			fixture.mu.Lock()
			for _, network := range fixture.connections {
				network.Close()
			}
			fixture.mu.Unlock()
			job = waitJob(t, handler, w)
			if job.State != "failed" || len(job.Errors) != 1 || !strings.Contains(job.Errors[0].Message, "SSH session ended") || job.FilesDone != 0 {
				t.Fatalf("dropped: %+v", job)
			}
			expectRemoteFile(t, filepath.Join(destination, "large.bin"), "original")
		})
	}
}

func expectRemoteFile(t *testing.T, filename, want string) {
	t.Helper()
	if got := readFile(t, filename); got != want {
		t.Fatalf("%s: got %q, want %q", filename, got, want)
	}
}
