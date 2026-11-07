package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPrecedenceFileThenEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.toml")
	os.WriteFile(path, []byte(`
[repo]
url = "https://github.com/org/app.git"
branch = "main"
[server]
listen = "127.0.0.1:9000"
[update]
interval = "15m"
typo = 1
`), 0o600)

	cfg := Default()
	found, unknown, err := LoadFile(path, &cfg)
	if err != nil || !found {
		t.Fatalf("LoadFile: found=%v err=%v", found, err)
	}
	if len(unknown) != 1 || unknown[0] != "update.typo" {
		t.Errorf("unknown = %v, want [update.typo]", unknown)
	}
	if cfg.Browser.Mode != BrowserApp || !cfg.Update.OnStart {
		t.Errorf("defaults lost: %+v", cfg)
	}
	if cfg.Update.Interval.Duration != 15*time.Minute {
		t.Errorf("interval = %v", cfg.Update.Interval)
	}

	if !cfg.Repo.Shallow || !cfg.Update.RandomizeStart {
		t.Error("shallow and randomize_start should default to true")
	}
	env := map[string]string{"GGS_SERVER_LISTEN": "127.0.0.1:9100", "GGS_UPDATE_ON_START": "false", "GGS_REPO_SHALLOW": "false", "GGS_UPDATE_RANDOMIZE_START": "false"}
	if err := ApplyEnv(&cfg, func(k string) (string, bool) { v, ok := env[k]; return v, ok }); err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Listen != "127.0.0.1:9100" || cfg.Update.OnStart || cfg.Repo.Shallow || cfg.Update.RandomizeStart {
		t.Errorf("env not applied: %+v", cfg)
	}
	if cfg.Repo.Branch != "main" {
		t.Errorf("file value lost: %+v", cfg.Repo)
	}
}

func TestLoadMissingFile(t *testing.T) {
	cfg := Default()
	found, _, err := LoadFile(filepath.Join(t.TempDir(), "nope.toml"), &cfg)
	if found || err != nil {
		t.Fatalf("found=%v err=%v", found, err)
	}
}

func TestApplyEnvBadValues(t *testing.T) {
	for k, v := range map[string]string{"GGS_UPDATE_ON_START": "maybe", "GGS_UPDATE_INTERVAL": "soon", "GGS_REPO_SHALLOW": "sometimes", "GGS_UPDATE_RANDOMIZE_START": "kinda"} {
		cfg := Default()
		err := ApplyEnv(&cfg, func(n string) (string, bool) { return v, n == k })
		if err == nil {
			t.Errorf("%s=%s: expected error", k, v)
		}
	}
}

func valid() Config {
	c := Default()
	c.Repo.URL = "https://github.com/org/app.git"
	return c
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mod     func(*Config)
		wantErr string
	}{
		{"ok", func(c *Config) {}, ""},
		{"no url", func(c *Config) { c.Repo.URL = "" }, "not set"},
		{"password in url", func(c *Config) { c.Repo.URL = "https://u:secret@github.com/org/app.git" }, "must not contain a password"},
		{"bad method", func(c *Config) { c.Auth.Method = "magic" }, "not one of"},
		{"token over ssh", func(c *Config) { c.Repo.URL = "git@github.com:org/app.git"; c.Auth.Method = AuthToken }, "https://"},
		{"ssh-key over https", func(c *Config) { c.Auth.Method = AuthSSHKey; c.Auth.KeyFile = "k" }, "SSH repository URL"},
		{"ssh-key no file", func(c *Config) { c.Repo.URL = "git@github.com:org/app.git"; c.Auth.Method = AuthSSHKey }, "key_file"},
		{"subdir escape", func(c *Config) { c.Repo.Subdir = "../etc" }, "inside the repository"},
		{"subdir abs", func(c *Config) { c.Repo.Subdir = "/etc" }, "inside the repository"},
		{"subdir git", func(c *Config) { c.Repo.Subdir = "docs/.GIT" }, ".git"},
		{"listen", func(c *Config) { c.Server.Listen = "8888" }, "host:port"},
		{"interval short", func(c *Config) { c.Update.Interval.Duration = time.Second }, "too short"},
		{"browser", func(c *Config) { c.Browser.Mode = "kiosk" }, "browser.mode"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := valid()
			tt.mod(&c)
			err := Validate(&c)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestInferAuthMethod(t *testing.T) {
	t.Setenv(EnvToken, "")
	os.Unsetenv(EnvToken)
	tests := []struct {
		url, key, tokenFile, want string
	}{
		{"git@github.com:org/app.git", "", "", AuthSSHAgent},
		{"ssh://git@github.com/org/app.git", "~/.ssh/k", "", AuthSSHKey},
		{"https://github.com/org/app.git", "", "", AuthNone},
		{"https://github.com/org/app.git", "", "/t", AuthToken},
		{"/srv/git/app", "", "", AuthNone},
	}
	for _, tt := range tests {
		c := Default()
		c.Repo.URL, c.Auth.KeyFile, c.Auth.TokenFile = tt.url, tt.key, tt.tokenFile
		if got := InferAuthMethod(c); got != tt.want {
			t.Errorf("%s: got %s, want %s", tt.url, got, tt.want)
		}
	}
}

func TestRenderRoundTrip(t *testing.T) {
	c := valid()
	c.Repo.Branch = "gh-pages"
	c.Repo.Subdir = "site"
	c.Auth.Method = AuthToken
	c.Auth.TokenFile = `C:\Users\me\app.token`
	c.Update.Interval.Duration = 5 * time.Minute

	path := filepath.Join(t.TempDir(), "x", "app.toml")
	if err := Write(path, c, false); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, c, false); err == nil {
		t.Error("Write without overwrite replaced an existing file")
	}
	got := Default()
	if _, unknown, err := LoadFile(path, &got); err != nil || len(unknown) > 0 {
		t.Fatalf("reload: %v %v", err, unknown)
	}
	if got != c {
		t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, c)
	}
	if fi, _ := os.Stat(path); fi != nil && fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("config file mode %v is too open", fi.Mode().Perm())
	}
}

func TestDefaultTemplateFailsValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.toml")
	if err := Write(path, Default(), false); err != nil {
		t.Fatal(err)
	}
	c := Default()
	if _, _, err := LoadFile(path, &c); err != nil {
		t.Fatal(err)
	}
	if err := Validate(&c); err == nil || !strings.Contains(err.Error(), "not set") {
		t.Errorf("template should require repo.url, got %v", err)
	}
}

func TestAppName(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/local/bin/dashboard": "dashboard",
		`C:\apps\docs.exe`:         "docs",
		"go-git-serve":             "go-git-serve",
	} {
		if os.PathSeparator == '/' && strings.Contains(in, `\`) {
			continue
		}
		if got := AppName(in); got != want {
			t.Errorf("AppName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInlineSecrets(t *testing.T) {
	// Inferred methods.
	c := Default()
	c.Repo.URL, c.Auth.Token = "https://github.com/org/app.git", "tok"
	if err := Validate(&c); err != nil || c.Auth.Method != AuthToken {
		t.Fatalf("inline token: method=%q err=%v", c.Auth.Method, err)
	}
	c = Default()
	c.Repo.URL, c.Auth.Key = "git@github.com:org/app.git", "-----BEGIN OPENSSH PRIVATE KEY-----\nabc\n-----END OPENSSH PRIVATE KEY-----\n"
	if err := Validate(&c); err != nil || c.Auth.Method != AuthSSHKey {
		t.Fatalf("inline key: method=%q err=%v", c.Auth.Method, err)
	}
	// Not both inline and file.
	c.Auth.KeyFile = "~/.ssh/k"
	if err := Validate(&c); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Errorf("key + key_file: %v", err)
	}
	c = valid()
	c.Auth.Token, c.Auth.TokenFile = "t", "/f"
	if err := Validate(&c); err == nil || !strings.Contains(err.Error(), "only one") {
		t.Errorf("token + token_file: %v", err)
	}

	// Round trip through the rendered file, including a multi-line key.
	c = valid()
	c.Auth.Method = AuthSSHKey
	c.Repo.URL = "git@github.com:org/app.git"
	c.Auth.Key = "-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\nBBBB\n-----END OPENSSH PRIVATE KEY-----\n"
	got := Default()
	if _, err := Parse(Render(c), &got); err != nil {
		t.Fatal(err)
	}
	if got.Auth.Key != c.Auth.Key {
		t.Errorf("key round trip:\n%q\n%q", got.Auth.Key, c.Auth.Key)
	}

	// Redaction hides both.
	c.Auth.Token = "secret-token"
	r := Render(c.Redacted())
	if strings.Contains(r, "AAAA") || strings.Contains(r, "secret-token") {
		t.Errorf("redacted render leaks a secret:\n%s", r)
	}
	if !c.HasInlineSecret() || Default().HasInlineSecret() {
		t.Error("HasInlineSecret wrong")
	}
}

func TestWriteRaw(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "app.toml")
	if err := WriteRaw(path, []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := WriteRaw(path, []byte("b")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "b" {
		t.Errorf("content = %q", b)
	}
	if fi, _ := os.Stat(path); os.PathSeparator == '/' && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode().Perm())
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".config-*")); len(m) > 0 {
		t.Errorf("temp files left: %v", m)
	}
}
