package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Render produces a commented TOML config file for c. Optional settings that
// are unset are written commented out so users can see what is available.
func Render(c Config) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	kv := func(key, val, comment string) {
		s := fmt.Sprintf("%s = %s", key, val)
		if comment != "" {
			s = fmt.Sprintf("%-40s # %s", s, comment)
		}
		line("%s", s)
	}
	opt := func(key, val, example, comment string) {
		if val != "" {
			kv(key, q(val), comment)
			return
		}
		kv("# "+key, q(example), comment)
	}

	line("# go-git-serve configuration")
	line("# Settings can be overridden by GGS_* environment variables and command-line flags.")
	line("# Secrets: prefer a key/token file, GGS_AUTH_TOKEN or the OS keychain (`<app> auth set-token`).")
	line("# To hand out a single file (e.g. to a class), auth.key or auth.token can hold them inline;")
	line("# then treat this file like the credential itself.")
	line("")
	line("[repo]")
	if c.Repo.URL != "" {
		kv("url", q(c.Repo.URL), "https://..., git@host:org/repo.git, or ssh://...")
	} else {
		kv("url", q(""), "REQUIRED, e.g. 'git@github.com:org/app.git'")
	}
	opt("branch", c.Repo.Branch, "main", "default: the repository's default branch")
	opt("subdir", filepath.ToSlash(c.Repo.Subdir), "dist", "serve only this folder; default: repo root")
	kv("shallow", strconv.FormatBool(c.Repo.Shallow), "latest commit only; false keeps full history")
	line("")
	line("[auth]")
	opt("method", c.Auth.Method, AuthSSHAgent, "none | ssh-key | ssh-agent | token; default: guessed from url")
	opt("key_file", c.Auth.KeyFile, "~/.ssh/app_deploy_key", "ssh-key: private key; passphrase prompted or GGS_AUTH_PASSPHRASE")
	opt("token_file", c.Auth.TokenFile, "~/.config/app.token", "token: else GGS_AUTH_TOKEN, else OS keychain")
	if c.Auth.Key != "" {
		line("key = %s", multiline(c.Auth.Key))
	} else {
		line("# key = '''<contents of the private key file>'''  # ssh-key: inline instead of key_file")
	}
	opt("token", c.Auth.Token, "github_pat_...", "token: inline instead of token_file")
	line("")
	line("[server]")
	kv("listen", q(c.Server.Listen), "keep on 127.0.0.1 unless you mean to share it")
	line("")
	line("[update]")
	kv("on_start", strconv.FormatBool(c.Update.OnStart), "pull when starting")
	iv, _ := c.Update.Interval.MarshalText()
	kv("interval", q(string(iv)), "re-pull periodically, e.g. \"15m\"; \"0\" disables")
	kv("randomize_start", strconv.FormatBool(c.Update.RandomizeStart), "first re-pull at a random time within the first interval")
	line("")
	line("[browser]")
	kv("mode", q(c.Browser.Mode), "app (chromeless window) | default | none")
	opt("path", c.Browser.Path, "/path/to/chrome", "app mode: browser binary; default: auto-detect")
	return b.String()
}

// q quotes a string as a TOML literal string when possible (so Windows paths
// need no escaping), falling back to a basic string.
func q(s string) string {
	if !strings.ContainsAny(s, "'\n\r") {
		return "'" + s + "'"
	}
	return strconv.Quote(s)
}

// multiline renders s as a TOML multi-line literal string (no escaping, so
// a pasted private key stays readable), falling back to a basic string.
func multiline(s string) string {
	const delim = "'''"
	if !strings.Contains(s, delim) && !strings.Contains(s, "\r") {
		return delim + "\n" + strings.TrimRight(s, "\n") + "\n" + delim
	}
	return strconv.Quote(s)
}

// Write saves the rendered config to path with owner-only permissions. It
// refuses to overwrite an existing file unless overwrite is set.
func Write(path string, c Config, overwrite bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if !overwrite {
		flags |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(Render(c)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// WriteRaw atomically replaces path with data (owner-only permissions):
// written to a temporary file in the same folder, then renamed over the
// target, so a crash never leaves a half-written configuration.
func WriteRaw(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after a successful rename
	// os.CreateTemp creates the file with owner-only (0600) permissions.
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
