// Package web serves a small local interface for reading the vault.
//
// Read-mostly on purpose. Obsidian edits notes better than a browser form ever
// will, and everything an agent writes goes through the verbs — so what this
// offers is what neither does well: a log view with cross-cutting filters, a
// graph of how projects, tasks and repositories connect, the vault checks, and
// creating a project without going through a chat.
package web

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/sebastianhutter/agentctx/internal/config"
	"github.com/sebastianhutter/agentctx/internal/service"
	"github.com/sebastianhutter/agentctx/internal/store"
)

//go:embed templates/*.html static/*
var assets embed.FS

// pages are the page templates, each parsed into its own set.
var pages = []string{"index.html", "project.html", "task.html", "repo.html", "log.html", "new.html", "graph.html", "checks.html"}

// shared are parsed into every page's set.
var shared = []string{"templates/layout.html", "templates/entrylist.html", "templates/issuelist.html"}

// Server serves the vault over HTTP on the loopback interface.
type Server struct {
	configPath string
	version    string
	templates  map[string]*template.Template
	errorPage  *template.Template
	http       *http.Server
}

// New builds a Server.
//
// One template set per page, rather than one set for everything. Every page
// defines "content", and html/template keeps a single definition per name in a
// set — so parsing them together leaves whichever file was read last answering
// for all of them, and every URL renders the same page. Separate sets are the
// fix, and cheap: the shared layout is parsed once per page at startup.
func New(configPath, version string) (*Server, error) {
	s := &Server{
		configPath: configPath,
		version:    version,
		templates:  make(map[string]*template.Template, len(pages)),
	}

	for _, page := range pages {
		tmpl, err := template.New(page).Funcs(funcs()).ParseFS(assets, append(append([]string{}, shared...), "templates/"+page)...)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", page, err)
		}
		s.templates[page] = tmpl
	}

	errorPage, err := template.New("error.html").Funcs(funcs()).ParseFS(assets, "templates/error.html")
	if err != nil {
		return nil, fmt.Errorf("parse error page: %w", err)
	}
	s.errorPage = errorPage

	return s, nil
}

// Handler returns the routes, exported so tests can exercise them without a
// listening socket.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /project/{project}", s.handleProject)
	mux.HandleFunc("GET /project/{project}/task/{task}", s.handleTask)
	mux.HandleFunc("GET /project/{project}/repo/{repo...}", s.handleRepoNote)
	mux.HandleFunc("GET /t/{task}", s.handleTaskRedirect)
	mux.HandleFunc("GET /log", s.handleLog)
	mux.HandleFunc("GET /graph", s.handleGraph)
	mux.HandleFunc("GET /graph.json", s.handleGraphJSON)
	mux.HandleFunc("GET /checks", s.handleChecks)
	mux.HandleFunc("GET /new", s.handleNewProjectForm)
	mux.HandleFunc("POST /new", s.handleNewProject)

	static, err := fsSub(assets, "static")
	if err == nil {
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(static))))
	}

	return localOnly(mux)
}

// localOnly rejects requests that did not arrive addressed to this machine.
//
// The listener is already bound to loopback, which stops another machine
// connecting. It does not stop a page in your browser resolving a name it
// controls to 127.0.0.1 and reading the vault through you — DNS rebinding, the
// standard way a local-only service with no auth gets read by a web page. A
// Host check costs nothing and closes it, which is worth doing before pointing
// a browser at a folder of everything you know.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		switch strings.ToLower(host) {
		case "localhost", "127.0.0.1", "[::1]", "::1", "":
		default:
			http.Error(w, "agentctx only answers to localhost", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Start begins serving on the given port.
func (s *Server) Start(port int) error {
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("port %d unavailable: %w", port, err)
	}

	s.http = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	slog.Info("starting web interface", "url", fmt.Sprintf("http://localhost:%d/", port))

	go func() {
		if err := s.http.Serve(ln); err != nil && err != http.ErrServerClosed {
			slog.Error("web interface stopped", "err", err)
		}
	}()
	return nil
}

// Stop shuts the server down.
func (s *Server) Stop() {
	if s.http == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.http.Shutdown(ctx); err != nil {
		slog.Error("web shutdown error", "err", err)
	}
	s.http = nil
	slog.Info("web interface stopped")
}

// newService loads configuration per request, so an edited config takes effect
// without a restart. A request is rare and a vault is small.
func (s *Server) newService() (*service.Service, error) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return nil, err
	}
	svc, err := service.New(cfg)
	if err != nil {
		return nil, err
	}
	return svc.WithTransport(service.TransportCLI), nil
}

func (s *Server) newStore() (*store.Store, *config.Config, error) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		return nil, nil, err
	}
	if errs := cfg.Validate(); len(errs) > 0 {
		return nil, cfg, errs[0]
	}
	return store.New(cfg.ExpandedVaultPath()), cfg, nil
}

// render writes a page, reporting a template failure rather than a blank page.
//
// Renders into a buffer first: a template that fails halfway would otherwise
// have already written a broken page, leaving no way to report the error.
func (s *Server) render(w http.ResponseWriter, name string, data any) {
	tmpl, ok := s.templates[name]
	if !ok {
		s.fail(w, fmt.Errorf("no template %q", name), http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.ExecuteTemplate(&buf, "layout", data); err != nil {
		slog.Error("render failed", "template", name, "err", err)
		s.fail(w, fmt.Errorf("rendering %s: %w", name, err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(buf.Bytes())
}

func (s *Server) fail(w http.ResponseWriter, err error, code int) {
	slog.Warn("request failed", "err", err, "code", code)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	if renderErr := s.errorPage.ExecuteTemplate(w, "error.html", map[string]any{
		"Title": http.StatusText(code),
		"Error": err.Error(),
	}); renderErr != nil {
		fmt.Fprintf(w, "<p>%s</p>", template.HTMLEscapeString(err.Error()))
	}
}
