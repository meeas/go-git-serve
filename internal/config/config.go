// Package config loads go-git-serve settings. Values are resolved in this
// order, later sources overriding earlier ones:
//
//	built-in defaults < TOML config file < GGS_* environment variables < CLI flags
//
// Secrets (tokens, passphrases, private keys) are never stored in the config
// file; it only references where to find them.
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// Auth methods.
const (
	AuthNone     = "none"
	AuthSSHKey   = "ssh-key"
	AuthSSHAgent = "ssh-agent"
	AuthToken    = "token"
)

// AuthMethods lists the valid values of auth.method.
var AuthMethods = []string{AuthNone, AuthSSHKey, AuthSSHAgent, AuthToken}

// Browser modes.
const (
	BrowserApp     = "app"
	BrowserDefault = "default"
	BrowserNone    = "none"
)

// BrowserModes lists the valid values of browser.mode.
var BrowserModes = []string{BrowserApp, BrowserDefault, BrowserNone}

// Environment variables holding secrets. They are read by the auth package,
// never written to disk.
const (
	EnvToken      = "GGS_AUTH_TOKEN"
	EnvPassphrase = "GGS_AUTH_PASSPHRASE"
)

// MinInterval is the shortest allowed periodic update interval.
const MinInterval = 30 * time.Second

// Config is the full set of settings.
type Config struct {
	Repo    Repo    `toml:"repo"`
	Auth    Auth    `toml:"auth"`
	Server  Server  `toml:"server"`
	Update  Update  `toml:"update"`
	Browser Browser `toml:"browser"`
}

// Repo describes which repository to serve.
type Repo struct {
	URL    string `toml:"url"`
	Branch string `toml:"branch"`
	Subdir string `toml:"subdir"`
	// Shallow clones and fetches only the latest commit (default true).
	Shallow bool `toml:"shallow"`
}

// Auth describes how to authenticate to the git server.
type Auth struct {
	Method    string `toml:"method"`
	KeyFile   string `toml:"key_file"`
	TokenFile string `toml:"token_file"`
}

// Server describes the local web server.
type Server struct {
	Listen string `toml:"listen"`
}

// Update controls when the repository is pulled.
type Update struct {
	OnStart  bool     `toml:"on_start"`
	Interval Duration `toml:"interval"`
}

// Browser controls how the site is shown.
type Browser struct {
	Mode string `toml:"mode"`
	Path string `toml:"path"`
}

// Duration is a time.Duration that reads and writes strings like "15m".
// An empty string or "0" means zero (disabled).
type Duration struct{ time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) {
	if d.Duration == 0 {
		return []byte("0"), nil
	}
	return []byte(d.Duration.String()), nil
}

// ParseDuration parses a duration, treating "" and "0" as zero.
func ParseDuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" {
		return 0, nil
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q (use values like 30s, 15m, 1h)", s)
	}
	return v, nil
}

// Default returns the built-in defaults.
func Default() Config {
	return Config{
		Repo:    Repo{Shallow: true},
		Server:  Server{Listen: "127.0.0.1:8888"},
		Update:  Update{OnStart: true},
		Browser: Browser{Mode: BrowserApp},
	}
}

// LoadFile decodes the TOML file at path on top of cfg. It returns
// found=false (and no error) when the file does not exist. Unknown keys are
// returned so the caller can warn about typos.
func LoadFile(path string, cfg *Config) (found bool, unknown []string, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return true, nil, fmt.Errorf("%s: %w", path, err)
	}
	for _, k := range md.Undecoded() {
		unknown = append(unknown, k.String())
	}
	return true, unknown, nil
}

// envVar maps an environment variable onto a config field.
type envVar struct {
	name string
	set  func(c *Config, v string) error
}

func setString(p func(c *Config) *string) func(*Config, string) error {
	return func(c *Config, v string) error { *p(c) = v; return nil }
}

// EnvVars lists every non-secret environment variable that overrides config.
var envVars = []envVar{
	{"GGS_REPO_URL", setString(func(c *Config) *string { return &c.Repo.URL })},
	{"GGS_REPO_BRANCH", setString(func(c *Config) *string { return &c.Repo.Branch })},
	{"GGS_REPO_SUBDIR", setString(func(c *Config) *string { return &c.Repo.Subdir })},
	{"GGS_REPO_SHALLOW", func(c *Config, v string) error {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("GGS_REPO_SHALLOW: %q is not true or false", v)
		}
		c.Repo.Shallow = b
		return nil
	}},
	{"GGS_AUTH_METHOD", setString(func(c *Config) *string { return &c.Auth.Method })},
	{"GGS_AUTH_KEY_FILE", setString(func(c *Config) *string { return &c.Auth.KeyFile })},
	{"GGS_AUTH_TOKEN_FILE", setString(func(c *Config) *string { return &c.Auth.TokenFile })},
	{"GGS_SERVER_LISTEN", setString(func(c *Config) *string { return &c.Server.Listen })},
	{"GGS_UPDATE_ON_START", func(c *Config, v string) error {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("GGS_UPDATE_ON_START: %q is not true or false", v)
		}
		c.Update.OnStart = b
		return nil
	}},
	{"GGS_UPDATE_INTERVAL", func(c *Config, v string) error {
		d, err := ParseDuration(v)
		if err != nil {
			return fmt.Errorf("GGS_UPDATE_INTERVAL: %w", err)
		}
		c.Update.Interval.Duration = d
		return nil
	}},
	{"GGS_BROWSER_MODE", setString(func(c *Config) *string { return &c.Browser.Mode })},
	{"GGS_BROWSER_PATH", setString(func(c *Config) *string { return &c.Browser.Path })},
}

// EnvNames returns the names of all non-secret override variables.
func EnvNames() []string {
	names := make([]string, len(envVars))
	for i, e := range envVars {
		names[i] = e.name
	}
	return names
}

// ApplyEnv overrides cfg with any GGS_* variables found by lookup
// (normally os.LookupEnv).
func ApplyEnv(cfg *Config, lookup func(string) (string, bool)) error {
	for _, e := range envVars {
		if v, ok := lookup(e.name); ok {
			if err := e.set(cfg, v); err != nil {
				return err
			}
		}
	}
	return nil
}

// Endpoint parses the repository URL, accepting https://, ssh://, scp-style
// (git@github.com:org/repo.git), file:// and local paths.
func Endpoint(rawURL string) (*transport.Endpoint, error) {
	if strings.TrimSpace(rawURL) == "" {
		return nil, errors.New("repository URL is not set (repo.url, GGS_REPO_URL or --repo)")
	}
	ep, err := transport.NewEndpoint(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid repository URL %q: %w", rawURL, err)
	}
	return ep, nil
}

// InferAuthMethod picks a sensible default when auth.method is unset:
// ssh-key if a key file is configured, ssh-agent for other SSH URLs,
// token if a token source is configured for HTTPS, otherwise none.
func InferAuthMethod(c Config) string {
	ep, err := Endpoint(c.Repo.URL)
	if err != nil {
		return AuthNone
	}
	switch ep.Protocol {
	case "ssh":
		if c.Auth.KeyFile != "" {
			return AuthSSHKey
		}
		return AuthSSHAgent
	case "https":
		if c.Auth.TokenFile != "" {
			return AuthToken
		}
		if _, ok := os.LookupEnv(EnvToken); ok {
			return AuthToken
		}
	}
	return AuthNone
}

// Validate normalises cfg (filling an inferred auth method, cleaning paths)
// and reports the first problem found.
func Validate(c *Config) error {
	ep, err := Endpoint(c.Repo.URL)
	if err != nil {
		return err
	}
	if ep.Password != "" {
		return errors.New("repository URL must not contain a password or token; use auth.method = \"token\" with a token file, GGS_AUTH_TOKEN or the OS keychain")
	}

	if c.Auth.Method == "" {
		c.Auth.Method = InferAuthMethod(*c)
	}
	if !contains(AuthMethods, c.Auth.Method) {
		return fmt.Errorf("auth.method %q is not one of %s", c.Auth.Method, strings.Join(AuthMethods, ", "))
	}
	switch c.Auth.Method {
	case AuthSSHKey, AuthSSHAgent:
		if ep.Protocol != "ssh" {
			return fmt.Errorf("auth.method %q needs an SSH repository URL (git@host:org/repo.git or ssh://...), got %s", c.Auth.Method, ep.Protocol)
		}
		if c.Auth.Method == AuthSSHKey && c.Auth.KeyFile == "" {
			return errors.New("auth.method \"ssh-key\" needs auth.key_file")
		}
	case AuthToken:
		if ep.Protocol != "https" {
			return fmt.Errorf("auth.method \"token\" needs an https:// repository URL, got %s", ep.Protocol)
		}
	}
	c.Auth.KeyFile = ExpandHome(c.Auth.KeyFile)
	c.Auth.TokenFile = ExpandHome(c.Auth.TokenFile)

	if c.Repo.Subdir != "" {
		sub := filepath.Clean(filepath.FromSlash(c.Repo.Subdir))
		if !filepath.IsLocal(sub) {
			return fmt.Errorf("repo.subdir %q must be a relative path inside the repository", c.Repo.Subdir)
		}
		if isGitDir(sub) {
			return errors.New("repo.subdir must not point into .git")
		}
		if sub == "." {
			sub = ""
		}
		c.Repo.Subdir = sub
	}

	if _, _, err := net.SplitHostPort(c.Server.Listen); err != nil {
		return fmt.Errorf("server.listen %q must be host:port, e.g. 127.0.0.1:8888", c.Server.Listen)
	}

	if c.Update.Interval.Duration < 0 {
		return errors.New("update.interval must not be negative")
	}
	if d := c.Update.Interval.Duration; d > 0 && d < MinInterval {
		return fmt.Errorf("update.interval %s is too short (minimum %s)", d, MinInterval)
	}

	if !contains(BrowserModes, c.Browser.Mode) {
		return fmt.Errorf("browser.mode %q is not one of %s", c.Browser.Mode, strings.Join(BrowserModes, ", "))
	}
	c.Browser.Path = ExpandHome(c.Browser.Path)
	return nil
}

func isGitDir(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if strings.EqualFold(part, ".git") {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ExpandHome replaces a leading "~" with the user's home directory.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") && !strings.HasPrefix(p, `~\`) {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}
