package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"path/filepath"

	"scp-client/internal/location"
	"scp-client/internal/transfer"
)

type Server struct {
	home string
	jobs *transfer.Manager
}

func New(home string, assets fs.FS) http.Handler {
	s := &Server{home: home, jobs: transfer.NewManager()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/list", s.list)
	mux.HandleFunc("POST /api/jobs", s.startJob)
	mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
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
	return protect(mux)
}

func protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Checking Host also stops DNS rebinding to this local service.
		if r.Host != "127.0.0.1:8787" && r.Host != "localhost:8787" {
			fail(w, http.StatusForbidden, "invalid_host", "use localhost:8787 or 127.0.0.1:8787")
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && origin != "http://127.0.0.1:8787" && origin != "http://localhost:8787" && origin != "http://127.0.0.1:5173" && origin != "http://localhost:5173" {
			fail(w, http.StatusForbidden, "invalid_origin", "requests from this origin are not allowed")
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			fail(w, http.StatusForbidden, "invalid_origin", "cross-site requests are not allowed")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
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
	if r.Header.Get("Content-Type") != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "invalid_content_type", "use application/json")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var req transfer.Request
	if err := decoder.Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		fail(w, http.StatusBadRequest, "invalid_request", "provide one JSON object")
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
