// Package editor provides a local HTTP server that serves a browser-based
// visual workflow editor for apiary.yaml files. The server exposes a JSON API
// used by the single-page application embedded in static/index.html.
//
// The canonical source of truth is always the YAML file; the visual editor is
// a read-write authoring surface that round-trips through the config package.
package editor

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"

	"github.com/orlandoburli/apiary/internal/config"
)

//go:embed static
var staticFiles embed.FS

// Server is the editor HTTP server. Create one with NewServer and start it
// with Start. The server is safe for concurrent use once started.
type Server struct {
	configPath string
	cfg        *config.Config
	rawYAML    []byte
	mux        *http.ServeMux
}

// NewServer creates an editor Server for the given config file. The file is
// read immediately so rawYAML holds the pre-edit content for diff generation.
func NewServer(configPath string, cfg *config.Config) (*Server, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}
	s := &Server{
		configPath: configPath,
		cfg:        cfg,
		rawYAML:    raw,
		mux:        http.NewServeMux(),
	}
	s.registerRoutes()
	return s, nil
}

func (s *Server) registerRoutes() {
	// Static assets (the SPA).
	sub, _ := fs.Sub(staticFiles, "static")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))

	// JSON API.
	s.mux.HandleFunc("/api/config", s.handleGetConfig)
	s.mux.HandleFunc("/api/render", s.handleRender)
	s.mux.HandleFunc("/api/validate", s.handleValidate)
	s.mux.HandleFunc("/api/diff", s.handleDiff)
	s.mux.HandleFunc("/api/save", s.handleSave)
}

// Start binds to a random loopback port and begins serving requests. It
// returns the URL (http://127.0.0.1:<port>) immediately. The server shuts
// down when ctx is cancelled.
func (s *Server) Start(ctx context.Context) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("listening: %w", err)
	}
	url := "http://" + ln.Addr().String()
	srv := &http.Server{Handler: s.mux}

	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	go func() { _ = srv.Serve(ln) }()

	return url, nil
}
