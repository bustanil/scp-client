package location

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path"
	"strings"

	"github.com/pkg/sftp"
)

var ErrUnavailable = errors.New("SSH session ended during copy. Disconnect and connect again")

type Remote struct {
	Client    *sftp.Client
	Base      string
	ServerID  string
	Available func() bool
}

func (r Remote) Identity() string { return "sftp:" + r.ServerID }

func (r Remote) result(err error) error {
	var status *sftp.StatusError
	var network *net.OpError
	if !r.Available() || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, sftp.ErrSSHFxNoConnection) || errors.As(err, &network) ||
		(errors.As(err, &status) && (status.FxCode() == sftp.ErrSSHFxNoConnection || status.FxCode() == sftp.ErrSSHFxConnectionLost)) {
		return ErrUnavailable
	}
	return err
}

func (r Remote) checkDirectories(directory string) error {
	if err := r.result(nil); err != nil {
		return err
	}
	directory = path.Clean(directory)
	if directory != r.Base && !strings.HasPrefix(directory, strings.TrimSuffix(r.Base, "/")+"/") {
		return errors.New("path is outside the location")
	}
	current := r.Base
	parts := append([]string{"."}, strings.Split(strings.TrimPrefix(strings.TrimPrefix(directory, r.Base), "/"), "/")...)
	for _, name := range parts {
		current = path.Join(current, name)
		info, err := r.Client.Lstat(current)
		if err != nil {
			return r.result(err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("directory is a link or non-directory: %s", current)
		}
	}
	return nil
}

func (r Remote) List(directory string) ([]Entry, error) {
	if err := r.checkDirectories(directory); err != nil {
		return nil, err
	}
	children, err := r.Client.ReadDir(directory)
	if err != nil {
		return nil, r.result(err)
	}
	entries := make([]Entry, 0, len(children))
	for _, child := range children {
		// Inspect the entry itself, including servers that return a link's target metadata.
		name := child.Name()
		if name == "." || name == ".." || path.Base(name) != name || name == "" || strings.ContainsRune(name, '\x00') {
			return nil, errors.New("server returned an invalid entry name")
		}
		info, err := r.Client.Lstat(path.Join(directory, name))
		if err != nil {
			return nil, r.result(err)
		}
		entries = append(entries, entry(path.Join(directory, name), info))
	}
	return entries, nil
}

func (r Remote) Stat(filename string) (Entry, error) {
	if err := r.checkDirectories(path.Dir(filename)); err != nil {
		return Entry{}, err
	}
	info, err := r.Client.Lstat(filename)
	if err != nil {
		return Entry{}, r.result(err)
	}
	return entry(filename, info), nil
}

func (r Remote) Open(filename string) (io.ReadCloser, error) {
	info, err := r.Stat(filename)
	if err != nil {
		return nil, err
	}
	if !info.Mode.IsRegular() || info.Symlink {
		return nil, errors.New("source is not a regular file")
	}
	file, err := r.Client.Open(filename)
	if err != nil {
		return nil, r.result(err)
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() {
		file.Close()
		if err != nil {
			return nil, r.result(err)
		}
		return nil, errors.New("source is not a regular file")
	}
	return &remoteReader{file, r}, nil
}

type remoteReader struct {
	file   *sftp.File
	remote Remote
}

func (f *remoteReader) Read(data []byte) (int, error) {
	if err := f.remote.result(nil); err != nil {
		return 0, err
	}
	n, err := f.file.Read(data)
	// Normal file EOF is unwrapped. Packet transport errors wrap EOF or use a status code.
	if err == io.EOF && f.remote.Available() {
		return n, io.EOF
	}
	return n, f.remote.result(err)
}

func (f *remoteReader) Close() error { return f.remote.result(f.file.Close()) }

func (r Remote) destination(filename string, replace bool) error {
	info, err := r.Stat(filename)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode.IsRegular() || info.Symlink {
		return errors.New("destination is not a regular file; links are not replaced")
	}
	if !replace {
		return fs.ErrExist
	}
	return nil
}

func (r Remote) Create(filename string, mode fs.FileMode, replace bool) (io.WriteCloser, error) {
	if err := r.destination(filename, replace); err != nil {
		return nil, err
	}
	extension := "hardlink@openssh.com"
	if replace {
		extension = "posix-rename@openssh.com"
	}
	_, atomic := r.Client.HasExtension(extension)
	writePath := filename
	flags := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if atomic {
		id := make([]byte, 16)
		if _, err := rand.Read(id); err != nil {
			return nil, err
		}
		writePath = path.Join(path.Dir(filename), ".scp-client-"+hex.EncodeToString(id))
	} else if replace {
		flags = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	file, err := r.Client.OpenFile(writePath, flags)
	if err != nil {
		return nil, r.result(err)
	}
	return &remoteWriter{file: file, remote: r, destination: filename, temporary: writePath, mode: mode, replace: replace, atomic: atomic}, nil
}

type remoteWriter struct {
	file                   *sftp.File
	remote                 Remote
	destination, temporary string
	mode                   fs.FileMode
	replace, atomic        bool
}

func (f *remoteWriter) Write(data []byte) (int, error) {
	if err := f.remote.result(nil); err != nil {
		return 0, err
	}
	n, err := f.file.Write(data)
	return n, f.remote.result(err)
}

func (f *remoteWriter) Abort() error {
	_ = f.file.Close()
	if f.atomic || !f.replace {
		return f.remote.result(f.remote.Client.Remove(f.temporary))
	}
	return nil
}

func (f *remoteWriter) Close() error {
	// Servers may reject permission changes. Copying permissions is best effort.
	_ = f.file.Chmod(f.mode.Perm())
	if f.atomic {
		defer f.remote.Client.Remove(f.temporary)
	}
	if err := f.remote.result(f.file.Close()); err != nil {
		return err
	}
	if !f.atomic {
		return nil
	}
	if err := f.remote.destination(f.destination, f.replace); err != nil {
		return err
	}
	if f.replace {
		return f.remote.result(f.remote.Client.PosixRename(f.temporary, f.destination))
	}
	return f.remote.result(f.remote.Client.Link(f.temporary, f.destination))
}

func (r Remote) MkdirAll(directory string) error {
	directory = path.Clean(directory)
	if directory != r.Base && !strings.HasPrefix(directory, strings.TrimSuffix(r.Base, "/")+"/") {
		return errors.New("directory is outside the copy destination")
	}
	if err := r.checkDirectories(r.Base); err != nil {
		return err
	}
	current := r.Base
	for _, name := range strings.Split(strings.TrimPrefix(strings.TrimPrefix(directory, r.Base), "/"), "/") {
		current = path.Join(current, name)
		info, err := r.Client.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			if err := r.Client.Mkdir(current); err != nil {
				// Another job or process can create the directory between these calls.
				if _, statErr := r.Client.Lstat(current); statErr != nil {
					return r.result(err)
				}
			}
			info, err = r.Client.Lstat(current)
		}
		if err != nil {
			return r.result(err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("destination directory is a link or non-directory: %s", current)
		}
	}
	return nil
}
