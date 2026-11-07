package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setup(t *testing.T) (*os.Root, string) {
	t.Helper()
	dir := t.TempDir()
	site := filepath.Join(dir, "site")
	must(t, os.MkdirAll(filepath.Join(site, ".git"), 0o755))
	must(t, os.MkdirAll(filepath.Join(site, "sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(site, "index.html"), []byte("hello"), 0o644))
	must(t, os.WriteFile(filepath.Join(site, ".git", "config"), []byte("secret"), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "outside.txt"), []byte("outside"), 0o644))
	if runtime.GOOS != "windows" {
		must(t, os.Symlink(filepath.Join(dir, "outside.txt"), filepath.Join(site, "escape.txt")))
	}
	root, err := OpenRoot(site)
	must(t, err)
	t.Cleanup(func() { root.Close() })
	return root, site
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func get(h http.Handler, method, host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "http://"+host+path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHandler(t *testing.T) {
	root, _ := setup(t)
	h := Handler(root.FS(), AllowedHosts("127.0.0.1:8888"))

	tests := []struct {
		name, method, host, path string
		want                     int
	}{
		{"index", "GET", "127.0.0.1:8888", "/", 200},
		{"localhost alias", "GET", "localhost:8888", "/index.html", 301}, // FileServer redirects /index.html -> /
		{"head", "HEAD", "127.0.0.1:8888", "/", 200},
		{"git dir", "GET", "127.0.0.1:8888", "/.git/config", 404},
		{"git dir case", "GET", "127.0.0.1:8888", "/.GIT/config", 404},
		{"git nested", "GET", "127.0.0.1:8888", "/sub/../.git/config", 404},
		{"git encoded", "GET", "127.0.0.1:8888", "/%2egit/config", 404},
		{"rebinding host", "GET", "evil.example:8888", "/", http.StatusMisdirectedRequest},
		{"post", "POST", "127.0.0.1:8888", "/", 405},
	}
	if runtime.GOOS != "windows" {
		tests = append(tests, struct {
			name, method, host, path string
			want                     int
		}{"symlink escape", "GET", "127.0.0.1:8888", "/escape.txt", 500})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(h, tt.method, tt.host, tt.path)
			if tt.name == "symlink escape" {
				if rec.Code == 200 || rec.Body.String() == "outside" {
					t.Fatalf("symlink escaped root: %d %q", rec.Code, rec.Body.String())
				}
				return
			}
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}

	rec := get(h, "GET", "127.0.0.1:8888", "/")
	if rec.Body.String() != "hello" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("unexpected response: %q %v", rec.Body.String(), rec.Header())
	}
}

func TestAllowedHosts(t *testing.T) {
	if AllowedHosts("0.0.0.0:80") != nil || AllowedHosts(":80") != nil {
		t.Error("wildcard listen should disable host checking")
	}
	got := AllowedHosts("192.168.1.5:80")
	if len(got) != 1 || got[0] != "192.168.1.5" {
		t.Errorf("got %v", got)
	}
	h := Handler(os.DirFS(t.TempDir()), AllowedHosts("[::1]:8888"))
	if rec := get(h, "GET", "[::1]:8888", "/"); rec.Code == http.StatusMisdirectedRequest {
		t.Error("IPv6 loopback host rejected")
	}
}
