package secrets

import (
	"errors"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestMacKeychainRoundTrip(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Getenv("SCP_CLIENT_TEST_KEYCHAIN") != "1" {
		t.Skip("set SCP_CLIENT_TEST_KEYCHAIN=1 on macOS to test the real Keychain")
	}
	store := Keychain{}
	id := "test-" + strconv.FormatInt(time.Now().UnixNano(), 16)
	t.Cleanup(func() { _ = store.Delete(id) })
	if err := store.Set(id, "local-test-secret"); err != nil {
		t.Fatal(err)
	}
	value, err := store.Get(id)
	if err != nil || value != "local-test-secret" {
		t.Fatalf("read: %v", err)
	}
	if err := store.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
}
