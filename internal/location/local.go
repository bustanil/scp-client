package location

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Entry struct {
	Name      string      `json:"name"`
	Path      string      `json:"path"`
	Directory bool        `json:"directory"`
	Symlink   bool        `json:"symlink"`
	Size      int64       `json:"size"`
	Mode      fs.FileMode `json:"mode"`
	Modified  time.Time   `json:"modified"`
}

// Location describes the operations needed by a copy job.
type Location interface {
	List(string) ([]Entry, error)
	Stat(string) (Entry, error)
	Open(string) (io.ReadCloser, error)
	Create(string, fs.FileMode, bool) (io.WriteCloser, error)
	MkdirAll(string) error
}

type Local struct {
	Base string
}

func Directory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("use an absolute directory path")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return resolved, nil
}

func entry(path string, info fs.FileInfo) Entry {
	return Entry{info.Name(), path, info.IsDir(), info.Mode()&os.ModeSymlink != 0, info.Size(), info.Mode(), info.ModTime()}
}

func (l Local) List(path string) ([]Entry, error) {
	if err := l.checkDirectories(path); err != nil {
		return nil, err
	}
	children, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(children))
	for _, child := range children {
		info, err := child.Info()
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry(filepath.Join(path, child.Name()), info))
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Directory != entries[j].Directory {
			return entries[i].Directory
		}
		a, b := strings.ToLower(entries[i].Name), strings.ToLower(entries[j].Name)
		if a == b {
			return entries[i].Name < entries[j].Name
		}
		return a < b
	})
	return entries, nil
}

func (l Local) Stat(path string) (Entry, error) {
	if err := l.checkDirectories(filepath.Dir(path)); err != nil {
		return Entry{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return Entry{}, err
	}
	return entry(path, info), nil
}

func (l Local) Open(path string) (io.ReadCloser, error) {
	if err := l.checkDirectories(filepath.Dir(path)); err != nil {
		return nil, err
	}
	// Never follow a source link if it changes after the job's directory walk.
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, errors.New("source is not a regular file")
	}
	return file, nil
}

func (l Local) Create(path string, mode fs.FileMode, replace bool) (io.WriteCloser, error) {
	if err := l.checkDirectories(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := destinationFile(path, replace); err != nil {
		return nil, err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".scp-client-*")
	if err != nil {
		return nil, err
	}
	// Permission copying is best effort, as specified for v1.
	_ = file.Chmod(mode.Perm())
	return &pendingFile{File: file, local: l, path: path, replace: replace}, nil
}

func destinationFile(path string, replace bool) error {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("destination is not a regular file; links are not replaced")
	}
	if !replace {
		return fs.ErrExist
	}
	return nil
}

func (l Local) MkdirAll(path string) error {
	rel, err := filepath.Rel(l.Base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("directory is outside the copy destination")
	}
	current := l.Base
	for _, name := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, name)
		if err := os.Mkdir(current, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination directory is a link or non-directory: %s", current)
		}
	}
	return nil
}

// Existing paths below Base must remain directories, never symbolic links.
func (l Local) checkDirectories(path string) error {
	rel, err := filepath.Rel(l.Base, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return errors.New("path is outside the location")
	}
	current := l.Base
	parts := append([]string{"."}, strings.Split(rel, string(filepath.Separator))...)
	for _, name := range parts {
		current = filepath.Join(current, name)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("directory is a link or non-directory: %s", current)
		}
	}
	return nil
}

// Files become visible only after a complete copy. Abort removes partial data.
type pendingFile struct {
	*os.File
	local   Local
	path    string
	replace bool
}

func (f *pendingFile) Abort() error {
	_ = f.File.Close()
	return os.Remove(f.Name())
}

func (f *pendingFile) Close() error {
	defer os.Remove(f.Name())
	if err := f.File.Close(); err != nil {
		return err
	}
	if err := f.local.checkDirectories(filepath.Dir(f.path)); err != nil {
		return err
	}
	if err := destinationFile(f.path, f.replace); err != nil {
		return err
	}
	if f.replace {
		return os.Rename(f.Name(), f.path)
	}
	// Link fails atomically if another process has created this name meanwhile.
	return os.Link(f.Name(), f.path)
}
