package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/meeas/go-git-serve/internal/bundle"
	"github.com/meeas/go-git-serve/internal/config"
	"github.com/meeas/go-git-serve/internal/logx"
)

var (
	bundleOnce sync.Once
	bundled    *bundle.Bundle
	bundleErr  error
)

// currentBundle returns the configuration bundled into this executable, or
// nil. A damaged bundle is reported once by realMain.
func currentBundle() *bundle.Bundle {
	bundleOnce.Do(func() { bundled, bundleErr = bundle.Current() })
	return bundled
}

// bundleUnlockedFlags may still be used with a bundled executable: they
// affect how the app runs on this machine, not what it serves or how it
// authenticates. Everything else is fixed by the bundle.
var bundleUnlockedFlags = []string{
	"verbose", "quiet", "browser", "browser-path", "no-browser",
	"listen", "log-file", "daemon", "offline",
}

var validAppName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func cmdBundle(app string, args []string) error {
	f := newFlags(app)
	output := f.fs.StringP("output", "o", "", "bundled executable to write")
	base := f.fs.String("base", "", "executable to bundle into, e.g. a release binary for another OS (default: this one)")
	name := f.fs.String("name", "", "app name stored in the bundle (default: the output file name)")
	show := f.fs.Bool("show", false, "show the configuration bundled into an executable (default: this one), secrets masked")
	if err := f.fs.Parse(args); err != nil {
		return usageErr("%v", err)
	}
	if _, err := f.setupLogging(); err != nil {
		return err
	}

	if *show {
		target := f.fs.Arg(0)
		if target == "" {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			target = exe
		}
		return showBundle(target)
	}

	if f.fs.NArg() != 1 || *output == "" {
		return usageErr("usage: %s bundle <config.toml> -o <output> [--base <executable>] [--name <app name>]\n       %s bundle --show [executable]", app, app)
	}
	src := f.fs.Arg(0)
	data, err := readLimited(src, maxConfigSize)
	if err != nil {
		return usageErr("reading %s: %v", src, err)
	}
	cfg := config.Default()
	unknown, err := config.Parse(string(data), &cfg)
	if err != nil {
		return usageErr("%s is not a valid configuration: %v", src, err)
	}
	for _, k := range unknown {
		logx.Warnf("%s: unknown setting %q ignored", src, k)
	}
	check := cfg
	if err := config.Validate(&check); err != nil {
		return usageErr("%s is not a valid configuration: %v", src, err)
	}
	if cfg.Auth.KeyFile != "" || cfg.Auth.TokenFile != "" {
		logx.Warnf("the configuration refers to a key or token file on this computer, which students will not have; use the inline auth.key or auth.token instead")
	}

	basePath := *base
	if basePath == "" {
		if basePath, err = os.Executable(); err != nil {
			return err
		}
	}
	bin, err := os.ReadFile(basePath)
	if err != nil {
		return usageErr("reading base executable: %v", err)
	}
	outPath, err := filepath.Abs(*output)
	if err != nil {
		return err
	}
	if sameFile(basePath, outPath) {
		return usageErr("the output must be a new file, not the base executable")
	}
	appName := *name
	if appName == "" {
		appName = config.AppName(outPath)
	}
	if !validAppName.MatchString(appName) {
		return usageErr("app name %q must be letters, digits, '.', '_' or '-' (set it with --name)", appName)
	}
	platform := bundle.Platform(bin)
	if platform == "windows" && !strings.EqualFold(filepath.Ext(outPath), ".exe") {
		logx.Warnf("the base is a Windows executable but %s does not end in .exe", filepath.Base(outPath))
	}

	out, err := bundle.Write(bin, bundle.Bundle{Name: appName, Config: string(data)})
	if err != nil {
		return err
	}
	if err := writeExecutable(outPath, out); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	logx.Infof("Bundled %q for %s: %s (serves %s)", appName, platform, outPath, cfg.Repo.URL)
	if platform == "darwin" {
		logx.Infof("The macOS code signature was recomputed (ad-hoc).")
	}
	return nil
}

func showBundle(path string) error {
	bin, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	b, err := bundle.Read(bin)
	if err != nil {
		return err
	}
	if b == nil {
		fmt.Printf("%s has no bundled configuration.\n", path)
		return nil
	}
	cfg := config.Default()
	if _, err := config.Parse(b.Config, &cfg); err != nil {
		return fmt.Errorf("bundled configuration is invalid: %w", err)
	}
	fmt.Printf("# Bundled into %s (%s) as app %q; secrets masked.\n", filepath.Base(path), bundle.Platform(bin), b.Name)
	fmt.Print(config.Render(cfg.Redacted()))
	return nil
}

// writeExecutable writes data to path with execute permission, atomically.
func writeExecutable(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".bundle-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil && !os.IsPermission(err) {
		// Windows ignores Unix modes.
		logx.Debugf("chmod: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
