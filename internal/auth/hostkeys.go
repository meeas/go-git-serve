package auth

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// githubKnownHosts holds GitHub's published SSH host keys, from
// https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/githubs-ssh-key-fingerprints
// They are used when ~/.ssh/known_hosts has no entry for GitHub, so that a
// fresh machine can still verify it is talking to the real GitHub.
const githubKnownHosts = `github.com,[ssh.github.com]:443 ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl
github.com,[ssh.github.com]:443 ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=
github.com,[ssh.github.com]:443 ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=
`

// pinnedKeys parses githubKnownHosts into normalised host -> keys.
func pinnedKeys() map[string][]ssh.PublicKey {
	out := map[string][]ssh.PublicKey{}
	rest := []byte(githubKnownHosts)
	for len(rest) > 0 {
		_, hosts, key, _, r, err := ssh.ParseKnownHosts(rest)
		if err != nil {
			break
		}
		rest = r
		for _, h := range hosts {
			h = knownhosts.Normalize(h)
			out[h] = append(out[h], key)
		}
	}
	return out
}

// knownHostsFiles returns the user's known_hosts files: those listed in
// SSH_KNOWN_HOSTS (path-list separated), else ~/.ssh/known_hosts if present.
func knownHostsFiles(getenv func(string) string) []string {
	var files []string
	if v := getenv("SSH_KNOWN_HOSTS"); v != "" {
		for _, f := range filepath.SplitList(v) {
			if _, err := os.Stat(f); err == nil {
				files = append(files, f)
			}
		}
		return files
	}
	if home, err := os.UserHomeDir(); err == nil {
		f := filepath.Join(home, ".ssh", "known_hosts")
		if _, err := os.Stat(f); err == nil {
			files = append(files, f)
		}
	}
	return files
}

// HostKeyCallback verifies SSH servers against the user's known_hosts files,
// falling back to GitHub's built-in keys. Unknown hosts are refused rather
// than trusted on first use, and a key that differs from a recorded key of
// the same type is reported as a possible man-in-the-middle.
func HostKeyCallback(getenv func(string) string) (ssh.HostKeyCallback, error) {
	var user ssh.HostKeyCallback
	if files := knownHostsFiles(getenv); len(files) > 0 {
		cb, err := knownhosts.New(files...)
		if err != nil {
			return nil, fmt.Errorf("reading known_hosts: %w", err)
		}
		user = cb
	}
	return newHostKeyCallback(user, pinnedKeys()), nil
}

func newHostKeyCallback(user ssh.HostKeyCallback, pinned map[string][]ssh.PublicKey) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if user != nil {
			err := user(hostname, remote, key)
			if err == nil {
				return nil
			}
			var ke *knownhosts.KeyError
			if !errors.As(err, &ke) {
				return err
			}
			for _, want := range ke.Want {
				if want.Key.Type() == key.Type() {
					return mismatchError(hostname, key, want.Filename, want.Line)
				}
			}
			// Host unknown, or only known with other key types: try pinned keys.
		}

		norm := knownhosts.Normalize(hostname)
		if keys, ok := pinned[norm]; ok {
			sameType := false
			for _, k := range keys {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					return nil
				}
				if k.Type() == key.Type() {
					sameType = true
				}
			}
			if sameType {
				return mismatchError(hostname, key, "built-in GitHub keys", 0)
			}
		}

		host := hostname
		if h, _, err := net.SplitHostPort(hostname); err == nil {
			host = h
		}
		return fmt.Errorf("SSH host %s is not in known_hosts (key %s %s). Verify the fingerprint with the host's documentation, then add it, e.g.: ssh-keyscan %s >> ~/.ssh/known_hosts",
			hostname, key.Type(), ssh.FingerprintSHA256(key), host)
	}
}

func mismatchError(hostname string, key ssh.PublicKey, where string, line int) error {
	loc := where
	if line > 0 {
		loc = fmt.Sprintf("%s:%d", where, line)
	}
	return fmt.Errorf("SSH HOST KEY MISMATCH for %s: server offered %s %s, which differs from %s. "+
		"This could be a man-in-the-middle attack; refusing to connect",
		hostname, key.Type(), ssh.FingerprintSHA256(key), strings.TrimSpace(loc))
}
