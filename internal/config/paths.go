package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultAppName is used when the executable name cannot be determined.
const DefaultAppName = "go-git-serve"

// Paths holds the per-app locations on disk. Every path is derived from the
// executable's name, so copies of the binary under different names keep
// separate configs and caches.
type Paths struct {
	AppName    string // executable name without extension
	ConfigFile string // <user config dir>/<app>.toml
	CacheDir   string // <user cache dir>/<app>
	RepoDir    string // <cache>/repo        - the git checkout
	ProfileDir string // <cache>/browser     - app-window browser profile
	LogFile    string // <cache>/service.log - used by installed services
}

// AppName derives the app name from argv[0], dropping any extension (.exe).
func AppName(arg0 string) string {
	base := filepath.Base(arg0)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return DefaultAppName
	}
	return base
}

// DefaultPaths computes the standard locations for app.
func DefaultPaths(app string) (Paths, error) {
	cfgDir, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("cannot determine config directory: %w", err)
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return Paths{}, fmt.Errorf("cannot determine cache directory: %w", err)
	}
	cache := filepath.Join(cacheDir, app)
	return Paths{
		AppName:    app,
		ConfigFile: filepath.Join(cfgDir, app+".toml"),
		CacheDir:   cache,
		RepoDir:    filepath.Join(cache, "repo"),
		ProfileDir: filepath.Join(cache, "browser"),
		LogFile:    filepath.Join(cache, "service.log"),
	}, nil
}
