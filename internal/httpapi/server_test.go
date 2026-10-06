package httpapi

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"scp-client/internal/location"
	"scp-client/internal/transfer"
)

func call(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	r := httptest.NewRequest(method, "http://127.0.0.1:8787"+path, bytes.NewReader(data))
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0640); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func waitJob(t *testing.T, handler http.Handler, response *httptest.ResponseRecorder) transfer.Job {
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
		if w.Code != http.StatusOK {
			t.Fatalf("poll: %d %s", w.Code, w.Body.String())
		}
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatal(err)
		}
		if job.State != "running" {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("job did not complete")
	return job
}

func copyRequest(source, dest string, names ...string) transfer.Request {
	return transfer.Request{From: transfer.Endpoint{Kind: "local", Path: source, Names: names}, To: transfer.Endpoint{Kind: "local", Path: dest}}
}

func TestLocalListAndHome(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "Zoo"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "alpha"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(home, "Beta.txt"), "test")
	writeFile(t, filepath.Join(home, "a.txt"), "file")
	if err := os.Symlink("Zoo", filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	handler := New(home, nil)
	w := call(t, handler, "GET", "/api/list?kind=local", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %s", w.Body.String())
	}
	var result struct {
		Path, Parent, Home string
		Entries            []location.Entry
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Path != home || result.Home != home || result.Parent != filepath.Dir(home) {
		t.Fatalf("paths: %+v", result)
	}
	want := []string{"alpha", "Zoo", "a.txt", "Beta.txt", "link"}
	for i, name := range want {
		if result.Entries[i].Name != name {
			t.Fatalf("order: %+v", result.Entries)
		}
	}
	if !result.Entries[4].Symlink || result.Entries[4].Directory {
		t.Fatalf("link: %+v", result.Entries[4])
	}
	for _, query := range []string{"kind=sftp", "kind=local&path=relative", "kind=local&sessionId=remote"} {
		if w := call(t, handler, "GET", "/api/list?"+query, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("accepted %s", query)
		}
	}
}

func TestCopyFileFolderAndLinks(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "folder", "nested", "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "hello.txt"), "hello")
	writeFile(t, filepath.Join(source, "folder", "nested", "child.txt"), "child")
	if err := os.Symlink("hello.txt", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(source, "folder", "broken")); err != nil {
		t.Fatal(err)
	}
	handler := New(source, nil)
	job := waitJob(t, handler, call(t, handler, "POST", "/api/jobs", copyRequest(source, dest, "hello.txt", "folder", "link")))
	if job.State != "done" || job.FilesTotal != 2 || job.FilesDone != 2 || job.BytesDone != 10 || len(job.Errors) != 0 {
		t.Fatalf("job: %+v", job)
	}
	if len(job.Skipped) != 2 || job.Skipped[0] != filepath.Join("folder", "broken") || job.Skipped[1] != "link" {
		t.Fatalf("skipped: %+v", job.Skipped)
	}
	if readFile(t, filepath.Join(dest, "hello.txt")) != "hello" || readFile(t, filepath.Join(dest, "folder", "nested", "child.txt")) != "child" {
		t.Fatal("copy content differs")
	}
	if info, err := os.Stat(filepath.Join(dest, "folder", "nested", "empty")); err != nil || !info.IsDir() {
		t.Fatal("empty directory not copied")
	}
	if _, err := os.Lstat(filepath.Join(dest, "link")); !os.IsNotExist(err) {
		t.Fatal("link was copied")
	}
	info, err := os.Stat(filepath.Join(dest, "hello.txt"))
	if err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("mode: %v %v", info, err)
	}
}

func TestOverwritePreflightAndMerge(t *testing.T) {
	source, dest := t.TempDir(), t.TempDir()
	for _, dir := range []string{source, dest} {
		if err := os.Mkdir(filepath.Join(dir, "folder"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, filepath.Join(source, "a.txt"), "new")
	writeFile(t, filepath.Join(source, "b.txt"), "second")
	writeFile(t, filepath.Join(source, "folder", "child.txt"), "merged")
	writeFile(t, filepath.Join(dest, "a.txt"), "original")
	writeFile(t, filepath.Join(dest, "folder", "keep.txt"), "keep")
	handler := New(source, nil)
	req := copyRequest(source, dest, "a.txt", "b.txt", "folder")
	w := call(t, handler, "POST", "/api/jobs", req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected conflict: %d %s", w.Code, w.Body.String())
	}
	var conflict struct {
		Code  string
		Names []string
	}
	if err := json.Unmarshal(w.Body.Bytes(), &conflict); err != nil {
		t.Fatal(err)
	}
	if conflict.Code != "exists" || len(conflict.Names) != 2 {
		t.Fatalf("conflict: %+v", conflict)
	}
	if readFile(t, filepath.Join(dest, "a.txt")) != "original" {
		t.Fatal("preflight changed existing file")
	}
	if _, err := os.Stat(filepath.Join(dest, "b.txt")); !os.IsNotExist(err) {
		t.Fatal("preflight started a partial job")
	}
	req.Replace = true
	job := waitJob(t, handler, call(t, handler, "POST", "/api/jobs", req))
	if len(job.Errors) != 0 || job.FilesDone != 3 {
		t.Fatalf("job: %+v", job)
	}
	if readFile(t, filepath.Join(dest, "a.txt")) != "new" || readFile(t, filepath.Join(dest, "folder", "child.txt")) != "merged" || readFile(t, filepath.Join(dest, "folder", "keep.txt")) != "keep" {
		t.Fatal("replace or merge failed")
	}
}

func TestCopyRejectsOverlapAndInvalidNames(t *testing.T) {
	source := t.TempDir()
	dest := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "folder", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "a.txt"), "untouched")
	alias := filepath.Join(dest, "alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	handler := New(source, nil)
	cases := []transfer.Request{
		copyRequest(source, source, "a.txt"),
		copyRequest(source, alias, "a.txt"),
		copyRequest(source, filepath.Join(source, "folder", "nested"), "folder"),
		copyRequest(source, dest, "../a.txt"),
		copyRequest(source, dest, ".."),
		copyRequest(source, dest, "missing"),
	}
	for _, req := range cases {
		if w := call(t, handler, "POST", "/api/jobs", req); w.Code != http.StatusBadRequest {
			t.Fatalf("accepted %+v: %d %s", req, w.Code, w.Body.String())
		}
	}
	if readFile(t, filepath.Join(source, "a.txt")) != "untouched" {
		t.Fatal("source modified")
	}
}

func TestDestinationLinksFailWithoutStoppingOtherFiles(t *testing.T) {
	source, dest, outside := t.TempDir(), t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "folder"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "a.txt"), "new")
	writeFile(t, filepath.Join(source, "b.txt"), "okay")
	writeFile(t, filepath.Join(source, "folder", "child.txt"), "escape")
	writeFile(t, filepath.Join(outside, "a.txt"), "original")
	if err := os.Symlink(filepath.Join(outside, "a.txt"), filepath.Join(dest, "a.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dest, "folder")); err != nil {
		t.Fatal(err)
	}
	req := copyRequest(source, dest, "a.txt", "folder", "b.txt")
	req.Replace = true
	handler := New(source, nil)
	job := waitJob(t, handler, call(t, handler, "POST", "/api/jobs", req))
	if job.State != "done" || job.FilesDone != 1 || len(job.Errors) != 3 {
		t.Fatalf("job: %+v", job)
	}
	if readFile(t, filepath.Join(dest, "b.txt")) != "okay" || readFile(t, filepath.Join(outside, "a.txt")) != "original" {
		t.Fatal("link target changed or job did not continue")
	}
	if _, err := os.Stat(filepath.Join(outside, "child.txt")); !os.IsNotExist(err) {
		t.Fatal("wrote through destination directory link")
	}
}

func TestCopyRejectsDestinationAncestor(t *testing.T) {
	base := t.TempDir()
	source := filepath.Join(base, "folder", "nested")
	if err := os.MkdirAll(filepath.Join(source, "folder"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "folder", "a.txt"), "untouched")
	handler := New(source, nil)
	// Copying this selection would merge into an ancestor of its own source.
	w := call(t, handler, "POST", "/api/jobs", copyRequest(source, base, "folder"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("accepted overlapping directories: %d %s", w.Code, w.Body.String())
	}
	if readFile(t, filepath.Join(source, "folder", "a.txt")) != "untouched" {
		t.Fatal("source modified")
	}
}

func TestLocalRequestProtection(t *testing.T) {
	handler := New(t.TempDir(), nil)
	for _, scenario := range []struct{ host, origin, site string }{
		{"evil.example:8787", "", ""},
		{"127.0.0.1:8787", "https://evil.example", ""},
		{"127.0.0.1:8787", "null", ""},
		{"127.0.0.1:8787", "", "cross-site"},
	} {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8787/api/list?kind=local", nil)
		r.Host = scenario.host
		r.Header.Set("Origin", scenario.origin)
		r.Header.Set("Sec-Fetch-Site", scenario.site)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("accepted %+v: %d", scenario, w.Code)
		}
	}
	if w := call(t, handler, "POST", "/api/jobs", nil); w.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("accepted non-JSON: %d", w.Code)
	}
	if w := call(t, handler, "GET", "/api/jobs/missing", nil); w.Code != http.StatusNotFound {
		t.Fatal("missing job not 404")
	}
	if w := call(t, handler, "GET", "/api/list?kind=local&path="+url.QueryEscape("/does-not-exist"), nil); w.Code != http.StatusBadRequest {
		t.Fatal("invalid path accepted")
	}
}

func TestPartialFileAbortPreservesExistingFile(t *testing.T) {
	dest, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dest, "original.txt")
	writeFile(t, path, "original")
	writer, err := (location.Local{Base: dest}).Create(path, fs.FileMode(0644), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("partial")); err != nil {
		t.Fatal(err)
	}
	if readFile(t, path) != "original" {
		t.Fatal("destination changed before completion")
	}
	if err := writer.(interface{ Abort() error }).Abort(); err != nil {
		t.Fatal(err)
	}
	if readFile(t, path) != "original" {
		t.Fatal("destination changed after abort")
	}
	children, err := os.ReadDir(dest)
	if err != nil || len(children) != 1 {
		t.Fatalf("temporary file remains: %v %v", children, err)
	}
}
