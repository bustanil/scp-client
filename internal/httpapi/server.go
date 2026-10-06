package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"sync"

	"scp-client/internal/connections"
	"scp-client/internal/knownhosts"

	"scp-client/internal/location"
	"scp-client/internal/session"
	"scp-client/internal/transfer"
)

type Server struct {
	home         string
	jobs         *transfer.Manager
	connections  *connections.Store
	sessions     *session.Manager
	connectionMu sync.Mutex
}

// Options binds desktop requests to the listener chosen by the Go process.
type Options struct {
	Address string
	Token   string
}

func New(home string, assets fs.FS) http.Handler {
	return NewWithServices(home, assets, nil, nil)
}

func NewWithServices(home string, assets fs.FS, records *connections.Store, sessions *session.Manager) http.Handler {
	return NewWithOptions(home, assets, records, sessions, Options{Address: "127.0.0.1:8787"})
}

func NewWithOptions(home string, assets fs.FS, records *connections.Store, sessions *session.Manager, options Options) http.Handler {
	s := &Server{home: home, jobs: transfer.NewManager(), connections: records, sessions: sessions}
	if sessions != nil {
		s.jobs = transfer.NewWithRemote(sessions.Resolve)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/list", s.list)
	mux.HandleFunc("POST /api/jobs", s.startJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, struct {
			Status string `json:"status"`
		}{"ok"})
	})
	mux.HandleFunc("GET /api/connections", s.listConnections)
	mux.HandleFunc("POST /api/connections", s.createConnection)
	mux.HandleFunc("PUT /api/connections/{id}", s.updateConnection)
	mux.HandleFunc("DELETE /api/connections/{id}", s.deleteConnection)
	mux.HandleFunc("POST /api/sessions", s.connect)
	mux.HandleFunc("DELETE /api/sessions/{id}", s.disconnect)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "not_found", "API route not found")
	})
	servedAssets := false
	if assets != nil {
		if _, err := fs.Stat(assets, "index.html"); err == nil {
			mux.Handle("/", http.FileServer(http.FS(assets)))
			servedAssets = true
		}
	}
	// An API-only development run works before the frontend is built.
	if !servedAssets {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "Build the frontend with npm --prefix web run build, then restart Go. For development, run npm --prefix web run dev.", http.StatusServiceUnavailable)
		})
	}
	return protect(mux, options)
}

func protect(next http.Handler, options Options) http.Handler {
	_, port, _ := net.SplitHostPort(options.Address)
	allowedHosts := map[string]bool{options.Address: true, "localhost:" + port: true}
	allowedOrigins := map[string]bool{"http://" + options.Address: true, "http://localhost:" + port: true}
	if options.Token == "" {
		allowedOrigins["http://127.0.0.1:5173"] = true
		allowedOrigins["http://localhost:5173"] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Checking Host also stops DNS rebinding to this local service.
		if !allowedHosts[r.Host] {
			fail(w, http.StatusForbidden, "invalid_host", "use the app's localhost address")
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && !allowedOrigins[origin] {
			fail(w, http.StatusForbidden, "invalid_origin", "requests from this origin are not allowed")
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			fail(w, http.StatusForbidden, "invalid_origin", "cross-site requests are not allowed")
			return
		}
		if options.Token != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-SCP-Desktop-Token")), []byte(options.Token)) != 1 {
			fail(w, http.StatusUnauthorized, "desktop_auth_required", "open this location in the desktop app")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("kind") == "sftp" && s.sessions != nil {
		listing, err := s.sessions.List(r.Context(), r.URL.Query().Get("sessionId"), r.URL.Query().Get("path"))
		if err != nil {
			s.serviceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, listing)
		return
	}
	if r.URL.Query().Get("kind") != "local" || r.URL.Query().Get("sessionId") != "" {
		fail(w, http.StatusBadRequest, "invalid_location", "S1 supports local locations only")
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = s.home
	}
	path, err := location.Directory(path)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_path", err.Error())
		return
	}
	entries, err := (location.Local{Base: path}).List(path)
	if err != nil {
		fail(w, http.StatusBadRequest, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Path    string           `json:"path"`
		Parent  string           `json:"parent"`
		Home    string           `json:"home"`
		Entries []location.Entry `json:"entries"`
	}{path, filepath.Dir(path), s.home, entries})
}

func (s *Server) startJob(w http.ResponseWriter, r *http.Request) {
	var req transfer.Request
	if !decode(w, r, &req) {
		return
	}
	job, err := s.jobs.Start(req)
	if err != nil {
		var conflict *transfer.Conflict
		if errors.As(err, &conflict) {
			writeJSON(w, http.StatusConflict, struct {
				Code    string   `json:"code"`
				Message string   `json:"message"`
				Names   []string `json:"names"`
			}{"exists", conflict.Error(), conflict.Names})
			return
		}
		fail(w, http.StatusBadRequest, "invalid_copy", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, job)
}

func decode(w http.ResponseWriter, r *http.Request, body any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "invalid_content_type", "use application/json")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		fail(w, http.StatusBadRequest, "invalid_request", err.Error())
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		fail(w, http.StatusBadRequest, "invalid_request", "provide one JSON object")
		return false
	}
	return true
}

func (s *Server) available(w http.ResponseWriter) bool {
	if s.connections == nil || s.sessions == nil {
		fail(w, http.StatusServiceUnavailable, "unavailable", "connection services are not configured")
		return false
	}
	return true
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	if s.available(w) {
		writeJSON(w, http.StatusOK, s.connections.List())
	}
}

func (s *Server) createConnection(w http.ResponseWriter, r *http.Request) { s.saveConnection(w, r, "") }
func (s *Server) updateConnection(w http.ResponseWriter, r *http.Request) {
	s.saveConnection(w, r, r.PathValue("id"))
}
func (s *Server) saveConnection(w http.ResponseWriter, r *http.Request, id string) {
	if !s.available(w) {
		return
	}
	var input connections.Input
	if !decode(w, r, &input) {
		return
	}
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	record, err := s.connections.Save(id, input)
	if err != nil {
		s.serviceError(w, err)
		return
	}
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	writeJSON(w, status, record)
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	if !s.available(w) {
		return
	}
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	id := r.PathValue("id")
	if s.sessions.InUse(id) {
		fail(w, http.StatusConflict, "in_use", "Disconnect this connection before deleting it.")
		return
	}
	if err := s.connections.Delete(id); err != nil {
		s.serviceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request) {
	if !s.available(w) {
		return
	}
	var input session.Request
	if !decode(w, r, &input) {
		return
	}
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	connected, err := s.sessions.Connect(r.Context(), input)
	if err != nil {
		s.serviceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, connected)
}

func (s *Server) disconnect(w http.ResponseWriter, r *http.Request) {
	if !s.available(w) {
		return
	}
	s.sessions.Disconnect(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) serviceError(w http.ResponseWriter, err error) {
	var host *knownhosts.Problem
	if errors.As(err, &host) {
		status := http.StatusPreconditionFailed
		if host.Code == "host_key_changed" {
			status = http.StatusConflict
		}
		writeJSON(w, status, host)
		return
	}
	var issue *session.Problem
	if errors.As(err, &issue) {
		status := http.StatusBadRequest
		switch issue.Code {
		case "auth_failed":
			status = http.StatusUnauthorized
		case "passphrase_required", "secret_required":
			status = http.StatusPreconditionRequired
		case "session_unavailable":
			status = http.StatusConflict
		case "connect_failed", "sftp_failed":
			status = http.StatusBadGateway
		}
		fail(w, status, issue.Code, issue.Message)
		return
	}
	if errors.Is(err, connections.ErrNotFound) {
		fail(w, http.StatusNotFound, "connection_not_found", err.Error())
		return
	}
	fail(w, http.StatusBadRequest, "connection_failed", err.Error())
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.jobs.Get(r.PathValue("id"))
	if !ok {
		fail(w, http.StatusNotFound, "job_not_found", "copy job not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
