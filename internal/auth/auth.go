// Package auth builds go-git authentication for the configured method and
// manages tokens in the OS keychain.
package auth

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	sshagent "github.com/xanzy/ssh-agent"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/meeas/go-git-serve/internal/config"
	"github.com/meeas/go-git-serve/internal/logx"
)

// PromptFunc asks the user for a secret without echoing it. It is nil when
// no terminal is available (daemon mode, services).
type PromptFunc func(prompt string) (string, error)

// Options carries what the auth builder needs besides the config.
type Options struct {
	AppName string     // keychain account name
	Prompt  PromptFunc // nil disables interactive prompts
	Getenv  func(string) string
}

// tokenUser is the HTTP username GitHub expects alongside a token. Other
// hosts accept any non-empty username with a personal access token.
const tokenUser = "x-access-token"

// Method returns the go-git AuthMethod for cfg. It returns nil for "none".
func Method(cfg config.Config, opts Options) (transport.AuthMethod, error) {
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	ep, err := config.Endpoint(cfg.Repo.URL)
	if err != nil {
		return nil, err
	}
	sshUser := ep.User
	if sshUser == "" {
		sshUser = "git"
	}

	switch cfg.Auth.Method {
	case config.AuthNone:
		return nil, nil

	case config.AuthToken:
		tok, src, err := findToken(cfg.Auth.TokenFile, opts)
		if err != nil {
			return nil, err
		}
		logx.Debugf("using token from %s", src)
		return &githttp.BasicAuth{Username: tokenUser, Password: tok}, nil

	case config.AuthSSHKey:
		signer, err := loadKey(cfg.Auth.KeyFile, opts)
		if err != nil {
			return nil, err
		}
		hk, err := HostKeyCallback(opts.Getenv)
		if err != nil {
			return nil, err
		}
		return &gitssh.PublicKeys{
			User:                  sshUser,
			Signer:                signer,
			HostKeyCallbackHelper: gitssh.HostKeyCallbackHelper{HostKeyCallback: hk},
		}, nil

	case config.AuthSSHAgent:
		hk, err := HostKeyCallback(opts.Getenv)
		if err != nil {
			return nil, err
		}
		a := &agentSigners{}
		if _, err := a.Signers(); err != nil {
			return nil, err
		}
		return &gitssh.PublicKeysCallback{
			User:                  sshUser,
			Callback:              a.Signers,
			HostKeyCallbackHelper: gitssh.HostKeyCallbackHelper{HostKeyCallback: hk},
		}, nil
	}
	return nil, fmt.Errorf("unknown auth method %q", cfg.Auth.Method)
}

// findToken looks for a token in GGS_AUTH_TOKEN, then the token file, then
// the OS keychain.
func findToken(tokenFile string, opts Options) (tok, source string, err error) {
	if v := strings.TrimSpace(opts.Getenv(config.EnvToken)); v != "" {
		return v, config.EnvToken, nil
	}
	if tokenFile != "" {
		WarnIfExposed(tokenFile)
		b, err := os.ReadFile(tokenFile)
		if err != nil {
			return "", "", fmt.Errorf("reading token file: %w", err)
		}
		v := strings.TrimSpace(string(b))
		if v == "" {
			return "", "", fmt.Errorf("token file %s is empty", tokenFile)
		}
		return v, "token file " + tokenFile, nil
	}
	v, err := GetToken(opts.AppName)
	if err == nil && v != "" {
		return v, "OS keychain", nil
	}
	hint := fmt.Sprintf("set %s, configure auth.token_file, or run `%s auth set-token`", config.EnvToken, opts.AppName)
	if err != nil && !errors.Is(err, ErrNoToken) {
		return "", "", fmt.Errorf("no token available (keychain: %v); %s", err, hint)
	}
	return "", "", fmt.Errorf("no token available; %s", hint)
}

// loadKey reads a private key, asking for a passphrase only if the key is
// encrypted.
func loadKey(path string, opts Options) (ssh.Signer, error) {
	WarnIfExposed(path)
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading SSH key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err == nil {
		return signer, nil
	}
	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		return nil, fmt.Errorf("parsing SSH key %s: %w", path, err)
	}

	pass := opts.Getenv(config.EnvPassphrase)
	if pass == "" {
		if opts.Prompt == nil {
			return nil, fmt.Errorf("SSH key %s is passphrase-protected; set %s or run interactively", path, config.EnvPassphrase)
		}
		if pass, err = opts.Prompt(fmt.Sprintf("Passphrase for %s: ", path)); err != nil {
			return nil, err
		}
	}
	signer, err = ssh.ParsePrivateKeyWithPassphrase(pem, []byte(pass))
	if err != nil {
		return nil, fmt.Errorf("decrypting SSH key %s: %w", path, err)
	}
	return signer, nil
}

// agentSigners talks to the running SSH agent: SSH_AUTH_SOCK on Linux and
// macOS; Pageant or the Windows OpenSSH agent pipe on Windows. The
// connection is reopened if the agent goes away between periodic pulls.
type agentSigners struct {
	mu   sync.Mutex
	ag   agent.Agent
	conn io.Closer
}

func (a *agentSigners) Signers() ([]ssh.Signer, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ag != nil {
		if s, err := a.ag.Signers(); err == nil {
			return s, nil
		}
		a.close()
	}
	ag, conn, err := sshagent.New()
	if err != nil {
		return nil, fmt.Errorf("cannot reach SSH agent (%s): %w", agentHint(), err)
	}
	s, err := ag.Signers()
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		return nil, fmt.Errorf("SSH agent: %w", err)
	}
	if len(s) == 0 {
		if conn != nil {
			conn.Close()
		}
		return nil, errors.New("SSH agent has no keys loaded (add one with ssh-add)")
	}
	a.ag, a.conn = ag, conn
	return s, nil
}

func (a *agentSigners) close() {
	if a.conn != nil {
		a.conn.Close()
	}
	a.ag, a.conn = nil, nil
}

func agentHint() string {
	if runtime.GOOS == "windows" {
		return "start Pageant or the 'OpenSSH Authentication Agent' service"
	}
	return "is SSH_AUTH_SOCK set?"
}
