package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/meeas/go-git-serve/internal/auth"
	"github.com/meeas/go-git-serve/internal/browser"
	"github.com/meeas/go-git-serve/internal/config"
	"github.com/meeas/go-git-serve/internal/logx"
	"github.com/meeas/go-git-serve/internal/repo"
	"github.com/meeas/go-git-serve/internal/server"
	"github.com/meeas/go-git-serve/internal/svc"
)

// syncTimeout bounds a single clone or pull.
const syncTimeout = 10 * time.Minute

func cmdRun(app string, args []string) error {
	f := newFlags(app)
	if err := f.parse(args); err != nil {
		return err
	}
	closeLog, err := f.setupLogging()
	if err != nil {
		return err
	}
	defer closeLog()

	cfg, paths, found, err := f.load(app)
	if err != nil {
		return err
	}
	interactive := !f.daemon && isTerminal()

	if !found && cfg.Repo.URL == "" {
		if !interactive {
			if werr := config.Write(paths.ConfigFile, config.Default(), false); werr == nil {
				return usageErr("no configuration found; wrote a template to %s. Set repo.url there and run again", paths.ConfigFile)
			}
			return usageErr("no configuration found at %s; run `%s init`, or set GGS_REPO_URL / --repo", paths.ConfigFile, app)
		}
		logx.Infof("No configuration found at %s. Let's create one.\n", paths.ConfigFile)
		if cfg, err = wizard(app, paths, cfg); err != nil {
			return err
		}
		if err := f.apply(&cfg); err != nil { // keep flag overrides like --no-browser
			return err
		}
	}
	if err := config.Validate(&cfg); err != nil {
		return usageErr("%v", err)
	}

	if f.daemon {
		return svc.Run(svc.Config{AppName: app}, func(ctx context.Context) error {
			return serve(ctx, app, cfg, paths, f.offline, nil)
		})
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var prompt auth.PromptFunc
	if interactive {
		prompt = promptSecret
	}
	return serve(ctx, app, cfg, paths, f.offline, prompt)
}

// gitProgress is where clone/fetch progress goes: the terminal when
// interactive and not quiet, otherwise nowhere.
func gitProgress(prompt auth.PromptFunc) io.Writer {
	if prompt != nil && !logx.Quiet() {
		return os.Stderr
	}
	return nil
}

func newSyncer(app string, cfg config.Config, paths config.Paths, prompt auth.PromptFunc) (*repo.Syncer, error) {
	var am transport.AuthMethod
	am, err := auth.Method(cfg, auth.Options{AppName: app, Prompt: prompt})
	if err != nil {
		return nil, err
	}
	return &repo.Syncer{
		URL:      cfg.Repo.URL,
		Branch:   cfg.Repo.Branch,
		Dir:      paths.RepoDir,
		Auth:     am,
		Progress: gitProgress(prompt),
	}, nil
}

// serve syncs the repository, serves it, and blocks until ctx is cancelled
// or the app window is closed.
func serve(ctx context.Context, app string, cfg config.Config, paths config.Paths, offline bool, prompt auth.PromptFunc) error {
	cached := &repo.Syncer{Dir: paths.RepoDir}
	haveCache := cached.Exists()

	var syncer *repo.Syncer
	if offline {
		if !haveCache {
			return fmt.Errorf("--offline: no cached copy at %s yet; run once without --offline", paths.RepoDir)
		}
		logx.Infof("Offline: serving the cached copy")
	} else {
		var err error
		syncer, err = newSyncer(app, cfg, paths, prompt)
		switch {
		case err != nil && haveCache:
			logx.Warnf("%v; serving the cached copy", err)
			syncer = nil
		case err != nil:
			return err
		case cfg.Update.OnStart || !haveCache:
			sctx, cancel := context.WithTimeout(ctx, syncTimeout)
			res, serr := syncer.Sync(sctx)
			cancel()
			if ctx.Err() != nil {
				return nil
			}
			if serr != nil {
				if !syncer.Exists() {
					return serr
				}
				logx.Warnf("update failed: %v", serr)
				logx.Warnf("serving the cached copy")
			} else {
				logSync(res)
			}
		}
	}

	root, err := server.OpenRoot(filepath.Join(paths.RepoDir, cfg.Repo.Subdir))
	if err != nil {
		if cfg.Repo.Subdir != "" {
			return fmt.Errorf("folder %q not found in the repository: %w", filepath.ToSlash(cfg.Repo.Subdir), err)
		}
		return err
	}
	defer root.Close()

	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("cannot listen on %s: %w (is another program, or another copy of %s, using it? choose another address with --listen)",
			cfg.Server.Listen, err, app)
	}
	if server.IsWildcard(cfg.Server.Listen) {
		logx.Warnf("listening on all network interfaces: other machines can reach this site")
	}
	srv := &http.Server{
		Handler:           server.Handler(root.FS(), server.AllowedHosts(cfg.Server.Listen)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	url := server.URL(ln.Addr(), cfg.Server.Listen)
	logx.Infof("Serving %s at %s", cfg.Repo.URL, url)
	if prompt != nil {
		logx.Infof("Press Ctrl+C to stop.")
	}

	if syncer != nil && cfg.Update.Interval.Duration > 0 {
		go updateLoop(ctx, syncer, cfg.Update.Interval.Duration)
	}

	sess := browser.Open(url, browser.Options{
		Mode:       cfg.Browser.Mode,
		Path:       cfg.Browser.Path,
		ProfileDir: paths.ProfileDir,
	})

	select {
	case <-ctx.Done():
		logx.Infof("Stopping")
	case <-sess.Closed():
		logx.Infof("Window closed; stopping")
	case err := <-serveErr:
		return err
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func logSync(res repo.Result) {
	short := res.Commit
	if len(short) > 12 {
		short = short[:12]
	}
	switch {
	case res.Cloned:
		logx.Infof("Cloned %s at %s", res.Branch, short)
	case res.Changed:
		logx.Infof("Updated %s to %s", res.Branch, short)
	default:
		logx.Infof("Up to date (%s at %s)", res.Branch, short)
	}
}

func updateLoop(ctx context.Context, s *repo.Syncer, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		uctx, cancel := context.WithTimeout(ctx, syncTimeout)
		res, err := s.Update(uctx)
		cancel()
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			logx.Warnf("periodic update failed: %v", err)
		case res.Changed:
			logSync(res)
		default:
			logx.Debugf("periodic update: no changes")
		}
	}
}

func cmdSync(app string, args []string) error {
	f := newFlags(app)
	if err := f.parse(args); err != nil {
		return err
	}
	closeLog, err := f.setupLogging()
	if err != nil {
		return err
	}
	defer closeLog()
	if f.offline {
		return usageErr("sync cannot be used with --offline")
	}
	cfg, paths, found, err := f.load(app)
	if err != nil {
		return err
	}
	if !found && cfg.Repo.URL == "" {
		return usageErr("no configuration found at %s; run `%s init` first", paths.ConfigFile, app)
	}
	if err := config.Validate(&cfg); err != nil {
		return usageErr("%v", err)
	}
	var prompt auth.PromptFunc
	if !f.daemon && isTerminal() {
		prompt = promptSecret
	}
	s, err := newSyncer(app, cfg, paths, prompt)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	res, err := s.Sync(ctx)
	if err != nil {
		return err
	}
	logSync(res)
	return nil
}
