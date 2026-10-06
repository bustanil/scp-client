package transfer

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"

	"scp-client/internal/location"
)

type Endpoint struct {
	Kind      string   `json:"kind"`
	SessionID string   `json:"sessionId"`
	Path      string   `json:"path"`
	Names     []string `json:"names,omitempty"`
}

type Request struct {
	From    Endpoint `json:"from"`
	To      Endpoint `json:"to"`
	Replace bool     `json:"replace"`
}

type FileError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

type Job struct {
	ID         string      `json:"id"`
	State      string      `json:"state"`
	FilesTotal int         `json:"filesTotal"`
	FilesDone  int         `json:"filesDone"`
	BytesDone  int64       `json:"bytesDone"`
	Current    string      `json:"current"`
	Skipped    []string    `json:"skipped"`
	Errors     []FileError `json:"errors"`
}

type Conflict struct {
	Names []string
}

func (e *Conflict) Error() string { return "destination names already exist" }

type Manager struct {
	mu    sync.RWMutex
	jobs  map[string]*Job
	order []string
}

func NewManager() *Manager { return &Manager{jobs: make(map[string]*Job)} }

type item struct {
	rel  string
	info location.Entry
}

func (m *Manager) Start(req Request) (Job, error) {
	if req.From.Kind != "local" || req.To.Kind != "local" || req.From.SessionID != "" || req.To.SessionID != "" {
		return Job{}, errors.New("S1 supports local locations only")
	}
	source, err := location.Directory(req.From.Path)
	if err != nil {
		return Job{}, fmt.Errorf("source: %w", err)
	}
	dest, err := location.Directory(req.To.Path)
	if err != nil {
		return Job{}, fmt.Errorf("destination: %w", err)
	}
	if source == dest {
		return Job{}, errors.New("choose a different destination directory")
	}
	if len(req.From.Names) == 0 {
		return Job{}, errors.New("select at least one file or directory")
	}
	from, to := location.Local{Base: source}, location.Local{Base: dest}
	items := []item{}
	skipped := []string{}
	walkErrors := []FileError{}
	conflicts := []string{}
	seen := map[string]bool{}
	var walk func(string, location.Entry)
	walk = func(rel string, info location.Entry) {
		if info.Symlink {
			skipped = append(skipped, rel)
			return
		}
		if !info.Directory && !info.Mode.IsRegular() {
			walkErrors = append(walkErrors, FileError{rel, "only regular files and directories can be copied"})
			return
		}
		items = append(items, item{rel, info})
		if !info.Directory {
			return
		}
		children, err := from.List(filepath.Join(source, rel))
		if err != nil {
			walkErrors = append(walkErrors, FileError{rel, err.Error()})
			return
		}
		for _, child := range children {
			walk(filepath.Join(rel, child.Name), child)
		}
	}
	for _, name := range req.From.Names {
		if name == "" || name == "." || name == ".." || filepath.Base(name) != name || strings.ContainsRune(name, '\x00') {
			return Job{}, errors.New("selected names must be direct children of the source directory")
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		info, err := from.Stat(filepath.Join(source, name))
		if err != nil {
			return Job{}, fmt.Errorf("source %s: %w", name, err)
		}
		if info.Directory {
			original, target := filepath.Join(source, name), filepath.Join(dest, name)
			if inside(original, dest) || inside(original, target) || inside(target, original) {
				return Job{}, errors.New("source and destination folders overlap; choose another destination")
			}
		}
		if !info.Symlink {
			_, err := to.Stat(filepath.Join(dest, name))
			if err == nil {
				conflicts = append(conflicts, name)
			} else if !errors.Is(err, fs.ErrNotExist) {
				return Job{}, fmt.Errorf("destination %s: %w", name, err)
			}
		}
		walk(name, info)
	}
	if len(conflicts) > 0 && !req.Replace {
		return Job{}, &Conflict{conflicts}
	}
	idBytes := make([]byte, 16)
	if _, err := rand.Read(idBytes); err != nil {
		return Job{}, err
	}
	job := &Job{ID: hex.EncodeToString(idBytes), State: "running", Skipped: skipped, Errors: walkErrors}
	for _, item := range items {
		if !item.info.Directory {
			job.FilesTotal++
		}
	}
	m.mu.Lock()
	m.jobs[job.ID] = job
	m.order = append(m.order, job.ID)
	initial := clone(job)
	m.mu.Unlock()
	go m.run(job, from, to, source, dest, items, req.Replace)
	return initial, nil
}

func inside(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Called with the manager lock held. Active jobs are never evicted.
func (m *Manager) prune() {
	finished := 0
	for _, id := range m.order {
		if m.jobs[id].State != "running" {
			finished++
		}
	}
	kept := m.order[:0]
	for _, id := range m.order {
		if finished > 100 && m.jobs[id].State != "running" {
			delete(m.jobs, id)
			finished--
		} else {
			kept = append(kept, id)
		}
	}
	m.order = kept
}

func clone(job *Job) Job {
	copy := *job
	copy.Skipped = append([]string{}, job.Skipped...)
	copy.Errors = append([]FileError{}, job.Errors...)
	return copy
}

func (m *Manager) Get(id string) (Job, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	job, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return clone(job), true
}

func (m *Manager) change(job *Job, fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn()
}

func (m *Manager) run(job *Job, from, to location.Location, source, dest string, items []item, replace bool) {
	for _, item := range items {
		m.change(job, func() { job.Current = item.rel })
		target := filepath.Join(dest, item.rel)
		var err error
		if item.info.Directory {
			err = to.MkdirAll(target)
		} else {
			err = m.copyFile(job, from, to, filepath.Join(source, item.rel), target, item.info.Mode, replace)
			if err == nil {
				m.change(job, func() { job.FilesDone++ })
			}
		}
		if err != nil {
			m.change(job, func() { job.Errors = append(job.Errors, FileError{item.rel, err.Error()}) })
		}
	}
	m.change(job, func() { job.State = "done"; job.Current = ""; m.prune() })
}

func (m *Manager) copyFile(job *Job, from, to location.Location, source, dest string, mode fs.FileMode, replace bool) error {
	reader, err := from.Open(source)
	if err != nil {
		return err
	}
	defer reader.Close()
	writer, err := to.Create(dest, mode, replace)
	if err != nil {
		return err
	}
	_, err = io.Copy(&progressWriter{writer, func(n int) { m.change(job, func() { job.BytesDone += int64(n) }) }}, reader)
	if err != nil {
		if aborter, ok := writer.(interface{ Abort() error }); ok {
			_ = aborter.Abort()
		} else {
			_ = writer.Close()
		}
		return err
	}
	return writer.Close()
}

type progressWriter struct {
	io.Writer
	report func(int)
}

func (w *progressWriter) Write(data []byte) (int, error) {
	n, err := w.Writer.Write(data)
	w.report(n)
	return n, err
}
