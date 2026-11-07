# Design

go-git-serve turns a git repository holding a static web app into a
double-clickable desktop app: one binary pulls the latest version and shows
it in its own window. This document records the decisions behind it.

## Decisions

| Area | Decision | Why |
|---|---|---|
| Config format | TOML, `<user config dir>/<exe name>.toml`, `--config` override | Readable, comments, no indentation pitfalls |
| Precedence | defaults < file < `GGS_*` env < flags | Containers and services can configure via env; flags win for one-offs |
| Missing config | Interactive wizard on a terminal; otherwise write a commented template and exit 2 | Friendly for people, predictable for services |
| Per-app identity | Every path, keychain entry and service name derives from the executable name | Copies of one binary act as independent apps |
| Repo options | `url`, optional `branch`, optional `subdir` | Serve build output (`dist/`) or a `gh-pages` branch |
| Auth | `none`, `ssh-key`, `ssh-agent`, `token` | Covers public repos, deploy keys, agents/password managers and HTTPS-only networks |
| Secrets | Never in the config file. Token: env, then file, then OS keychain. Passphrase: prompted only if the key is encrypted, or `GGS_AUTH_PASSPHRASE` | Config files get copied and backed up |
| SSH host keys | `known_hosts`, falling back to GitHub's built-in keys; unknown hosts refused | go-git otherwise needs a known_hosts file; trust-on-first-use is unsafe |
| Commands | `run` (default), `init`, `sync`, `auth`, `service`, `version` | Small surface; flags via `spf13/pflag` (GNU style) |
| Daemon | `--daemon` = unattended foreground run; `service` delegates backgrounding to systemd / launchd / Windows SCM via `kardianos/service` | Go cannot fork reliably (especially on Windows); service managers give restart, boot start and logging |
| Failed pull | Serve the cached copy with a warning | The app stays usable offline |
| Port in use | Exit with an error (code 1) | Predictable URL; avoids two copies racing |
| Platforms | windows, linux, darwin x amd64, arm64; `CGO_ENABLED=0` | Cross-compile everything from one machine; static Linux binaries |
| Releases | GitHub Actions: tests on Linux/Windows/macOS; GoReleaser on `v*` tags; unsigned | Signing needs paid certificates; can be added later |
| Browser | `app` mode by default: Chromium-family `--app` window with a dedicated profile; closing it stops the program. Fallback to the default browser; `none` prints the URL | Matches the original "self-contained window" idea without lorca (archived) or cgo webviews |
| Web security | Loopback by default; `.git` never served; files confined with `os.Root`; Host header checked against the listen address | Prevents leaking repo metadata, symlink escapes and DNS rebinding |

## Layout

```
main.go, run.go, init.go, commands.go   CLI: commands, serve loop, setup wizard
internal/config    settings, precedence, validation, template rendering
internal/auth      auth methods, host key verification, keychain
internal/repo      clone / fetch + hard reset of the cached checkout
internal/server    HTTP handler (.git blocking, host check), URL helpers
internal/browser   app window / default browser launching
internal/svc       OS service integration
internal/logx      levelled logging (timestamps in daemon mode)
```

## Behaviour details

- **Sync.** First run clones (single branch) into a temporary folder and
  moves it into place, so a failed clone never destroys a working cache. Later
  runs fetch the checked-out branch and hard-reset to it. If `url` or `branch`
  changes, the cache is re-cloned at the next start; periodic updates only
  update in place.
- **App-window detection.** The browser runs with its own profile so the
  launched process lives exactly as long as the window. If it exits within
  3 seconds it is assumed to have handed off to an existing browser process,
  and the program keeps serving until Ctrl+C.
- **Services.** Installed with `run --daemon --config <abs path> --log-file
  <cache>/service.log`. Per-user services on Linux and macOS (own config,
  cache and keychain); a LocalSystem service on Windows.

## Not included (possible later work)

- GitHub App or browser-based (device flow) login.
- Code signing and notarisation.
- Serving while a re-clone happens (re-clones only occur at start-up).
