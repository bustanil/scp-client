package httpapi

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"scp-client/internal/connections"
	"scp-client/internal/knownhosts"
	"scp-client/internal/secrets"
	"scp-client/internal/session"
)

type memorySecrets struct {
	mu   sync.Mutex
	data map[string]string
	fail bool
}

func (s *memorySecrets) Get(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return "", errors.New("Keychain unavailable")
	}
	value, ok := s.data[id]
	if !ok {
		return "", secrets.ErrNotFound
	}
	return value, nil
}
func (s *memorySecrets) Set(id, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("Keychain unavailable")
	}
	s.data[id] = value
	return nil
}
func (s *memorySecrets) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("Keychain unavailable")
	}
	delete(s.data, id)
	return nil
}

type sshFixture struct {
	listener    net.Listener
	mu          sync.Mutex
	signer      ssh.Signer
	connections []net.Conn
	port        int
}

func makeSigner(t *testing.T) (ed25519.PrivateKey, ssh.Signer) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	return private, signer
}

// The fixture uses the real SSH handshake and SFTP subsystem on loopback.
func sshServer(t *testing.T, root string, acceptedKey ssh.PublicKey) *sshFixture {
	t.Helper()
	_, hostSigner := makeSigner(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &sshFixture{listener: listener, signer: hostSigner, port: listener.Addr().(*net.TCPAddr).Port}
	t.Cleanup(func() {
		listener.Close()
		server.mu.Lock()
		for _, conn := range server.connections {
			conn.Close()
		}
		server.mu.Unlock()
	})
	go func() {
		for {
			network, err := listener.Accept()
			if err != nil {
				return
			}
			server.mu.Lock()
			signer := server.signer
			server.connections = append(server.connections, network)
			server.mu.Unlock()
			go func() {
				defer network.Close()
				config := &ssh.ServerConfig{
					PasswordCallback: func(meta ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
						if meta.User() == "tester" && string(password) == "fixture-password" {
							return nil, nil
						}
						return nil, errors.New("bad password")
					},
					PublicKeyCallback: func(meta ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
						if meta.User() == "tester" && acceptedKey != nil && bytes.Equal(key.Marshal(), acceptedKey.Marshal()) {
							return nil, nil
						}
						return nil, errors.New("bad key")
					},
				}
				config.AddHostKey(signer)
				client, channels, requests, err := ssh.NewServerConn(network, config)
				if err != nil {
					return
				}
				defer client.Close()
				go ssh.DiscardRequests(requests)
				for incoming := range channels {
					if incoming.ChannelType() != "session" {
						incoming.Reject(ssh.UnknownChannelType, "session required")
						continue
					}
					channel, requests, err := incoming.Accept()
					if err != nil {
						continue
					}
					go func() {
						defer channel.Close()
						for req := range requests {
							var subsystem struct{ Name string }
							if req.Type != "subsystem" || ssh.Unmarshal(req.Payload, &subsystem) != nil || subsystem.Name != "sftp" {
								_ = req.Reply(false, nil)
								continue
							}
							remote, err := sftp.NewServer(channel, sftp.ReadOnly(), sftp.WithServerWorkingDirectory(root))
							if err != nil {
								_ = req.Reply(false, nil)
								return
							}
							_ = req.Reply(true, nil)
							_ = remote.Serve()
							_ = remote.Close()
							return
						}
					}()
				}
			}()
		}
	}()
	return server
}

func fixtureServices(t *testing.T, dir string, keychain *memorySecrets) (*connections.Store, *session.Manager, http.Handler) {
	t.Helper()
	records, err := connections.New(dir, keychain)
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := knownhosts.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	sessions := session.New(records, hosts)
	t.Cleanup(sessions.Close)
	return records, sessions, NewWithServices(dir, nil, records, sessions)
}

func savedConnection(t *testing.T, handler http.Handler, input connections.Input) connections.Record {
	t.Helper()
	w := call(t, handler, "POST", "/api/connections", input)
	if w.Code != http.StatusCreated {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	var record connections.Record
	if err := json.Unmarshal(w.Body.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(w.Body.String(), "fixture-password") || strings.Contains(w.Body.String(), "secret") {
		t.Fatal("connection response contains a secret")
	}
	return record
}

func TestSavedConnectionCRUDAndRestart(t *testing.T) {
	dir := t.TempDir()
	keychain := &memorySecrets{data: map[string]string{}}
	_, _, handler := fixtureServices(t, dir, keychain)
	password := "fixture-password"
	input := connections.Input{Record: connections.Record{Name: "My host", Host: "example.com", Port: 22, Username: "tester", Auth: "password"}, Secret: &password}
	record := savedConnection(t, handler, input)
	input.Record = record
	input.Name = "Renamed"
	input.Secret = nil
	w := call(t, handler, "PUT", "/api/connections/"+record.ID, input)
	if w.Code != http.StatusOK {
		t.Fatalf("update: %d %s", w.Code, w.Body.String())
	}
	value, err := keychain.Get(record.ID)
	if err != nil || value != password {
		t.Fatal("editing discarded the saved secret")
	}
	_, _, restarted := fixtureServices(t, dir, keychain)
	w = call(t, restarted, "GET", "/api/connections", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Renamed") || strings.Contains(w.Body.String(), password) {
		t.Fatalf("restart: %s", w.Body.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, "connections.json"))
	if err != nil || strings.Contains(string(data), password) || strings.Contains(string(data), "secret") {
		t.Fatalf("secret in connection file: %v", err)
	}
	for path, want := range map[string]os.FileMode{dir: 0700, filepath.Join(dir, "connections.json"): 0600} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != want {
			t.Fatalf("mode %s: %v %v", path, info, err)
		}
	}
	w = call(t, restarted, "DELETE", "/api/connections/"+record.ID, nil)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if _, err := keychain.Get(record.ID); !errors.Is(err, secrets.ErrNotFound) {
		t.Fatal("secret not deleted")
	}
	if w := call(t, restarted, "GET", "/api/connections", nil); strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatal("connection not deleted")
	}
}

func TestPasswordHostTrustBrowseAuthFailureAndChangedKey(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "Folder"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "a.txt"), "remote")
	if err := os.Symlink("a.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	fixture := sshServer(t, root, nil)
	dir := t.TempDir()
	keychain := &memorySecrets{data: map[string]string{}}
	_, sessions, handler := fixtureServices(t, dir, keychain)
	password := "fixture-password"
	record := savedConnection(t, handler, connections.Input{Record: connections.Record{Name: "Password host", Host: "127.0.0.1", Port: fixture.port, Username: "tester", Auth: "password"}, Secret: &password})
	input := session.Request{ConnectionID: record.ID}
	w := call(t, handler, "POST", "/api/sessions", input)
	if w.Code != http.StatusPreconditionFailed {
		t.Fatalf("unknown host: %d %s", w.Code, w.Body.String())
	}
	var problem knownhosts.Problem
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if problem.Code != "unknown_host_key" || problem.Fingerprint != ssh.FingerprintSHA256(fixture.signer.PublicKey()) {
		t.Fatalf("key: %+v", problem)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "known_hosts"))
	input.TrustFingerprint = "SHA256:wrong"
	if w := call(t, handler, "POST", "/api/sessions", input); w.Code != http.StatusPreconditionFailed {
		t.Fatal("trusted a different fingerprint")
	}
	input.TrustFingerprint = problem.Fingerprint
	w = call(t, handler, "POST", "/api/sessions", input)
	if w.Code != http.StatusCreated {
		t.Fatalf("connect: %d %s", w.Code, w.Body.String())
	}
	var result session.Connected
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Path != root || len(result.Entries) != 3 || result.Entries[0].Name != "Folder" || !result.Entries[2].Symlink {
		t.Fatalf("listing: %+v", result)
	}
	if len(before) != 0 {
		t.Fatal("unknown key saved before trust")
	}
	w = call(t, handler, "GET", "/api/list?kind=sftp&sessionId="+result.SessionID+"&path="+root+"/Folder", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), root+"/Folder") {
		t.Fatalf("browse: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, handler, "DELETE", "/api/connections/"+record.ID, nil); w.Code != http.StatusConflict {
		t.Fatal("deleted active connection")
	}
	// An edited record does not end its live session.
	update := connections.Input{Record: record}
	update.Name = "New name"
	if w := call(t, handler, "PUT", "/api/connections/"+record.ID, update); w.Code != http.StatusOK {
		t.Fatal("edit failed")
	}
	if !sessions.InUse(record.ID) {
		t.Fatal("edit dropped live session")
	}
	call(t, handler, "DELETE", "/api/sessions/"+result.SessionID, nil)
	_, _, restarted := fixtureServices(t, dir, keychain)
	input.TrustFingerprint = ""
	w = call(t, restarted, "POST", "/api/sessions", input)
	if w.Code != http.StatusCreated {
		t.Fatalf("trust did not survive restart: %d %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	call(t, restarted, "DELETE", "/api/sessions/"+result.SessionID, nil)
	wrong := "wrong-password"
	input.Secret = &wrong
	if w := call(t, restarted, "POST", "/api/sessions", input); w.Code != http.StatusUnauthorized {
		t.Fatalf("bad login: %d %s", w.Code, w.Body.String())
	}
	knownBefore, _ := os.ReadFile(filepath.Join(dir, "known_hosts"))
	_, replacement := makeSigner(t)
	fixture.mu.Lock()
	fixture.signer = replacement
	fixture.mu.Unlock()
	input.Secret = nil
	input.TrustFingerprint = ssh.FingerprintSHA256(replacement.PublicKey())
	w = call(t, restarted, "POST", "/api/sessions", input)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "host_key_changed") {
		t.Fatalf("changed key: %d %s", w.Code, w.Body.String())
	}
	knownAfter, _ := os.ReadFile(filepath.Join(dir, "known_hosts"))
	if !bytes.Equal(knownBefore, knownAfter) {
		t.Fatal("changed host key overwrote trust")
	}
}

func TestPrivateKeyAndEncryptedPassphrase(t *testing.T) {
	private, signer := makeSigner(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "remote.txt"), "key login")
	fixture := sshServer(t, root, signer.PublicKey())
	dir := t.TempDir()
	keychain := &memorySecrets{data: map[string]string{}}
	_, _, handler := fixtureServices(t, dir, keychain)
	passphrase := "fixture-passphrase"
	for _, encrypted := range []bool{false, true} {
		var block *pem.Block
		if encrypted {
			block, err = ssh.MarshalPrivateKeyWithPassphrase(private, "test", []byte(passphrase))
		} else {
			block, err = ssh.MarshalPrivateKey(private, "test")
		}
		if err != nil {
			t.Fatal(err)
		}
		keyPath := filepath.Join(t.TempDir(), "id_ed25519")
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
			t.Fatal(err)
		}
		record := savedConnection(t, handler, connections.Input{Record: connections.Record{Name: "Key host", Host: "127.0.0.1", Port: fixture.port, Username: "tester", Auth: "privateKey", PrivateKeyPath: keyPath, StartPath: root}})
		input := session.Request{ConnectionID: record.ID, TrustFingerprint: ssh.FingerprintSHA256(fixture.signer.PublicKey())}
		if encrypted {
			w := call(t, handler, "POST", "/api/sessions", input)
			if w.Code != http.StatusPreconditionRequired || !strings.Contains(w.Body.String(), "passphrase_required") {
				t.Fatalf("encrypted prompt: %d %s", w.Code, w.Body.String())
			}
			wrong := "wrong"
			input.Secret = &wrong
			if w := call(t, handler, "POST", "/api/sessions", input); w.Code != http.StatusUnauthorized {
				t.Fatal("wrong key passphrase accepted")
			}
			input.Secret = &passphrase
			input.SaveSecret = true
		}
		w := call(t, handler, "POST", "/api/sessions", input)
		if w.Code != http.StatusCreated {
			t.Fatalf("key login: %d %s", w.Code, w.Body.String())
		}
		var result session.Connected
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if len(result.Entries) != 1 || result.Entries[0].Name != "remote.txt" {
			t.Fatalf("key browse: %+v", result)
		}
		call(t, handler, "DELETE", "/api/sessions/"+result.SessionID, nil)
		if encrypted {
			value, err := keychain.Get(record.ID)
			if err != nil || value != passphrase {
				t.Fatal("passphrase not saved")
			}
			_, _, restarted := fixtureServices(t, dir, keychain)
			input.Secret = nil
			input.TrustFingerprint = ""
			input.SaveSecret = false
			w := call(t, restarted, "POST", "/api/sessions", input)
			if w.Code != http.StatusCreated {
				t.Fatalf("saved passphrase restart: %d %s", w.Code, w.Body.String())
			}
			data, _ := os.ReadFile(filepath.Join(dir, "connections.json"))
			if bytes.Contains(data, []byte(passphrase)) {
				t.Fatal("passphrase in JSON")
			}
		}
	}
}

func TestFailedKeychainWriteDoesNotSaveConnection(t *testing.T) {
	dir := t.TempDir()
	keychain := &memorySecrets{data: map[string]string{}, fail: true}
	records, _, handler := fixtureServices(t, dir, keychain)
	password := "fixture-password"
	w := call(t, handler, "POST", "/api/connections", connections.Input{Record: connections.Record{Name: "host", Host: "example.com", Port: 22, Username: "tester", Auth: "password"}, Secret: &password})
	if w.Code != http.StatusBadRequest || len(records.List()) != 0 {
		t.Fatalf("saved after secret error: %d %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "connections.json")); !os.IsNotExist(err) {
		t.Fatal("connection file created after secret error")
	}
}

func TestExplicitSecretWorksWithoutKeychainAccess(t *testing.T) {
	fixture := sshServer(t, t.TempDir(), nil)
	keychain := &memorySecrets{data: map[string]string{}}
	_, _, handler := fixtureServices(t, t.TempDir(), keychain)
	record := savedConnection(t, handler, connections.Input{Record: connections.Record{Name: "Temporary login", Host: "127.0.0.1", Port: fixture.port, Username: "tester", Auth: "password"}})
	keychain.mu.Lock()
	keychain.fail = true
	keychain.mu.Unlock()
	password := "fixture-password"
	w := call(t, handler, "POST", "/api/sessions", session.Request{ConnectionID: record.ID, Secret: &password, TrustFingerprint: ssh.FingerprintSHA256(fixture.signer.PublicKey())})
	if w.Code != http.StatusCreated {
		t.Fatalf("ephemeral login consulted Keychain: %d %s", w.Code, w.Body.String())
	}
}
