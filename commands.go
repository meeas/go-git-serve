package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/meeas/go-git-serve/internal/auth"
	"github.com/meeas/go-git-serve/internal/config"
	"github.com/meeas/go-git-serve/internal/logx"
	"github.com/meeas/go-git-serve/internal/svc"
)

func cmdAuth(app string, args []string) error {
	if len(args) == 0 {
		return usageErr("usage: %s auth set-token | clear", app)
	}
	switch args[0] {
	case "set-token":
		tok, err := tokenFromUser()
		if err != nil {
			return err
		}
		if err := auth.SetToken(app, tok); err != nil {
			return fmt.Errorf("saving to the OS keychain: %w (alternatives: auth.token_file or %s)", err, config.EnvToken)
		}
		logx.Infof("Token saved to the OS keychain for %s", app)
		return nil
	case "clear":
		err := auth.DeleteToken(app)
		if errors.Is(err, auth.ErrNoToken) {
			logx.Infof("No token stored for %s", app)
			return nil
		}
		if err != nil {
			return err
		}
		logx.Infof("Token removed from the OS keychain for %s", app)
		return nil
	}
	return usageErr("unknown auth action %q (set-token | clear)", args[0])
}

// tokenFromUser prompts on a terminal, or reads the whole of stdin when
// piped (e.g. `cat token.txt | app auth set-token`).
func tokenFromUser() (string, error) {
	if isTerminal() {
		return readToken()
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" {
		return "", usageErr("no token received on stdin")
	}
	return tok, nil
}

func cmdService(app string, args []string) error {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return usageErr("usage: %s service %s", app, strings.Join(svc.Actions, " | "))
	}
	action := args[0]
	f := newFlags(app)
	if err := f.parse(args[1:]); err != nil {
		return err
	}
	sc := svc.Config{AppName: app}

	if action == "install" {
		cfg, paths, found, err := f.load(app)
		if err != nil {
			return err
		}
		if !found {
			return usageErr("no configuration at %s; run `%s init` first (services read their settings from the config file)", paths.ConfigFile, app)
		}
		if runtime.GOOS == "windows" {
			for _, p := range []string{cfg.Auth.KeyFile, cfg.Auth.TokenFile} {
				if strings.HasPrefix(p, "~") {
					logx.Warnf("%q: the service runs as LocalSystem, whose home differs from yours; use an absolute path", p)
				}
			}
		}
		if err := config.Validate(&cfg); err != nil {
			return usageErr("%v", err)
		}
		cfgPath, err := filepath.Abs(paths.ConfigFile)
		if err != nil {
			return err
		}
		logFile := paths.LogFile
		if f.logFile != "" {
			logFile = config.ExpandHome(f.logFile)
		}
		if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
			return err
		}
		sc.Arguments = []string{"run", "--daemon", "--config", cfgPath, "--log-file", logFile}
		warnServiceAuth(app, cfg)
		if exe, err := os.Executable(); err == nil && strings.Contains(exe, "go-build") {
			logx.Warnf("installing from a temporary `go run` binary; build the executable first")
		}
		defer logx.Infof("Logs: %s", logFile)
	}

	msg, err := svc.Control(sc, action)
	if err != nil {
		return fmt.Errorf("service %s: %w", action, err)
	}
	logx.Infof("%s service: %s", app, msg)
	if action == "install" {
		logx.Infof("Start it with: %s service start", app)
		if runtime.GOOS == "linux" {
			logx.Infof("To keep it running when you are logged out: loginctl enable-linger $USER")
		}
	}
	return nil
}

// warnServiceAuth points out auth settings that cannot work unattended.
func warnServiceAuth(app string, cfg config.Config) {
	switch cfg.Auth.Method {
	case config.AuthSSHKey:
		logx.Infof("Note: services cannot prompt; the SSH key must have no passphrase (a read-only deploy key is recommended).")
	case config.AuthSSHAgent:
		logx.Warnf("ssh-agent is usually not reachable from a service; prefer ssh-key with a deploy key, or token")
	case config.AuthToken:
		if cfg.Auth.TokenFile == "" && runtime.GOOS == "windows" {
			logx.Warnf("Windows services run as LocalSystem and cannot read your keychain; set auth.token_file")
		}
	}
}
