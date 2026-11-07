# go-git-serve

A single-file executable that keeps a local copy of a git repository up to
date and serves it as a web site, opened in its own chromeless browser window.

Hand someone one binary and they always get the latest version of a static
web app kept in a (private) git repository: no git install, no web server,
no setup beyond a URL and a read-only credential.

Builds are pure Go with no dependencies on Windows (amd64, arm64), Linux
(amd64, arm64) and macOS (Apple Silicon, Intel).

## Quick start

```sh
go-git-serve            # first run in a terminal asks a few questions, then:
                        # clones the repo, serves it, opens an app window
```

Closing the window stops the program. Each later run pulls the latest
changes first; if the pull fails (offline, say), the cached copy is served.

### One binary, many apps

Every path is derived from the executable's name. Copy or rename the binary
(`dashboard`, `docs-viewer.exe`, ...) and each copy has its own config,
cache, keychain entry and service.

| | Location |
|---|---|
| Config | `<user config dir>/<name>.toml` (`~/.config` on Linux, `~/Library/Application Support` on macOS, `%AppData%` on Windows) |
| Cache (checkout, browser profile, service log) | `<user cache dir>/<name>/` |

## Commands

```
go-git-serve [run]                 sync, serve and open the site (default)
go-git-serve init                  create the config file interactively
go-git-serve sync                  clone or pull, then exit
go-git-serve auth set-token        store an access token in the OS keychain
go-git-serve auth clear            remove the stored token
go-git-serve service install|uninstall|start|stop|restart|status
go-git-serve version | help
```

Main flags (run `go-git-serve help` for all): `-c/--config`, `-l/--listen`,
`--repo`, `--branch`, `--subdir`, `--full`, `--auth`, `--key-file`, `--token-file`,
`--offline`, `--interval 15m`, `--randomize-start=false`, `--browser app|default|none`, `--no-browser`,
`-d/--daemon`, `--log-file`, `-v/--verbose`, `-q/--quiet`.

Exit codes: `0` ok, `1` runtime error (e.g. port in use, clone failed),
`2` configuration or usage error.

## Configuration

Settings are resolved as **defaults < config file < `GGS_*` environment
variables < command-line flags**.

```toml
[repo]
url    = 'git@github.com:org/app.git'  # https://, ssh://, scp-style, or a local path
branch = 'main'                        # optional; default: the repository's default branch
subdir = 'dist'                        # optional; serve only this folder
shallow = true                         # latest commit only (default); false keeps full history

[auth]
method   = 'ssh-key'                   # none | ssh-key | ssh-agent | token (default: guessed from url)
key_file = '~/.ssh/app_deploy_key'
# token_file = '~/.config/app.token'

[server]
listen = '127.0.0.1:8888'

[update]
on_start = true                        # pull when starting
interval = '0'                         # e.g. '15m' to re-pull while running
randomize_start = true                 # first re-pull at a random time within the first interval

[browser]
mode = 'app'                           # app | default | none
# path = '/path/to/chrome'             # app mode; default: auto-detect
```

Use single quotes for Windows paths (`'C:\Users\me\key'`) or forward slashes.

Environment variables: `GGS_REPO_URL`, `GGS_REPO_BRANCH`, `GGS_REPO_SUBDIR`,
`GGS_REPO_SHALLOW`,
`GGS_AUTH_METHOD`, `GGS_AUTH_KEY_FILE`, `GGS_AUTH_TOKEN_FILE`,
`GGS_SERVER_LISTEN`, `GGS_UPDATE_ON_START`, `GGS_UPDATE_INTERVAL`,
`GGS_UPDATE_RANDOMIZE_START`,
`GGS_BROWSER_MODE`, `GGS_BROWSER_PATH`, plus the secrets `GGS_AUTH_TOKEN` and
`GGS_AUTH_PASSPHRASE`.

### Periodic updates and classrooms

With `interval` set, the first periodic check happens at a random moment
within the first interval, and every later check follows one interval after
the previous one finished. When many people start the app at the same time
(a classroom), their checks are therefore spread across the interval instead
of all hitting the git server together. A check that finds nothing new costs
only a small exchange of branch names. Disable with `randomize_start = false`.

### Shallow clones

By default only the latest commit is downloaded (`git clone --depth 1`), which
for repositories with a long history is several times smaller and faster.
Updates fetch only the new latest commit. If an update of a shallow checkout
fails for any reason other than the network or credentials, the checkout is
replaced by a fresh shallow clone (the running site switches over without a
restart). Set `shallow = false` or pass `--full` to keep the full history
instead; switching to full re-clones once.

Every update that brings changes adds a file to `.git`, and git's usual
clean-up does not run here, so after 30 such updates the checkout is
compacted: re-cloned when shallow, repacked when full.

Git LFS is not supported: files stored in LFS are served as their small
pointer files.

## Authentication

Secrets never go in the config file.

| Method | Use | Secret lives in |
|---|---|---|
| `none` | public repositories | - |
| `ssh-key` | a per-repository **read-only deploy key** | key file; a passphrase is asked for only if the key has one (or `GGS_AUTH_PASSPHRASE`) |
| `ssh-agent` | a key loaded in ssh-agent, Pageant, the Windows OpenSSH agent, or 1Password/Bitwarden agents | the agent |
| `token` | HTTPS with a **fine-grained personal access token**: one repository, read-only *Contents*, with an expiry | `GGS_AUTH_TOKEN`, else `token_file`, else the OS keychain (`auth set-token`) |

SSH servers are verified against `~/.ssh/known_hosts` (or `SSH_KNOWN_HOSTS`),
falling back to GitHub's published host keys built into the binary. Unknown
hosts are refused, not trusted on first use. Key and token files readable by
other users trigger a warning on Linux and macOS.

## Browser

- `app` (default): opens a chromeless window using Chrome, Edge, Chromium or
  Brave with a dedicated profile. Closing the window stops the program. Falls
  back to the default browser if none is installed.
- `default`: opens the system default browser; stop with Ctrl+C.
- `none`: only prints the URL.

On Linux without a display (e.g. over SSH) the URL is printed instead.

## Running in the background

`--daemon` runs unattended: no prompts, no browser, timestamped logs. To have
the OS keep it running, install it as a service (it reads the config file):

```sh
go-git-serve service install   # systemd --user (Linux), LaunchAgent (macOS), Windows service
go-git-serve service start
```

Notes: a service reads **only the config file**. Flags and `GGS_*`
environment variables given to `service install` are not passed to it
(install warns if you use them), so put every setting in the file. Services cannot prompt, so use a deploy key without a passphrase or a
token. On Linux, run `loginctl enable-linger $USER` to keep the service running
while logged out. Windows services run as LocalSystem, which cannot read your
keychain: use `token_file` and absolute paths. Logs go to
`<cache dir>/<name>/service.log`.

## Running more than one copy

Copies of the same app (same executable name) share one config, cache and
address, and coordinate with each other:

- **Starting it again** while it is already running (in a terminal or as a
  service) just opens the running site and exits; it does not pull or serve
  a second time. A second `--daemon` copy exits with an error instead.
- **Updates never overlap.** Clones, pulls and periodic updates take a lock
  on the checkout; another copy waits up to 30 seconds, then reports the
  process ID that holds it.
- **The checkout is never replaced while it is being served.** If `url` or
  `branch` changed, the re-clone is refused until the running copy stops
  (`sync` exits with an error; `run` serves the cached copy and says so).
- **Crashes leave nothing stuck.** Locks are operating-system file locks
  (`flock` / `LockFileEx`), released automatically when a process exits in
  any way; the leftover `repo.lock` and `serve.lock` files are harmless.
- If the address is taken by a different app or another program, the
  program exits with an error before touching the checkout.

While a copy is still cloning or pulling, its address shows a short
"starting" page that refreshes itself.

## Security

- Serves only on `127.0.0.1` by default; never serves `.git`, nor files
  outside the served folder (including via symlinks).
- Rejects requests for other host names (DNS-rebinding protection) unless
  listening on all interfaces.
- The checkout is a read-only cache: local changes are discarded on update.

## Building

```sh
make build     # this platform
make all       # all six targets into dist/
make test
```

Requires Go 1.26 or newer. Releases are built by GoReleaser when a `v*` tag is
pushed (`.github/workflows/release.yml`). Binaries are unsigned: on macOS,
allow them in *System Settings > Privacy & Security* or run
`xattr -d com.apple.quarantine <file>`; on Windows, choose *More info > Run
anyway* in SmartScreen.

Every CI run also uploads the six binaries as an artifact named
`go-git-serve-<commit>` (Actions → run → *Artifacts*, kept 30 days), for
testing before a release.

See [docs/DESIGN.md](docs/DESIGN.md) for the design decisions and
[docs/MANUAL-TESTING.md](docs/MANUAL-TESTING.md) for the manual test checklist.
