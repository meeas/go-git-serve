// Command go-git-serve keeps a local copy of a git repository up to date and
// serves it as a web site, opened in a chromeless browser window.
//
// All settings are tied to the executable's name, so copies of the binary
// under different names act as independent apps.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/pflag"

	"github.com/meeas/go-git-serve/internal/config"
	"github.com/meeas/go-git-serve/internal/logx"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// exitError carries a specific process exit code.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

// usageErr is a configuration or usage problem (exit code 2).
func usageErr(format string, args ...any) error {
	return &exitError{code: 2, err: fmt.Errorf(format, args...)}
}

func main() {
	os.Exit(realMain(os.Args))
}

func realMain(argv []string) int {
	app := config.AppName(argv[0])
	args := argv[1:]
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}

	var err error
	switch cmd {
	case "run":
		err = cmdRun(app, args)
	case "init":
		err = cmdInit(app, args)
	case "sync":
		err = cmdSync(app, args)
	case "auth":
		err = cmdAuth(app, args)
	case "service":
		err = cmdService(app, args)
	case "version":
		fmt.Printf("%s %s (go-git-serve, %s/%s)\n", app, version, runtime.GOOS, runtime.GOARCH)
	case "help":
		fmt.Print(usage(app))
	default:
		err = usageErr("unknown command %q (see `%s help`)", cmd, app)
	}

	if err == nil || errors.Is(err, pflag.ErrHelp) {
		return 0
	}
	logx.Errorf("%v", err)
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	return 1
}

func usage(app string) string {
	return strings.ReplaceAll(`Usage: APP [command] [flags]

Keeps a local copy of a git repository up to date and serves it as a web site.

Commands:
  run                 sync, serve and open the site (default)
  init                create the config file interactively
  sync                clone or pull, then exit
  auth set-token      store an access token in the OS keychain (prompts, or reads stdin)
  auth clear          remove the stored token
  service ACTION      manage the background service: install | uninstall | start | stop | restart | status
  version             print the version
  help                show this help

Flags:
`+newFlags(app).fs.FlagUsages()+`
Configuration file: <user config dir>/APP.toml (see --config).
Every setting can also be set with GGS_* environment variables, e.g. GGS_REPO_URL.
Secrets: GGS_AUTH_TOKEN, GGS_AUTH_PASSPHRASE.
`, "APP", app)
}

// flags holds every command-line flag. Not all commands use all of them.
type flags struct {
	fs *pflag.FlagSet

	config, logFile                                  string
	listen, repo, branch, subdir                     string
	auth, keyFile, tokenFile                         string
	interval, browser, browserPath                   string
	offline, noBrowser, daemon, verbose, quiet, full bool
	randomizeStart                                   bool
}

func newFlags(app string) *flags {
	f := &flags{fs: pflag.NewFlagSet(app, pflag.ContinueOnError)}
	fs := f.fs
	fs.SortFlags = false
	fs.StringVarP(&f.config, "config", "c", "", "config file (default <user config dir>/"+app+".toml)")
	fs.StringVarP(&f.listen, "listen", "l", "", "address to serve on (default 127.0.0.1:8888)")
	fs.StringVar(&f.repo, "repo", "", "repository URL")
	fs.StringVar(&f.branch, "branch", "", "branch to serve (default: the repository's default)")
	fs.StringVar(&f.subdir, "subdir", "", "serve only this folder of the repository")
	fs.BoolVar(&f.full, "full", false, "clone the full history instead of only the latest commit")
	fs.StringVar(&f.auth, "auth", "", "auth method: none | ssh-key | ssh-agent | token")
	fs.StringVar(&f.keyFile, "key-file", "", "SSH private key file (ssh-key)")
	fs.StringVar(&f.tokenFile, "token-file", "", "file containing an access token (token)")
	fs.BoolVar(&f.offline, "offline", false, "serve the cached copy without pulling")
	fs.StringVar(&f.interval, "interval", "", "re-pull periodically, e.g. 15m (0 disables)")
	fs.BoolVar(&f.randomizeStart, "randomize-start", true, "first re-pull at a random time within the first interval (--randomize-start=false to disable)")
	fs.StringVar(&f.browser, "browser", "", "app | default | none (default app)")
	fs.StringVar(&f.browserPath, "browser-path", "", "browser binary for app mode (default: auto-detect)")
	fs.BoolVar(&f.noBrowser, "no-browser", false, "only print the URL (same as --browser none)")
	fs.BoolVarP(&f.daemon, "daemon", "d", false, "run unattended: no prompts, no browser, timestamped logs")
	fs.StringVar(&f.logFile, "log-file", "", "append logs to this file instead of stderr")
	fs.BoolVarP(&f.verbose, "verbose", "v", false, "show debug output")
	fs.BoolVarP(&f.quiet, "quiet", "q", false, "only show warnings and errors")
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage(app)) }
	return f
}

func (f *flags) parse(args []string) error {
	if err := f.fs.Parse(args); err != nil {
		if errors.Is(err, pflag.ErrHelp) {
			return err
		}
		return usageErr("%v", err)
	}
	if f.fs.NArg() > 0 {
		return usageErr("unexpected argument %q", f.fs.Arg(0))
	}
	if f.verbose && f.quiet {
		return usageErr("--verbose and --quiet cannot be combined")
	}
	return nil
}

// apply overrides cfg with the flags that were given.
func (f *flags) apply(cfg *config.Config) error {
	set := func(name string, dst *string, v string) {
		if f.fs.Changed(name) {
			*dst = v
		}
	}
	set("listen", &cfg.Server.Listen, f.listen)
	set("repo", &cfg.Repo.URL, f.repo)
	set("branch", &cfg.Repo.Branch, f.branch)
	set("subdir", &cfg.Repo.Subdir, f.subdir)
	set("auth", &cfg.Auth.Method, f.auth)
	set("key-file", &cfg.Auth.KeyFile, f.keyFile)
	set("token-file", &cfg.Auth.TokenFile, f.tokenFile)
	set("browser", &cfg.Browser.Mode, f.browser)
	set("browser-path", &cfg.Browser.Path, f.browserPath)
	if f.fs.Changed("interval") {
		d, err := config.ParseDuration(f.interval)
		if err != nil {
			return usageErr("--interval: %v", err)
		}
		cfg.Update.Interval.Duration = d
	}
	if f.fs.Changed("randomize-start") {
		cfg.Update.RandomizeStart = f.randomizeStart
	}
	if f.full {
		cfg.Repo.Shallow = false
	}
	if f.noBrowser || f.daemon {
		cfg.Browser.Mode = config.BrowserNone
	}
	return nil
}

// setupLogging applies --verbose/--quiet/--log-file. Daemon logs get timestamps.
func (f *flags) setupLogging() (closeFn func(), err error) {
	level := logx.LevelNormal
	switch {
	case f.verbose:
		level = logx.LevelVerbose
	case f.quiet:
		level = logx.LevelQuiet
	}
	closeFn = func() {}
	out := os.Stderr
	if f.logFile != "" {
		lf, err := os.OpenFile(config.ExpandHome(f.logFile), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, usageErr("opening log file: %v", err)
		}
		out = lf
		closeFn = func() { lf.Close() }
	}
	logx.Setup(out, level, f.daemon || f.logFile != "")
	return closeFn, nil
}

// load resolves defaults, the config file, env vars and flags. found
// reports whether the config file exists.
func (f *flags) load(app string) (cfg config.Config, paths config.Paths, found bool, err error) {
	cfg, paths, found, err = f.loadFile(app)
	if err != nil {
		return cfg, paths, found, err
	}
	if err := config.ApplyEnv(&cfg, os.LookupEnv); err != nil {
		return cfg, paths, found, usageErr("%v", err)
	}
	if err := f.apply(&cfg); err != nil {
		return cfg, paths, found, err
	}
	return cfg, paths, found, nil
}

// loadFile resolves defaults and the config file only, which is all an
// installed service sees.
func (f *flags) loadFile(app string) (cfg config.Config, paths config.Paths, found bool, err error) {
	paths, err = config.DefaultPaths(app)
	if err != nil {
		return cfg, paths, false, err
	}
	if f.config != "" {
		paths.ConfigFile = config.ExpandHome(f.config)
	}
	cfg = config.Default()
	found, unknown, err := config.LoadFile(paths.ConfigFile, &cfg)
	if err != nil {
		return cfg, paths, found, usageErr("%v", err)
	}
	for _, k := range unknown {
		logx.Warnf("%s: unknown setting %q ignored", paths.ConfigFile, k)
	}
	return cfg, paths, found, nil
}
