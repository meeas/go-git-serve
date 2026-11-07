package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"

	"github.com/meeas/go-git-serve/internal/config"
)

// GitHub's documented fingerprints; guards against typos in the pinned keys.
func TestPinnedGitHubFingerprints(t *testing.T) {
	want := map[string]string{
		"ssh-ed25519":         "SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU",
		"ecdsa-sha2-nistp256": "SHA256:p2QAMXNIC1TJYWeIOttrVc98/R1BUFWu3/LiyKgUfQM",
		"ssh-rsa":             "SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s",
	}
	pinned := pinnedKeys()
	for _, host := range []string{"github.com", "[ssh.github.com]:443"} {
		keys := pinned[host]
		if len(keys) != 3 {
			t.Fatalf("%s: %d keys, want 3", host, len(keys))
		}
		for _, k := range keys {
			if got := ssh.FingerprintSHA256(k); got != want[k.Type()] {
				t.Errorf("%s %s: fingerprint %s, want %s", host, k.Type(), got, want[k.Type()])
			}
		}
	}
}

func newKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func TestHostKeyCallback(t *testing.T) {
	addr := &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 22}
	pinned := pinnedKeys()
	var gh ssh.PublicKey
	for _, k := range pinned["github.com"] {
		if k.Type() == "ssh-ed25519" {
			gh = k
		}
	}

	// No known_hosts: GitHub verified by pinned keys; impostor and unknown hosts refused.
	cb := newHostKeyCallback(nil, pinned)
	if err := cb("github.com:22", addr, gh); err != nil {
		t.Errorf("real GitHub key rejected: %v", err)
	}
	if err := cb("github.com:22", addr, newKey(t)); err == nil || !strings.Contains(err.Error(), "MISMATCH") {
		t.Errorf("impostor GitHub key: %v", err)
	}
	if err := cb("git.example.com:22", addr, newKey(t)); err == nil || !strings.Contains(err.Error(), "not in known_hosts") {
		t.Errorf("unknown host: %v", err)
	}

	// With a known_hosts file.
	dir := t.TempDir()
	other := newKey(t)
	kh := filepath.Join(dir, "known_hosts")
	line := "git.example.com " + string(ssh.MarshalAuthorizedKey(other))
	if err := os.WriteFile(kh, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	hk, err := HostKeyCallback(func(k string) string {
		if k == "SSH_KNOWN_HOSTS" {
			return kh
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hk("git.example.com:22", addr, other); err != nil {
		t.Errorf("known host rejected: %v", err)
	}
	if err := hk("git.example.com:22", addr, newKey(t)); err == nil || !strings.Contains(err.Error(), "MISMATCH") {
		t.Errorf("changed key for known host: %v", err)
	}
	if err := hk("github.com:22", addr, gh); err != nil {
		t.Errorf("GitHub should fall back to pinned keys: %v", err)
	}
}

func writeKey(t *testing.T, passphrase string) string {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "id")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func noEnv(string) string { return "" }

func TestLoadKeyPassphraseOnlyWhenNeeded(t *testing.T) {
	plain := writeKey(t, "")
	prompted := false
	prompt := func(string) (string, error) { prompted = true; return "pw", nil }
	if _, err := loadKey(plain, Options{Prompt: prompt, Getenv: noEnv}); err != nil || prompted {
		t.Fatalf("plain key: err=%v prompted=%v", err, prompted)
	}

	enc := writeKey(t, "pw")
	if _, err := loadKey(enc, Options{Prompt: prompt, Getenv: noEnv}); err != nil || !prompted {
		t.Fatalf("encrypted key via prompt: err=%v prompted=%v", err, prompted)
	}
	env := func(k string) string {
		if k == config.EnvPassphrase {
			return "pw"
		}
		return ""
	}
	if _, err := loadKey(enc, Options{Getenv: env}); err != nil {
		t.Fatalf("encrypted key via env: %v", err)
	}
	if _, err := loadKey(enc, Options{Getenv: noEnv}); err == nil || !strings.Contains(err.Error(), config.EnvPassphrase) {
		t.Fatalf("encrypted key without prompt: %v", err)
	}
	wrong := func(string) (string, error) { return "nope", nil }
	if _, err := loadKey(enc, Options{Prompt: wrong, Getenv: noEnv}); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
}

func TestMethod(t *testing.T) {
	cfg := config.Default()
	cfg.Repo.URL = "https://github.com/org/app.git"
	cfg.Auth.Method = config.AuthNone
	if am, err := Method(cfg, Options{Getenv: noEnv}); am != nil || err != nil {
		t.Fatalf("none: %v %v", am, err)
	}

	// Token: env wins over file.
	tf := filepath.Join(t.TempDir(), "tok")
	os.WriteFile(tf, []byte("file-token\n"), 0o600)
	cfg.Auth.Method, cfg.Auth.TokenFile = config.AuthToken, tf
	am, err := Method(cfg, Options{Getenv: noEnv})
	if err != nil || am.(*githttp.BasicAuth).Password != "file-token" {
		t.Fatalf("token file: %v %v", am, err)
	}
	env := func(k string) string {
		if k == config.EnvToken {
			return "env-token"
		}
		return ""
	}
	am, _ = Method(cfg, Options{Getenv: env})
	if am.(*githttp.BasicAuth).Password != "env-token" {
		t.Errorf("env token should take precedence")
	}
	if s := am.String(); strings.Contains(s, "env-token") {
		t.Errorf("auth String() leaks the token: %s", s)
	}

	// SSH key: user from URL, host key checking installed.
	cfg = config.Default()
	cfg.Repo.URL = "ssh://deploy@git.example.com/app.git"
	cfg.Auth.Method, cfg.Auth.KeyFile = config.AuthSSHKey, writeKey(t, "")
	am, err = Method(cfg, Options{Getenv: noEnv})
	if err != nil {
		t.Fatal(err)
	}
	pk := am.(*gitssh.PublicKeys)
	if pk.User != "deploy" || pk.HostKeyCallback == nil {
		t.Errorf("ssh-key: user=%q callback=%v", pk.User, pk.HostKeyCallback != nil)
	}
}

func TestInlineCredentials(t *testing.T) {
	cfg := config.Default()
	cfg.Repo.URL = "https://github.com/org/app.git"
	cfg.Auth.Method, cfg.Auth.Token = config.AuthToken, "  inline-token\n"
	am, err := Method(cfg, Options{Getenv: noEnv})
	if err != nil || am.(*githttp.BasicAuth).Password != "inline-token" {
		t.Fatalf("inline token: %v %v", am, err)
	}
	env := func(k string) string {
		if k == config.EnvToken {
			return "env-token"
		}
		return ""
	}
	if am, _ := Method(cfg, Options{Getenv: env}); am.(*githttp.BasicAuth).Password != "env-token" {
		t.Error("GGS_AUTH_TOKEN should override the inline token")
	}

	keyFile := writeKey(t, "")
	pem, _ := os.ReadFile(keyFile)
	cfg = config.Default()
	cfg.Repo.URL = "git@github.com:org/app.git"
	cfg.Auth.Method, cfg.Auth.Key = config.AuthSSHKey, string(pem)
	if _, err := Method(cfg, Options{Getenv: noEnv}); err != nil {
		t.Fatalf("inline key: %v", err)
	}
	cfg.Auth.Key = "not a key"
	if _, err := Method(cfg, Options{Getenv: noEnv}); err == nil || strings.Contains(err.Error(), "not a key") {
		t.Fatalf("bad inline key should fail without echoing it: %v", err)
	}
}
