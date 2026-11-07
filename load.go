package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/meeas/go-git-serve/internal/auth"
	"github.com/meeas/go-git-serve/internal/config"
	"github.com/meeas/go-git-serve/internal/logx"
	"github.com/meeas/go-git-serve/internal/repo"
	"github.com/meeas/go-git-serve/internal/server"
	"github.com/meeas/go-git-serve/internal/svc"
)

// maxConfigSize bounds a loaded configuration file.
const maxConfigSize = 1 << 20

// accessCheckTimeout bounds the credential test during load.
const accessCheckTimeout = 30 * time.Second

// isLoadArg reports whether the first argument is a configuration file to
// load: a .toml file dragged onto the executable (Windows passes its path
// as the only argument), or typed as `app course.toml [--yes]`.
func isLoadArg(args []string) bool {
	if len(args) == 0 || !strings.EqualFold(filepath.Ext(args[0]), ".toml") {
		return false
	}
	fi, err := os.Stat(args[0])
	return err == nil && fi.Mode().IsRegular()
}

// cmdLoad installs a configuration file as this app's configuration:
// validate, test access, confirm replacement, write atomically, remove the
// source, restart a running service, then start (interactively).
func cmdLoad(app string, args []string) error {
	f := newFlags(app)
	yes := f.fs.BoolP("yes", "y", false, "replace an existing configuration without asking")
	keep := f.fs.Bool("keep", false, "keep the loaded file instead of deleting it")
	if err := f.fs.Parse(args); err != nil {
		return usageErr("%v", err)
	}
	if f.fs.NArg() != 1 {
		return usageErr("usage: %s load <file.toml> [--yes] [--keep]", app)
	}
	closeLog, err := f.setupLogging()
	if err != nil {
		return err
	}
	defer closeLog()
	if b := currentBundle(); b != nil {
		return usageErr("%s has a built-in configuration (bundled); it cannot load another one", app)
	}

	src, err := filepath.Abs(f.fs.Arg(0))
	if err != nil {
		return err
	}
	data, err := readLimited(src, maxConfigSize)
	if err != nil {
		return usageErr("reading %s: %v", src, err)
	}

	// 1. Check before touching anything.
	cfg := config.Default()
	if _, unknown, err := config.LoadFile(src, &cfg); err != nil {
		return usageErr("%s is not a valid configuration: %v", src, err)
	} else {
		for _, k := range unknown {
			logx.Warnf("%s: unknown setting %q ignored", src, k)
		}
	}
	if err := config.Validate(&cfg); err != nil {
		return usageErr("%s is not a valid configuration: %v", src, err)
	}

	paths, err := config.DefaultPaths(app)
	if err != nil {
		return err
	}
	if f.config != "" {
		paths.ConfigFile = config.ExpandHome(f.config)
	}
	dest, err := filepath.Abs(paths.ConfigFile)
	if err != nil {
		return err
	}
	if sameFile(src, dest) {
		return usageErr("%s is already %s's configuration", src, app)
	}
	interactive := isTerminal()

	// 2. Confirm replacing an existing configuration.
	if _, err := os.Stat(dest); err == nil && !*yes {
		old := config.Default()
		_, _, _ = config.LoadFile(dest, &old)
		if !interactive {
			return usageErr("%s already has a configuration (%s); use --yes to replace it", app, dest)
		}
		answer, err := ask(fmt.Sprintf("Replace the configuration for %s with %s? (y/N)", describe(old), describe(cfg)), "N")
		if err != nil {
			return err
		}
		if !strings.EqualFold(answer, "y") && !strings.EqualFold(answer, "yes") {
			logx.Infof("Nothing changed.")
			return nil
		}
	}

	// 3. Test the credential (reported, never blocking: the classroom may be
	// offline right now).
	var prompt auth.PromptFunc
	if interactive {
		prompt = promptSecret
	}
	if am, err := auth.Method(cfg, auth.Options{AppName: app, Prompt: prompt}); err != nil {
		logx.Warnf("could not test access: %v", err)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), accessCheckTimeout)
		err := repo.CheckAccess(ctx, cfg.Repo.URL, am)
		cancel()
		if err != nil {
			logx.Warnf("could not confirm access to %s: %v (installing the configuration anyway)", cfg.Repo.URL, err)
		} else {
			logx.Infof("Access to %s confirmed", cfg.Repo.URL)
		}
	}

	// 4. Install it, then remove the source.
	if err := config.WriteRaw(dest, data); err != nil {
		return fmt.Errorf("installing configuration: %w", err)
	}
	logx.Infof("Configuration installed for %s", app)
	logx.Debugf("installed at %s", dest)
	if !*keep {
		if err := os.Remove(src); err != nil {
			logx.Warnf("could not remove %s: %v", src, err)
		} else {
			logx.Infof("Removed %s", src)
		}
	}

	// 5. Whatever is running keeps the old configuration until restarted.
	serviceRunning := false
	if st, err := svc.Control(svc.Config{AppName: app}, "status"); err == nil && st == "running" {
		serviceRunning = true
		if runtime.GOOS == "windows" {
			logx.Warnf("the %s service is running with the old configuration; in an Administrator terminal run: %s service restart", app, app)
		} else if _, err := svc.Control(svc.Config{AppName: app}, "restart"); err != nil {
			logx.Warnf("restarting the %s service failed: %v; run: %s service restart", app, err, app)
		} else {
			logx.Infof("Restarted the %s service", app)
		}
	}
	if !serviceRunning {
		probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		res, _ := server.Probe(probeCtx, cfg.Server.Listen, app)
		cancel()
		if res == server.ProbeSelf {
			logx.Warnf("%s is running with the old configuration; close it and start it again", app)
			return nil
		}
	}

	// 6. Start it.
	if !interactive {
		logx.Infof("Run `%s` to start.", app)
		return nil
	}
	logx.Infof("")
	return cmdRun(app, nil)
}

// describe names a configuration for the replace prompt.
func describe(c config.Config) string {
	if c.Repo.URL == "" {
		return "(unknown repository)"
	}
	if c.Repo.Branch != "" {
		return c.Repo.URL + " (" + c.Repo.Branch + ")"
	}
	return c.Repo.URL
}

func readLimited(path string, limit int64) ([]byte, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file")
	}
	if fi.Size() > limit {
		return nil, fmt.Errorf("file is larger than %d bytes", limit)
	}
	return os.ReadFile(path)
}

func sameFile(a, b string) bool {
	fa, err1 := os.Stat(a)
	fb, err2 := os.Stat(b)
	return err1 == nil && err2 == nil && os.SameFile(fa, fb)
}
