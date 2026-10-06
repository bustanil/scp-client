package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"scp-client/internal/connections"
	"scp-client/internal/session"
	"scp-client/internal/transfer"
)

func hostConnection(t *testing.T, handler http.Handler, fixture *sshFixture, name, directory string) (connections.Record, transfer.Endpoint) {
	t.Helper()
	password := "fixture-password"
	record := savedConnection(t, handler, connections.Input{Record: connections.Record{Name: name, Host: "127.0.0.1", Port: fixture.port, Username: "tester", Auth: "password", StartPath: directory}, Secret: &password})
	return record, connectHostRecord(t, handler, fixture, record.ID)
}

func connectHostRecord(t *testing.T, handler http.Handler, fixture *sshFixture, id string) transfer.Endpoint {
	t.Helper()
	w := call(t, handler, "POST", "/api/sessions", session.Request{ConnectionID: id, TrustFingerprint: ssh.FingerprintSHA256(fixture.signer.PublicKey())})
	if w.Code != http.StatusCreated {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	var connected session.Connected
	if err := json.Unmarshal(w.Body.Bytes(), &connected); err != nil {
		t.Fatal(err)
	}
	return transfer.Endpoint{Kind: "sftp", SessionID: connected.SessionID, Path: connected.Path}
}

func hostPair(t *testing.T) (http.Handler, transfer.Endpoint, transfer.Endpoint, *sshFixture, *sshFixture) {
	t.Helper()
	_, _, handler := fixtureServices(t, t.TempDir(), &memorySecrets{data: map[string]string{}})
	a, b := sshServer(t, t.TempDir(), nil, true), sshServer(t, t.TempDir(), nil, true)
	_, from := hostConnection(t, handler, a, "Host A", "")
	_, to := hostConnection(t, handler, b, "Host B", "")
	return handler, from, to, a, b
}

func TestHostToHostStreamsFilesAndFoldersInBothDirections(t *testing.T) {
	handler, from, to, _, _ := hostPair(t)
	if err := os.MkdirAll(filepath.Join(from.Path, "folder", "nested", "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	content := strings.Repeat("host bytes\x00", 10000)
	writeFile(t, filepath.Join(from.Path, "file.bin"), content)
	writeFile(t, filepath.Join(from.Path, "folder", "nested", "child.txt"), "child")
	writeFile(t, filepath.Join(from.Path, "zero.txt"), "")
	if err := os.Symlink("file.bin", filepath.Join(from.Path, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(from.Path, "folder", "broken")); err != nil {
		t.Fatal(err)
	}
	// A relay must work even when no local temporary directory is available.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "unavailable"))
	from.Names = []string{"folder", "file.bin", "zero.txt", "link"}
	job := waitJob(t, handler, call(t, handler, "POST", "/api/jobs", transfer.Request{From: from, To: to}))
	if job.State != "done" || len(job.Errors) != 0 || job.FilesDone != 3 || job.BytesDone != int64(len(content)+5) || len(job.Skipped) != 2 {
		t.Fatalf("relay: %+v", job)
	}
	expectRemoteFile(t, filepath.Join(to.Path, "file.bin"), content)
	expectRemoteFile(t, filepath.Join(to.Path, "folder", "nested", "child.txt"), "child")
	expectRemoteFile(t, filepath.Join(to.Path, "zero.txt"), "")
	if info, err := os.Stat(filepath.Join(to.Path, "folder", "nested", "empty")); err != nil || !info.IsDir() {
		t.Fatalf("empty folder: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(to.Path, "link")); !os.IsNotExist(err) {
		t.Fatal("relay followed a symbolic link")
	}
	writeFile(t, filepath.Join(to.Path, "return.txt"), "reverse")
	to.Names = []string{"return.txt"}
	job = waitJob(t, handler, call(t, handler, "POST", "/api/jobs", transfer.Request{From: to, To: from}))
	if job.FilesDone != 1 || len(job.Errors) != 0 {
		t.Fatalf("reverse: %+v", job)
	}
	expectRemoteFile(t, filepath.Join(from.Path, "return.txt"), "reverse")
	expectRemoteFile(t, filepath.Join(from.Path, "file.bin"), content)
}

func TestHostToHostOverwritePreflightMergeAndDestinationLinks(t *testing.T) {
	handler, from, to, _, _ := hostPair(t)
	for _, directory := range []string{from.Path, to.Path} {
		if err := os.Mkdir(filepath.Join(directory, "folder"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(from.Path, "new.txt"), "new")
	writeFile(t, filepath.Join(from.Path, "same.txt"), "replacement")
	writeFile(t, filepath.Join(from.Path, "folder", "child.txt"), "child")
	writeFile(t, filepath.Join(to.Path, "same.txt"), "original")
	writeFile(t, filepath.Join(to.Path, "folder", "keep.txt"), "keep")
	from.Names = []string{"new.txt", "same.txt", "folder"}
	input := transfer.Request{From: from, To: to}
	w := call(t, handler, "POST", "/api/jobs", input)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "same.txt") || !strings.Contains(w.Body.String(), "folder") {
		t.Fatalf("preflight: %d %s", w.Code, w.Body.String())
	}
	expectRemoteFile(t, filepath.Join(to.Path, "same.txt"), "original")
	if _, err := os.Stat(filepath.Join(to.Path, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("wrote before confirmation")
	}
	input.Replace = true
	job := waitJob(t, handler, call(t, handler, "POST", "/api/jobs", input))
	if job.FilesDone != 3 || len(job.Errors) != 0 {
		t.Fatalf("replace: %+v", job)
	}
	expectRemoteFile(t, filepath.Join(to.Path, "same.txt"), "replacement")
	expectRemoteFile(t, filepath.Join(to.Path, "folder", "keep.txt"), "keep")
	writeFile(t, filepath.Join(from.Path, "blocked.txt"), "new")
	if err := os.Symlink("same.txt", filepath.Join(to.Path, "blocked.txt")); err != nil {
		t.Fatal(err)
	}
	input.From.Names = []string{"blocked.txt", "new.txt"}
	job = waitJob(t, handler, call(t, handler, "POST", "/api/jobs", input))
	if job.FilesDone != 1 || len(job.Errors) != 1 || !strings.Contains(job.Errors[0].Message, "links are not replaced") {
		t.Fatalf("destination link: %+v", job)
	}
	expectRemoteFile(t, filepath.Join(to.Path, "same.txt"), "replacement")
}

func TestDistinctHostsAllowIdenticalDirectoryPaths(t *testing.T) {
	handler, from, to, _, _ := hostPair(t)
	// Loopback fixtures share a disk. Preflight still distinguishes their SSH endpoints.
	to.Path = from.Path
	writeFile(t, filepath.Join(from.Path, "file.txt"), "data")
	from.Names = []string{"file.txt"}
	w := call(t, handler, "POST", "/api/jobs", transfer.Request{From: from, To: to})
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "exists") {
		t.Fatalf("distinct hosts were treated as one: %d %s", w.Code, w.Body.String())
	}
	expectRemoteFile(t, filepath.Join(from.Path, "file.txt"), "data")
}

func TestSameHostSeparateSessionsCopyAndRejectOverlap(t *testing.T) {
	root, fixture, handler, from := remoteCopyFixture(t, true)
	// A second saved label and a DNS alias still refer to the same SSH endpoint.
	password := "fixture-password"
	record := savedConnection(t, handler, connections.Input{Record: connections.Record{Name: "Same host alias", Host: "localhost", Port: fixture.port, Username: "tester", Auth: "password", StartPath: root}, Secret: &password})
	to := connectHostRecord(t, handler, fixture, record.ID)
	if from.SessionID == to.SessionID {
		t.Fatal("panes shared a session ID")
	}
	from.Path, to.Path = filepath.Join(root, "source"), filepath.Join(root, "destination")
	if err := os.MkdirAll(filepath.Join(from.Path, "folder", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(to.Path, 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(from.Path, "file.txt"), "same host")
	writeFile(t, filepath.Join(from.Path, "folder", "nested", "child.txt"), "child")
	from.Names = []string{"file.txt", "folder"}
	job := waitJob(t, handler, call(t, handler, "POST", "/api/jobs", transfer.Request{From: from, To: to}))
	if job.FilesDone != 2 || len(job.Errors) != 0 {
		t.Fatalf("same host: %+v", job)
	}
	expectRemoteFile(t, filepath.Join(to.Path, "file.txt"), "same host")
	expectRemoteFile(t, filepath.Join(to.Path, "folder", "nested", "child.txt"), "child")
	for _, directory := range []string{from.Path, filepath.Join(from.Path, "folder"), filepath.Join(from.Path, "folder", "nested")} {
		to.Path = directory
		w := call(t, handler, "POST", "/api/jobs", transfer.Request{From: from, To: to, Replace: true})
		if w.Code != http.StatusBadRequest || (!strings.Contains(w.Body.String(), "overlap") && !strings.Contains(w.Body.String(), "different destination")) {
			t.Fatalf("overlap %s: %d %s", directory, w.Code, w.Body.String())
		}
	}
	// Canonicalization must catch two path spellings of the same directory.
	to.Path = from.Path + "/folder/.."
	w := call(t, handler, "POST", "/api/jobs", transfer.Request{From: from, To: to, Replace: true})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "different destination") {
		t.Fatalf("canonical overlap: %d %s", w.Code, w.Body.String())
	}
	// This fixture's RealPath leaves links unresolved. Such a base must also be rejected.
	if err := os.Symlink(from.Path, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	to.Path = filepath.Join(root, "alias")
	w = call(t, handler, "POST", "/api/jobs", transfer.Request{From: from, To: to, Replace: true})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unresolved base link: %d %s", w.Code, w.Body.String())
	}
	expectRemoteFile(t, filepath.Join(from.Path, "file.txt"), "same host")
}

func TestTwoSessionsKeepConnectionInUseUntilBothDisconnect(t *testing.T) {
	root := t.TempDir()
	fixture := sshServer(t, root, nil, true)
	_, _, handler := fixtureServices(t, t.TempDir(), &memorySecrets{data: map[string]string{}})
	record, a := hostConnection(t, handler, fixture, "Shared connection", "")
	b := connectHostRecord(t, handler, fixture, record.ID)
	update := connections.Input{Record: record}
	update.Name, update.Host, update.Port = "Edited connection", "example.invalid", 22
	w := call(t, handler, "PUT", "/api/connections/"+record.ID, update)
	if w.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", w.Code, w.Body.String())
	}
	for _, endpoint := range []transfer.Endpoint{a, b} {
		w = call(t, handler, "GET", "/api/list?kind=sftp&sessionId="+endpoint.SessionID+"&path="+url.QueryEscape(endpoint.Path), nil)
		if w.Code != http.StatusOK {
			t.Fatalf("edit ended a live session: %d %s", w.Code, w.Body.String())
		}
	}
	for _, endpoint := range []transfer.Endpoint{a, b} {
		w = call(t, handler, "DELETE", "/api/connections/"+record.ID, nil)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "in_use") {
			t.Fatalf("delete live record: %d %s", w.Code, w.Body.String())
		}
		call(t, handler, "DELETE", "/api/sessions/"+endpoint.SessionID, nil)
	}
	w = call(t, handler, "DELETE", "/api/connections/"+record.ID, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete disconnected record: %d %s", w.Code, w.Body.String())
	}
}

func TestDroppedSourceOrDestinationFailsHostToHostJob(t *testing.T) {
	for _, side := range []string{"source", "destination"} {
		t.Run(side, func(t *testing.T) {
			handler, from, to, sourceHost, destinationHost := hostPair(t)
			file, err := os.Create(filepath.Join(from.Path, "large.bin"))
			if err != nil {
				t.Fatal(err)
			}
			if err := file.Truncate(128 << 20); err != nil {
				t.Fatal(err)
			}
			file.Close()
			writeFile(t, filepath.Join(to.Path, "large.bin"), "original")
			from.Names = []string{"large.bin"}
			w := call(t, handler, "POST", "/api/jobs", transfer.Request{From: from, To: to, Replace: true})
			job := waitHostProgress(t, handler, w)
			fixture := sourceHost
			if side == "destination" {
				fixture = destinationHost
			}
			fixture.mu.Lock()
			for _, network := range fixture.connections {
				network.Close()
			}
			fixture.mu.Unlock()
			job = waitJob(t, handler, w)
			if job.State != "failed" || job.FilesDone != 0 || len(job.Errors) != 1 || !strings.Contains(job.Errors[0].Message, "SSH session ended") {
				t.Fatalf("dropped %s: %+v", side, job)
			}
			expectRemoteFile(t, filepath.Join(to.Path, "large.bin"), "original")
			survivor := to
			if side == "destination" {
				survivor = from
			}
			w = call(t, handler, "GET", "/api/list?kind=sftp&sessionId="+survivor.SessionID+"&path="+url.QueryEscape(survivor.Path), nil)
			if w.Code != http.StatusOK {
				t.Fatalf("other host session lost: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func waitHostProgress(t *testing.T, handler http.Handler, response *httptest.ResponseRecorder) transfer.Job {
	t.Helper()
	if response.Code != http.StatusAccepted {
		t.Fatalf("start: %d %s", response.Code, response.Body.String())
	}
	var job transfer.Job
	if err := json.Unmarshal(response.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w := call(t, handler, "GET", "/api/jobs/"+job.ID, nil)
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if job.BytesDone > 0 && job.State == "running" {
			return job
		}
		if job.State != "running" {
			t.Fatalf("job ended before connection drop: %+v", job)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("copy made no progress")
	return job
}
