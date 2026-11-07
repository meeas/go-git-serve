// Package server serves the checked-out site over HTTP.
package server

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
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

// Handler serves files from fsys. It never serves anything inside a .git
// directory, and when allowedHosts is non-empty it rejects requests whose
// Host header names another host (DNS-rebinding protection).
func Handler(fsys fs.FS, allowedHosts []string) http.Handler {
	files := http.FileServerFS(fsys)
	allowed := map[string]bool{}
	for _, h := range allowedHosts {
		allowed[strings.ToLower(h)] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(allowed) > 0 && !allowed[hostOnly(r.Host)] {
			http.Error(w, "unknown host", http.StatusMisdirectedRequest)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if hasGitComponent(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Content-Type-Options", "nosniff")
		files.ServeHTTP(w, r)
	})
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

// URL returns the browser URL for a bound listener address.
func URL(addr net.Addr, configured string) string {
	host, _, _ := net.SplitHostPort(configured)
	_, port, _ := net.SplitHostPort(addr.String())
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port) + "/"
}
