package main

import (
	"context"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
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
	if found && currentBundle() == nil && cfg.HasInlineSecret() {
		auth.WarnIfExposed(paths.ConfigFile)
	}

	if f.daemon {
		return svc.Run(svc.Config{AppName: app}, func(ctx context.Context) error {
			return serve(ctx, runOpts{app: app, cfg: cfg, paths: paths, offline: f.offline, daemon: true})
		})
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var prompt auth.PromptFunc
	if interactive {
		prompt = promptSecret
	}
	return serve(ctx, runOpts{app: app, cfg: cfg, paths: paths, offline: f.offline, prompt: prompt})
}

// authOptions are the auth settings for this run. A bundled executable
// ignores GGS_AUTH_TOKEN: its credentials are fixed by the bundle.
func authOptions(app string, prompt auth.PromptFunc) auth.Options {
	opts := auth.Options{AppName: app, Prompt: prompt}
	if currentBundle() != nil {
		opts.Getenv = func(k string) string {
			if k == config.EnvToken {
				return ""
			}
			return os.Getenv(k)
		}
	}
	return opts
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
	am, err := auth.Method(cfg, authOptions(app, prompt))
	if err != nil {
		return nil, err
	}
	return &repo.Syncer{
		URL:      cfg.Repo.URL,
		Branch:   cfg.Repo.Branch,
		Dir:      paths.RepoDir,
		Shallow:  cfg.Repo.Shallow,
		Auth:     am,
		Progress: gitProgress(prompt),
	}, nil
}

// runOpts carries what serve needs.
type runOpts struct {
	app     string
	cfg     config.Config
	paths   config.Paths
	offline bool
	daemon  bool
	prompt  auth.PromptFunc // nil when no terminal is available
}

func (o runOpts) browserOptions() browser.Options {
	return browser.Options{Mode: o.cfg.Browser.Mode, Path: o.cfg.Browser.Path, ProfileDir: o.paths.ProfileDir}
}

// serve claims the listen address, syncs the repository, serves it, and
// blocks until ctx is cancelled or the app window is closed. If this app is
// already running on the address, it opens that instead (or, as a daemon,
// reports an error).
func serve(ctx context.Context, o runOpts) error {
	cfg, paths, app := o.cfg, o.paths, o.app

	// 1. Before touching the checkout: is something already on our address?
	if handled, err := checkRunning(ctx, o); handled {
		return err
	}

	// 2. Claim the address. Until the content is ready, the site answers with
	// a "starting" page that also identifies this app to other copies.
	ln, err := net.Listen("tcp", cfg.Server.Listen)
	if err != nil {
		if handled, perr := checkRunning(ctx, o); handled { // lost a start-up race
			return perr
		}
		return fmt.Errorf("cannot listen on %s: %w (choose another address with --listen)", cfg.Server.Listen, err)
	}
	if server.IsWildcard(cfg.Server.Listen) {
		logx.Warnf("listening on all network interfaces: other machines can reach this site")
	}
	site := server.NewSite(app, server.AllowedHosts(cfg.Server.Listen))
	srv := &http.Server{Handler: site, ReadHeaderTimeout: 10 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()
	url := server.URL(ln.Addr(), cfg.Server.Listen)

	// 3. Bring the checkout up to date.
	cached := &repo.Syncer{Dir: paths.RepoDir}
	haveCache := cached.Exists()
	var syncer *repo.Syncer
	if o.offline {
		if !haveCache {
			return fmt.Errorf("--offline: no cached copy at %s yet; run once without --offline", paths.RepoDir)
		}
		logx.Infof("Offline: serving the cached copy")
	} else {
		syncer, err = newSyncer(app, cfg, paths, o.prompt)
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

	// 4. Mark the checkout as in use, so no other copy re-clones it under us.
	hold, err := cached.HoldServing(ctx, syncTimeout)
	if err != nil {
		return err
	}
	defer hold.Release()

	served := &servedRoot{dir: filepath.Join(paths.RepoDir, cfg.Repo.Subdir), site: site}
	if err := served.open(); err != nil {
		if cfg.Repo.Subdir != "" {
			return fmt.Errorf("folder %q not found in the repository: %w", filepath.ToSlash(cfg.Repo.Subdir), err)
		}
		return err
	}
	defer served.close()

	if origin := cached.Origin(); origin != "" && origin != cfg.Repo.URL {
		logx.Warnf("serving the cached copy of %s, not %s", origin, cfg.Repo.URL)
		logx.Infof("Serving %s at %s", origin, url)
	} else {
		logx.Infof("Serving %s at %s", cfg.Repo.URL, url)
	}
	if o.prompt != nil {
		logx.Infof("Press Ctrl+C to stop.")
	}
	if syncer != nil && cfg.Update.Interval.Duration > 0 {
		go updateLoop(ctx, syncer, hold, served.open, cfg.Update.Interval.Duration, cfg.Update.RandomizeStart)
	}
	sess := browser.Open(url, o.browserOptions())

	select {
	case <-ctx.Done():
		logx.Infof("Stopping")
	case <-sess.Closed():
		logx.Infof("Window closed; stopping")
	case err := <-serveErr:
		return err
	}
	return nil
}

// checkRunning probes the listen address. It returns handled=true when the
// address is taken: by this app (opens it, or errors in daemon mode), by a
// different go-git-serve app, or by another program (errors).
func checkRunning(ctx context.Context, o runOpts) (handled bool, err error) {
	listen := o.cfg.Server.Listen
	res, other := server.Probe(ctx, listen, o.app)
	url := server.URLFor(listen)
	switch res {
	case server.ProbeSelf:
		if o.daemon {
			return true, fmt.Errorf("%s is already running at %s", o.app, url)
		}
		logx.Infof("%s is already running at %s; opening it", o.app, url)
		browser.Open(url, o.browserOptions())
		return true, nil
	case server.ProbeOtherApp:
		return true, fmt.Errorf("%s is in use by another go-git-serve app (%s); choose another address with --listen", listen, other)
	case server.ProbeForeign:
		return true, fmt.Errorf("%s is in use by another program; choose another address with --listen", listen)
	}
	return false, nil
}

func logSync(res repo.Result) {
	short := res.Commit
	if len(short) > 12 {
		short = short[:12]
	}
	switch {
	case res.Cloned && res.Note != "":
		logx.Infof("Re-cloned %s at %s (%s)", res.Branch, short, res.Note)
	case res.Cloned:
		logx.Infof("Cloned %s at %s", res.Branch, short)
	case res.Changed:
		logx.Infof("Updated %s to %s", res.Branch, short)
	default:
		logx.Infof("Up to date (%s at %s)", res.Branch, short)
	}
}

// servedRoot is the checkout directory being served. open (re)opens it, so
// the site switches to a replaced checkout without a restart.
type servedRoot struct {
	mu   sync.Mutex
	dir  string
	site *server.Site
	root *os.Root
}

func (s *servedRoot) open() error {
	r, err := server.OpenRoot(s.dir)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.site.SetFiles(r.FS())
	if s.root != nil {
		s.root.Close()
	}
	s.root = r
	return nil
}

func (s *servedRoot) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root != nil {
		s.root.Close()
		s.root = nil
	}
}

// firstUpdateDelay is the wait before the first periodic update: a random
// point in [0, every) when randomize is set, so copies started together
// (a whole classroom) spread their checks across the interval; otherwise
// one full interval. randN returns a uniform value in [0, n).
func firstUpdateDelay(every time.Duration, randomize bool, randN func(time.Duration) time.Duration) time.Duration {
	if !randomize || every <= 0 {
		return every
	}
	return randN(every)
}

// updateLoop pulls periodically. The first update follows firstUpdateDelay;
// each later one waits a full interval after the previous update finished,
// so a slow pull never overlaps the next and each copy keeps its own phase.
func updateLoop(ctx context.Context, s *repo.Syncer, hold *repo.Serving, swap func() error, every time.Duration, randomize bool) {
	wait := firstUpdateDelay(every, randomize, rand.N[time.Duration])
	logx.Debugf("first periodic update in %s, then every %s", wait.Round(time.Second), every)
	t := time.NewTimer(wait)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		uctx, cancel := context.WithTimeout(ctx, syncTimeout)
		res, err := s.Update(uctx, hold, swap)
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
		t.Reset(every)
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
