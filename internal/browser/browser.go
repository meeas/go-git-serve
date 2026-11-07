// Package browser opens the served site: in a chromeless "app" window using
// an installed Chromium-family browser, in the default browser, or not at all.
package browser

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/meeas/go-git-serve/internal/config"
	"github.com/meeas/go-git-serve/internal/logx"
)

// handoffWindow is how soon after launch a browser exit is taken to mean
// "handed the window to an already-running browser" rather than "closed".
const handoffWindow = 3 * time.Second

// Session is an opened browser window.
type Session struct {
	closed chan struct{}
}

// Closed is closed when the app window is closed by the user. It is nil
// (blocks forever) when closing cannot be detected, e.g. default-browser mode.
func (s *Session) Closed() <-chan struct{} {
	if s == nil {
		return nil
	}
	return s.closed
}

// Options configures Open.
type Options struct {
	Mode       string // config.Browser* constant
	Path       string // explicit browser binary for app mode
	ProfileDir string // dedicated profile so the window is its own process
}

// Open shows url according to opts.Mode. Failure to open a browser is not
// fatal: the URL has already been printed.
func Open(url string, opts Options) *Session {
	switch opts.Mode {
	case config.BrowserNone:
		return nil
	case config.BrowserApp:
		if s, err := openApp(url, opts); err == nil {
			return s
		} else {
			logx.Infof("App window unavailable (%v); using the default browser", err)
		}
	}
	if err := openDefault(url); err != nil {
		logx.Infof("Could not open a browser (%v); open the URL above manually", err)
	}
	return nil
}

var errNoDisplay = errors.New("no graphical display")

func hasDisplay() bool {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func openApp(url string, opts Options) (*Session, error) {
	if !hasDisplay() {
		return nil, errNoDisplay
	}
	bin := opts.Path
	if bin == "" {
		bin = FindChromium()
	}
	if bin == "" {
		return nil, errors.New("no Chrome, Edge, Chromium or Brave found; set browser.path")
	}
	if err := os.MkdirAll(opts.ProfileDir, 0o700); err != nil {
		return nil, err
	}
	cmd := exec.Command(bin,
		"--app="+url,
		"--user-data-dir="+opts.ProfileDir,
		"--no-first-run",
		"--no-default-browser-check",
	)
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	logx.Debugf("launched %s (pid %d)", bin, cmd.Process.Pid)
	s := &Session{closed: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		if time.Since(start) < handoffWindow {
			logx.Infof("Browser handed the window to an existing process; press Ctrl+C to stop serving")
			return
		}
		close(s.closed)
	}()
	return s, nil
}

func openDefault(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		if !hasDisplay() {
			return errNoDisplay
		}
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap
	return nil
}

// FindChromium returns the path of an installed Chromium-family browser
// that supports --app, or "" if none is found.
func FindChromium() string {
	for _, c := range candidates() {
		if filepath.IsAbs(c) {
			if fi, err := os.Stat(c); err == nil && !fi.IsDir() {
				return c
			}
			continue
		}
		if p, err := exec.LookPath(c); err == nil {
			return p
		}
	}
	return ""
}

func candidates() []string {
	switch runtime.GOOS {
	case "windows":
		var out []string
		rels := []string{
			`Google\Chrome\Application\chrome.exe`,
			`Microsoft\Edge\Application\msedge.exe`,
			`Chromium\Application\chrome.exe`,
			`BraveSoftware\Brave-Browser\Application\brave.exe`,
		}
		for _, rel := range rels {
			for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
				if base := os.Getenv(env); base != "" {
					out = append(out, filepath.Join(base, rel))
				}
			}
		}
		return out
	case "darwin":
		apps := []string{
			"Google Chrome.app/Contents/MacOS/Google Chrome",
			"Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"Chromium.app/Contents/MacOS/Chromium",
			"Brave Browser.app/Contents/MacOS/Brave Browser",
		}
		var out []string
		home, _ := os.UserHomeDir()
		for _, a := range apps {
			out = append(out, filepath.Join("/Applications", a))
			if home != "" {
				out = append(out, filepath.Join(home, "Applications", a))
			}
		}
		return out
	default:
		return []string{
			"google-chrome", "google-chrome-stable", "chromium", "chromium-browser",
			"microsoft-edge", "microsoft-edge-stable", "brave-browser",
		}
	}
}
