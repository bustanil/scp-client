package knownhosts

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	sshhosts "golang.org/x/crypto/ssh/knownhosts"
)

type Problem struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	KeyType     string `json:"keyType,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

func (p *Problem) Error() string { return p.Message }

type Store struct {
	mu   sync.Mutex
	file string
}

func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	file := filepath.Join(dir, "known_hosts")
	f, err := os.OpenFile(file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(file, 0600); err != nil {
		return nil, err
	}
	return &Store{file: file}, nil
}

func (s *Store) Callback(trustFingerprint string) ssh.HostKeyCallback {
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		check, err := sshhosts.New(s.file)
		if err != nil {
			return err
		}
		err = check(host, remote, key)
		if err == nil {
			return nil
		}
		var keyError *sshhosts.KeyError
		if !errors.As(err, &keyError) {
			return err
		}
		if len(keyError.Want) > 0 {
			return &Problem{Code: "host_key_changed", Message: "The host key no longer matches the trusted key. Verify the change with the server administrator before updating known_hosts."}
		}
		fingerprint := ssh.FingerprintSHA256(key)
		if trustFingerprint != fingerprint {
			return &Problem{Code: "unknown_host_key", Message: "Verify this host key before trusting the server.", KeyType: key.Type(), Fingerprint: fingerprint}
		}
		f, err := os.OpenFile(s.file, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := f.WriteString(sshhosts.Line([]string{sshhosts.Normalize(host)}, key) + "\n"); err != nil {
			return err
		}
		return f.Sync()
	}
}
