// Package dashboard serves a read-only HTTP dashboard showing a point-in-time
// snapshot of the pipeline. All requests require a Bearer token.
package dashboard

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

//go:embed static
var staticFiles embed.FS

// SnapshotFunc returns the current pipeline snapshot as JSON-serializable data.
type SnapshotFunc func() any

// Server is the dashboard HTTP server.
type Server struct {
	srv   *http.Server
	token string
}

// New creates a dashboard server bound to addr, gated by the given token.
// snapshotFn is called on each GET /api/snapshot to produce the response payload.
func New(addr, token string, snapshotFn SnapshotFunc) *Server {
	mux := http.NewServeMux()

	s := &Server{
		srv: &http.Server{
			Addr:    addr,
			Handler: mux,
		},
		token: token,
	}

	mux.Handle("/api/snapshot", s.requireAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(snapshotFn())
	})))

	staticSub, _ := fs.Sub(staticFiles, "static")
	fileServer := http.FileServer(http.FS(staticSub))
	mux.Handle("/", s.requireAuth(fileServer))

	return s
}

// ListenAndServe starts listening. It blocks until the server is shut down.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.srv.Addr)
	if err != nil {
		return err
	}
	slog.Info("dashboard listening", "addr", ln.Addr().String())
	return s.srv.Serve(ln)
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// Handler returns the server's HTTP handler (for testing with httptest).
func (s *Server) Handler() http.Handler {
	return s.srv.Handler
}

// requireAuth returns middleware that checks for a valid Bearer token in the
// Authorization header, or a ?token= query parameter (convenience for browser
// page loads).
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token != s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
