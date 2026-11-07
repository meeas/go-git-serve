// Package svc integrates with the OS service manager (systemd, launchd,
// Windows Service Control Manager) via kardianos/service.
package svc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/kardianos/service"

	"github.com/meeas/go-git-serve/internal/logx"
)

// Actions accepted by Control.
var Actions = []string{"install", "uninstall", "start", "stop", "restart", "status"}

// RunFunc is the long-running work; it must return when ctx is cancelled.
type RunFunc func(ctx context.Context) error

type program struct {
	run    RunFunc
	cancel context.CancelFunc
	done   chan struct{}
	mu     sync.Mutex
	err    error
}

func (p *program) Start(service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.done = make(chan struct{})
	go func() {
		defer close(p.done)
		err := p.run(ctx)
		p.mu.Lock()
		p.err = err
		p.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			// Exit non-zero so the service manager restarts us; in the
			// foreground this also ends the process, which Run would not.
			logx.Errorf("%v", err)
			os.Exit(1)
		}
	}()
	return nil
}

func (p *program) Stop(service.Service) error {
	if p.cancel != nil {
		p.cancel()
		select {
		case <-p.done:
		case <-time.After(10 * time.Second):
		}
	}
	return nil
}

// Config describes the service to create.
type Config struct {
	AppName   string
	Arguments []string // arguments passed to the executable when the service starts
}

func newService(c Config, run RunFunc) (service.Service, *program, error) {
	p := &program{run: run}
	sc := &service.Config{
		Name:        c.AppName,
		DisplayName: c.AppName + " (go-git-serve)",
		Description: "Serves a git repository as a local web site (go-git-serve).",
		Arguments:   c.Arguments,
		Option: service.KeyValue{
			"Restart":                "on-failure", // systemd
			"RunAtLoad":              true,         // launchd: start at login
			"OnFailure":              "restart",    // Windows
			"OnFailureDelayDuration": "10s",
		},
	}
	if runtime.GOOS != "windows" {
		// Per-user service (systemd --user / LaunchAgent): runs with the
		// user's config, cache and keychain, no root required.
		sc.Option["UserService"] = true
	}
	s, err := service.New(p, sc)
	return s, p, err
}

// Run executes run under the service manager when started by it, or in
// the foreground (stopping on Ctrl+C / SIGTERM) otherwise.
func Run(c Config, run RunFunc) error {
	s, p, err := newService(c, run)
	if err != nil {
		return err
	}
	if err := s.Run(); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// Control performs a service management action and returns a message for
// the user.
func Control(c Config, action string) (string, error) {
	s, _, err := newService(c, nil)
	if err != nil {
		return "", err
	}
	if action == "status" {
		st, err := s.Status()
		if errors.Is(err, service.ErrNotInstalled) {
			return "not installed", nil
		}
		if err != nil {
			return "", err
		}
		switch st {
		case service.StatusRunning:
			return "running", nil
		case service.StatusStopped:
			return "stopped", nil
		default:
			return "unknown", nil
		}
	}
	valid := false
	for _, a := range service.ControlAction {
		valid = valid || a == action
	}
	if !valid {
		return "", fmt.Errorf("unknown service action %q", action)
	}
	if err := service.Control(s, action); err != nil {
		return "", err
	}
	return action + " done", nil
}
