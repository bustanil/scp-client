package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDesktopListenerAuthenticationAndOriginProtection(t *testing.T) {
	address, token := "127.0.0.1:49152", "a-disposable-desktop-test-token-value"
	handler := NewWithOptions(t.TempDir(), nil, nil, nil, Options{Address: address, Token: token})
	for _, scenario := range []struct {
		name, host, origin, token, site string
		status                          int
	}{
		{"owned service", address, "http://" + address, token, "same-origin", http.StatusOK},
		{"health probe", address, "", token, "", http.StatusOK},
		{"localhost alias", "localhost:49152", "http://localhost:49152", token, "same-origin", http.StatusOK},
		{"missing token", address, "", "", "", http.StatusUnauthorized},
		{"wrong token", address, "", "wrong-token", "", http.StatusUnauthorized},
		{"browser port", "127.0.0.1:8787", "", token, "", http.StatusForbidden},
		{"foreign host", "evil.example:49152", "", token, "", http.StatusForbidden},
		{"foreign origin", address, "https://evil.example", token, "", http.StatusForbidden},
		{"vite origin", address, "http://127.0.0.1:5173", token, "", http.StatusForbidden},
		{"null origin", address, "null", token, "", http.StatusForbidden},
		{"cross site", address, "", token, "cross-site", http.StatusForbidden},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+scenario.host+"/api/health", nil)
			r.Header.Set("Origin", scenario.origin)
			r.Header.Set("X-SCP-Desktop-Token", scenario.token)
			r.Header.Set("Sec-Fetch-Site", scenario.site)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != scenario.status {
				t.Fatalf("got %d, want %d: %s", w.Code, scenario.status, w.Body.String())
			}
		})
	}
}
