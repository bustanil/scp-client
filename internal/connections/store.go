package connections

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"scp-client/internal/secrets"
)

var ErrNotFound = errors.New("connection not found")

type Record struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	StartPath      string `json:"startPath"`
	Auth           string `json:"auth"`
	PrivateKeyPath string `json:"privateKeyPath"`
}

type Input struct {
	Record
	Secret *string `json:"secret,omitempty"`
}

type Store struct {
	mu      sync.Mutex
	file    string
	records []Record
	secrets secrets.Store
}

func New(dir string, keychain secrets.Store) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	s := &Store{file: filepath.Join(dir, "connections.json"), records: []Record{}, secrets: keychain}
	data, err := os.ReadFile(s.file)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.records); err != nil {
		return nil, fmt.Errorf("read connection file: %w", err)
	}
	seen := map[string]bool{}
	for _, record := range s.records {
		if record.ID == "" || seen[record.ID] {
			return nil, errors.New("connection file contains missing or duplicate ids")
		}
		if err := validate(record); err != nil {
			return nil, fmt.Errorf("invalid saved connection: %w", err)
		}
		seen[record.ID] = true
	}
	if s.records == nil {
		s.records = []Record{}
	}
	if err := os.Chmod(s.file, 0600); err != nil {
		return nil, err
	}
	return s, nil
}

func validate(record Record) error {
	if strings.TrimSpace(record.Name) == "" || strings.TrimSpace(record.Username) == "" {
		return errors.New("name and username are required")
	}
	if record.Host == "" || strings.ContainsAny(record.Host, "/\\@ \t\r\n") || (strings.Contains(record.Host, ":") && net.ParseIP(record.Host) == nil) {
		return errors.New("host must be a DNS name or IP address without a path or port")
	}
	if record.Port < 1 || record.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	if record.StartPath != "" && !path.IsAbs(record.StartPath) {
		return errors.New("start path must be an absolute remote directory")
	}
	if record.Auth != "password" && record.Auth != "privateKey" {
		return errors.New("choose password or private-key authentication")
	}
	if record.Auth == "privateKey" && !filepath.IsAbs(record.PrivateKeyPath) {
		return errors.New("private-key path must be an absolute local file path")
	}
	return nil
}

func (s *Store) List() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Record{}, s.records...)
}

func (s *Store) Get(id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range s.records {
		if record.ID == id {
			return record, nil
		}
	}
	return Record{}, ErrNotFound
}

func (s *Store) Secret(id string) (string, error) {
	value, err := s.secrets.Get(id)
	if errors.Is(err, secrets.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("cannot read macOS Keychain; check Keychain access")
	}
	return value, nil
}

func (s *Store) SaveSecret(record Record, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, current := range s.records {
		if current.ID == record.ID {
			if current != record {
				return errors.New("connection changed during login; connect again")
			}
			if err := s.secrets.Set(record.ID, value); err != nil {
				return errors.New("cannot save the secret in macOS Keychain")
			}
			return nil
		}
	}
	return ErrNotFound
}

func (s *Store) Save(id string, input Input) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	input.Name = strings.TrimSpace(input.Name)
	input.Host = strings.TrimSpace(input.Host)
	input.Username = strings.TrimSpace(input.Username)
	if input.Port == 0 {
		input.Port = 22
	}
	if input.Auth == "password" {
		input.PrivateKeyPath = ""
	}
	if err := validate(input.Record); err != nil {
		return Record{}, err
	}
	index := -1
	var previous Record
	if id != "" {
		for i, record := range s.records {
			if record.ID == id {
				index, previous = i, record
				break
			}
		}
		if index == -1 {
			return Record{}, ErrNotFound
		}
	} else {
		var err error
		id, err = ID()
		if err != nil {
			return Record{}, err
		}
	}
	input.ID = id
	changeSecret := input.Secret != nil || (index >= 0 && (previous.Auth != input.Auth || previous.PrivateKeyPath != input.PrivateKeyPath))
	oldSecret := ""
	if changeSecret {
		var err error
		oldSecret, err = s.Secret(id)
		if err != nil {
			return Record{}, err
		}
		var writeErr error
		if input.Secret != nil && *input.Secret != "" {
			writeErr = s.secrets.Set(id, *input.Secret)
		} else {
			writeErr = s.secrets.Delete(id)
		}
		if writeErr != nil && !errors.Is(writeErr, secrets.ErrNotFound) {
			return Record{}, errors.New("cannot update the secret in macOS Keychain")
		}
	}
	updated := append([]Record{}, s.records...)
	if index >= 0 {
		updated[index] = input.Record
	} else {
		updated = append(updated, input.Record)
	}
	if err := s.persist(updated); err != nil {
		if changeSecret {
			if oldSecret != "" {
				_ = s.secrets.Set(id, oldSecret)
			} else {
				_ = s.secrets.Delete(id)
			}
		}
		return Record{}, err
	}
	s.records = updated
	return input.Record, nil
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, record := range s.records {
		if record.ID == id {
			index = i
			break
		}
	}
	if index == -1 {
		return ErrNotFound
	}
	previous, err := s.Secret(id)
	if err != nil {
		return err
	}
	if err := s.secrets.Delete(id); err != nil && !errors.Is(err, secrets.ErrNotFound) {
		return errors.New("cannot delete the secret from macOS Keychain")
	}
	updated := append([]Record{}, s.records[:index]...)
	updated = append(updated, s.records[index+1:]...)
	if err := s.persist(updated); err != nil {
		if previous != "" {
			_ = s.secrets.Set(id, previous)
		}
		return err
	}
	s.records = updated
	return nil
}

func (s *Store) persist(records []Record) error {
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(s.file), ".connections-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(data, '\n')); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), s.file)
}

func ID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}
