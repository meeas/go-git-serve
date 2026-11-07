// Package server serves the checked-out site over HTTP.
package server

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
)

// OpenRoot opens dir as a filesystem confined to that directory: symlinks
// in the repository cannot be used to read files outside it.
func OpenRoot(dir string) (*os.Root, error) {
	fi, err := os.Stat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	return os.OpenRoot(dir)
}

// AppHeader identifies a go-git-serve response and names the app, so a
// second copy can recognise a running instance of itself.
const AppHeader = "X-Go-Git-Serve"

// Site is the HTTP handler. Until SetFiles is called it answers with a
// short "starting" page, so the address is claimed (and recognisable)
// while the repository is still being cloned or pulled.
//
// It never serves anything inside a .git directory, and when allowedHosts
// is non-empty it rejects requests whose Host header names another host
// (DNS-rebinding protection).
type Site struct {
	app     string
	allowed map[string]bool
	files   atomic.Pointer[http.Handler]
}

// NewSite returns a Site for app that accepts the given Host names.
func NewSite(app string, allowedHosts []string) *Site {
	s := &Site{app: app, allowed: map[string]bool{}}
	for _, h := range allowedHosts {
		s.allowed[strings.ToLower(h)] = true
	}
	return s
}

// SetFiles starts serving fsys.
func (s *Site) SetFiles(fsys fs.FS) {
	h := http.FileServerFS(fsys)
	s.files.Store(&h)
}

// Handler is a Site already serving fsys (convenience for tests).
func Handler(app string, fsys fs.FS, allowedHosts []string) http.Handler {
	s := NewSite(app, allowedHosts)
	s.SetFiles(fsys)
	return s
}

const startingPage = `<!doctype html><meta charset="utf-8"><meta http-equiv="refresh" content="2">
<title>Starting...</title><body style="font-family:sans-serif;margin:3em">Fetching the latest version, one moment...</body>`

func (s *Site) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set(AppHeader, s.app)
	if len(s.allowed) > 0 && !s.allowed[hostOnly(r.Host)] {
		http.Error(w, "unknown host", http.StatusMisdirectedRequest)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		h.Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Content-Type-Options", "nosniff")
	files := s.files.Load()
	if files == nil {
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Retry-After", "2")
		w.WriteHeader(http.StatusServiceUnavailable)
		if r.Method == http.MethodGet {
			fmt.Fprint(w, startingPage)
		}
		return
	}
	if hasGitComponent(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	(*files).ServeHTTP(w, r)
}

func hostOnly(hostport string) string {
	h := hostport
	if host, _, err := net.SplitHostPort(hostport); err == nil {
		h = host
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

// hasGitComponent reports whether any path segment is ".git" (case- and
// separator-insensitive, for Windows and macOS filesystems).
func hasGitComponent(p string) bool {
	p = strings.ReplaceAll(p, `\`, "/")
	for _, part := range strings.Split(p, "/") {
		part = strings.TrimRight(part, ". ") // Windows ignores trailing dots/spaces
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}

// AllowedHosts returns the Host header values accepted for a listen address.
// It returns nil (no check) when listening on all interfaces, because any
// of the machine's names may then be used legitimately.
func AllowedHosts(listen string) []string {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return nil
	}
	host = strings.ToLower(host)
	if host == "" || host == "0.0.0.0" || host == "::" {
		return nil
	}
	if host == "localhost" || isLoopback(host) {
		return []string{"localhost", "127.0.0.1", "::1", host}
	}
	return []string{host}
}

// IsWildcard reports whether listen binds to all interfaces.
func IsWildcard(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	return err == nil && (host == "" || host == "0.0.0.0" || host == "::")
}

func isLoopback(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// URLFor returns the browser URL for a configured listen address.
func URLFor(listen string) string {
	host, port, _ := net.SplitHostPort(listen)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}

// URL returns the browser URL for a bound listener address.
func URL(addr net.Addr, configured string) string {
	host, _, _ := net.SplitHostPort(configured)
	_, port, _ := net.SplitHostPort(addr.String())
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}
