package secrets

import "github.com/zalando/go-keyring"

var ErrNotFound = keyring.ErrNotFound

type Store interface {
	Get(string) (string, error)
	Set(string, string) error
	Delete(string) error
}

type Keychain struct{}

func (Keychain) Get(id string) (string, error) { return keyring.Get("scp-client", id) }
func (Keychain) Set(id, value string) error    { return keyring.Set("scp-client", id, value) }
func (Keychain) Delete(id string) error        { return keyring.Delete("scp-client", id) }
