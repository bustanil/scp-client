package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"scp-client/internal/connections"
	"scp-client/internal/knownhosts"
	"scp-client/internal/location"
)

type Problem struct{ Code, Message string }

func (p *Problem) Error() string { return p.Message }

type Request struct {
	ConnectionID     string  `json:"connectionId"`
	TrustFingerprint string  `json:"trustFingerprint,omitempty"`
	Secret           *string `json:"secret,omitempty"`
	SaveSecret       bool    `json:"saveSecret,omitempty"`
}

type Listing struct {
	Path    string           `json:"path"`
	Parent  string           `json:"parent"`
	Home    string           `json:"home"`
	Entries []location.Entry `json:"entries"`
}

type Connected struct {
	SessionID    string `json:"sessionId"`
	ConnectionID string `json:"connectionId"`
	Name         string `json:"name"`
	Listing
}

type live struct {
	id           string
	connectionID string
	ssh          *ssh.Client
	sftp         *sftp.Client
	home         string
}

type Manager struct {
	mu          sync.RWMutex
	sessions    map[string]*live
	connections *connections.Store
	hosts       *knownhosts.Store
}

func New(records *connections.Store, hosts *knownhosts.Store) *Manager {
	return &Manager{sessions: map[string]*live{}, connections: records, hosts: hosts}
}

func (m *Manager) Connect(ctx context.Context, req Request) (Connected, error) {
	record, err := m.connections.Get(req.ConnectionID)
	if err != nil {
		return Connected{}, err
	}
	secret := ""
	if req.Secret != nil {
		secret = *req.Secret
	}
	loadSecret := func() error {
		if req.Secret != nil {
			return nil
		}
		secret, err = m.connections.Secret(record.ID)
		return err
	}
	var auth ssh.AuthMethod
	if record.Auth == "password" {
		if err := loadSecret(); err != nil {
			return Connected{}, err
		}
		if secret == "" {
			return Connected{}, &Problem{"secret_required", "Enter the password for this connection."}
		}
		auth = ssh.Password(secret)
	} else {
		data, err := readPrivateKey(record.PrivateKeyPath)
		if err != nil {
			return Connected{}, &Problem{"invalid_key", "Cannot read the private-key file: " + err.Error()}
		}
		signer, err := ssh.ParsePrivateKey(data)
		var encrypted *ssh.PassphraseMissingError
		if errors.As(err, &encrypted) {
			if err := loadSecret(); err != nil {
				return Connected{}, err
			}
			if secret == "" {
				return Connected{}, &Problem{"passphrase_required", "This private key is encrypted. Enter its passphrase."}
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase(data, []byte(secret))
			if err != nil {
				return Connected{}, &Problem{"auth_failed", "The private-key passphrase is incorrect."}
			}
		}
		if err != nil {
			return Connected{}, &Problem{"invalid_key", "The file is not a supported SSH private key."}
		}
		auth = ssh.PublicKeys(signer)
	}
	address := net.JoinHostPort(record.Host, strconv.Itoa(record.Port))
	network, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return Connected{}, &Problem{"connect_failed", "Cannot reach the SSH server: " + err.Error()}
	}
	succeeded := false
	defer func() {
		if !succeeded {
			network.Close()
		}
	}()
	stopCancel := context.AfterFunc(ctx, func() { network.Close() })
	defer stopCancel()
	_ = network.SetDeadline(time.Now().Add(15 * time.Second))
	sshConn, channels, requests, err := ssh.NewClientConn(network, address, &ssh.ClientConfig{User: record.Username, Auth: []ssh.AuthMethod{auth}, HostKeyCallback: m.hosts.Callback(req.TrustFingerprint)})
	if err != nil {
		var hostProblem *knownhosts.Problem
		if errors.As(err, &hostProblem) {
			return Connected{}, hostProblem
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return Connected{}, &Problem{"auth_failed", "SSH authentication failed: " + err.Error()}
		}
		return Connected{}, &Problem{"connect_failed", "SSH connection failed: " + err.Error()}
	}
	client := ssh.NewClient(sshConn, channels, requests)
	remote, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return Connected{}, &Problem{"sftp_failed", "The server did not open its SFTP subsystem: " + err.Error()}
	}
	closeFailed := func() { remote.Close(); client.Close() }
	home, err := remote.RealPath(".")
	if err != nil {
		closeFailed()
		return Connected{}, err
	}
	id, err := connections.ID()
	if err != nil {
		closeFailed()
		return Connected{}, err
	}
	liveSession := &live{id: id, connectionID: record.ID, ssh: client, sftp: remote, home: home}
	start := record.StartPath
	if start == "" {
		start = home
	}
	listing, err := liveSession.list(ctx, start)
	if err != nil {
		closeFailed()
		return Connected{}, &Problem{"invalid_path", "Cannot open the remote start directory: " + err.Error()}
	}
	if req.SaveSecret && req.Secret != nil {
		if err := m.connections.SaveSecret(record, secret); err != nil {
			closeFailed()
			return Connected{}, err
		}
	}
	_ = network.SetDeadline(time.Time{})
	m.mu.Lock()
	m.sessions[id] = liveSession
	m.mu.Unlock()
	succeeded = true
	go func() {
		_ = remote.Wait()
		_ = client.Close()
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
	}()
	return Connected{SessionID: id, ConnectionID: record.ID, Name: record.Name, Listing: listing}, nil
}

func (s *live) list(ctx context.Context, directory string) (Listing, error) {
	if !path.IsAbs(directory) {
		return Listing{}, errors.New("use an absolute remote directory path")
	}
	stop := context.AfterFunc(ctx, func() { _ = s.ssh.Close() })
	defer stop()
	directory, err := s.sftp.RealPath(directory)
	if err != nil {
		return Listing{}, err
	}
	info, err := s.sftp.Stat(directory)
	if err != nil {
		return Listing{}, err
	}
	if !info.IsDir() {
		return Listing{}, errors.New("path is not a directory")
	}
	children, err := s.sftp.ReadDirContext(ctx, directory)
	if err != nil {
		return Listing{}, err
	}
	entries := make([]location.Entry, 0, len(children))
	for _, child := range children {
		entries = append(entries, location.Entry{Name: child.Name(), Path: path.Join(directory, child.Name()), Directory: child.IsDir(), Symlink: child.Mode()&os.ModeSymlink != 0, Size: child.Size(), Mode: child.Mode(), Modified: child.ModTime()})
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
	return Listing{directory, path.Dir(directory), s.home, entries}, nil
}

func (m *Manager) List(ctx context.Context, id, directory string) (Listing, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	m.mu.RLock()
	s := m.sessions[id]
	m.mu.RUnlock()
	if s == nil {
		return Listing{}, &Problem{"session_unavailable", "This SSH session has ended. Disconnect and connect again."}
	}
	listing, err := s.list(ctx, directory)
	if err != nil {
		return Listing{}, &Problem{"list_failed", fmt.Sprintf("Cannot list remote directory: %v", err)}
	}
	return listing, nil
}

// Resolve binds a job to the live session and a canonical remote directory.
func (m *Manager) Resolve(id, directory string) (location.Location, string, error) {
	m.mu.RLock()
	s := m.sessions[id]
	m.mu.RUnlock()
	if s == nil {
		return nil, "", location.ErrUnavailable
	}
	remote := location.Remote{Client: s.sftp, Available: func() bool {
		m.mu.RLock()
		defer m.mu.RUnlock()
		return m.sessions[id] == s
	}}
	if !path.IsAbs(directory) {
		return nil, "", errors.New("use an absolute remote directory path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { s.ssh.Close() })
	defer stop()
	canonical, err := s.sftp.RealPath(path.Clean(directory))
	if err != nil {
		return nil, "", fmt.Errorf("cannot resolve remote directory: %w", err)
	}
	remote.Base = canonical
	info, err := s.sftp.Lstat(canonical)
	if err != nil {
		return nil, "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, "", errors.New("remote path is not a directory")
	}
	return remote, canonical, nil
}

func readPrivateKey(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("choose a regular private-key file smaller than 1 MiB")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, errors.New("private-key file exceeds 1 MiB")
	}
	return data, nil
}

func (m *Manager) InUse(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.sessions {
		if s.connectionID == id {
			return true
		}
	}
	return false
}

func (m *Manager) Disconnect(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if s != nil {
		s.sftp.Close()
		s.ssh.Close()
	}
}

func (m *Manager) Close() {
	m.mu.RLock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.RUnlock()
	for _, id := range ids {
		m.Disconnect(id)
	}
}
