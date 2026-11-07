# Manual testing checklist

CI runs the unit tests on Linux, Windows and macOS and cross-builds every
target. This checklist covers what CI cannot: real GitHub credentials, OS
keychains, SSH agents, real browser windows, OS services, and multi-process
behaviour on a real desktop.

Test the latest commit on the branch; it contains every earlier change.
Tick items as you go (copy this file into an issue or PR comment to keep a
record per release). Unless marked **[Linux]** or **[Windows]**, run every
item on both platforms.

## 0. Setup

**Binaries.** Every CI run uploads an artifact named `go-git-serve-<commit>`
(Actions → the run → *Artifacts*, kept 30 days) containing all six binaries
and `checksums.txt`. Alternatively run `make build`.

- [ ] `go-git-serve version` prints `0.0.0-SNAPSHOT-<commit>` and the right OS/arch.
- [ ] **[Windows]** The unsigned binary triggers SmartScreen ("More info → Run anyway") only on first run.

**Test repositories on GitHub** (all small, with an `index.html` at the root
and another in `dist/`):

| Repo | Purpose |
|---|---|
| `ggs-test-public` | public, for `auth = none` |
| `ggs-test-private` | private; add a second branch `pages` with a different `index.html` |

Credentials for `ggs-test-private`:
- a **read-only deploy key** without a passphrase, and a second one *with* a passphrase;
- a **fine-grained personal access token**: only this repo, *Contents: Read-only*, short expiry.

Use a test copy of the binary named e.g. `ggs` (`ggs.exe`), so its config
(`~/.config/ggs.toml` / `%AppData%\ggs.toml`) and cache don't touch anything else.
To reset between sections: delete the config file and the cache folder
(`~/.cache/ggs` / `%LocalAppData%\ggs`).

**Reporting a problem:** include `ggs version`, the OS version, the command,
and output with `-v`.

## 1. Configuration and first run

- [ ] With no config, run `ggs` in a terminal: the wizard asks the questions, writes the config, then serves.
- [ ] With no config, run non-interactively (`ggs < /dev/null` **[Linux]**, `ggs < NUL` **[Windows]**): writes a commented template, exits code 2, does not overwrite an existing file on a second run.
- [ ] `ggs init` with an existing config asks before overwriting.
- [ ] Config file contains no secrets after choosing `token`.
- [ ] **[Linux]** Config and token files are created mode `600`.
- [ ] Env var overrides a file setting (`GGS_SERVER_LISTEN=127.0.0.1:9999 ggs`), and a flag overrides the env var (`--listen 127.0.0.1:9998`).
- [ ] A typo'd key in the TOML (e.g. `lisen =`) prints an "unknown setting" warning.
- [ ] **[Windows]** A single-quoted backslash path works: `key_file = 'C:\Users\me\.ssh\ggs_deploy'`. A `~\.ssh\...` path also works.
- [ ] **[Windows]** App name drops `.exe`: `ggs.exe` uses `ggs.toml`.
- [ ] A copy renamed to `ggs2` uses its own `ggs2.toml` and cache.

## 2. Authentication against GitHub

- [ ] `none`: `ggs-test-public` over `https://` clones and serves.
- [ ] `ssh-key`, no passphrase: `git@github.com:<you>/ggs-test-private.git` clones; **no** passphrase prompt.
- [ ] `ssh-key` with passphrase: prompted once (input hidden); wrong passphrase gives a clear error.
- [ ] `ssh-key` with passphrase and `GGS_AUTH_PASSPHRASE` set: no prompt.
- [ ] `ssh-key` with passphrase, non-interactive (`--daemon`) and no env var: clear error naming `GGS_AUTH_PASSPHRASE`.
- [ ] **[Linux]** Key file mode `644` prints a "chmod 600" warning.
- [ ] `ssh-agent`: key loaded with `ssh-add` **[Linux]**, or in the *OpenSSH Authentication Agent* service **[Windows]**; clones without a key file.
- [ ] `ssh-agent` with the agent stopped (or no keys loaded): clear error with a hint.
- [ ] **[Windows]** (optional) `ssh-agent` with Pageant, or the 1Password / Bitwarden SSH agent.
- [ ] `token` via `GGS_AUTH_TOKEN`: clones over `https://`.
- [ ] `token` via `token_file`.
- [ ] `token` via keychain: `ggs auth set-token` (hidden prompt), then run with no env var or file.
  - [ ] **[Windows]** Entry appears in *Credential Manager → Windows Credentials* as `go-git-serve`.
  - [ ] **[Linux]** Works in a desktop session (GNOME Keyring / KWallet). Over plain SSH without a keyring, the error suggests `token_file` / env var.
- [ ] `echo <token> | ggs auth set-token` (piped) works.
- [ ] `ggs auth clear` removes it; running again reports no token available.
- [ ] Expired or revoked token: clear error; if a cached copy exists, it is served with a warning.
- [ ] A URL with credentials (`https://user:tok@github.com/...`) is rejected with exit code 2.

### Host key verification (SSH)

Use a throwaway `SSH_KNOWN_HOSTS` file so your real one is untouched.

- [ ] Empty or missing known_hosts: GitHub is still verified (built-in keys) and the clone works.
- [ ] known_hosts with a **wrong** `github.com ssh-ed25519 ...` line: refuses with "SSH HOST KEY MISMATCH".
- [ ] A non-GitHub SSH host not in known_hosts: refused with a `ssh-keyscan` hint (skip if you have no other SSH git host).

## 3. Sync behaviour

- [ ] Push a commit to the repo, run again: "Updated main to …" and the new content is served.
- [ ] Run with nothing new: "Up to date".
- [ ] `--interval 1m`: push while running; within about a minute the page shows the change after a reload.
- [ ] `--subdir dist` serves `dist/index.html`; a non-existent subdir gives a clear error.
- [ ] `--branch pages`: re-clones ("branch changed … re-cloning") and serves the `pages` content; switching back works.
- [ ] Disconnect the network, run: "update failed … serving the cached copy", site still works.
- [ ] `--offline` serves the cache without network; with an empty cache, a clear error.
- [ ] `ggs sync` clones or pulls and exits 0 without serving.
- [ ] Delete a file inside the cached checkout, then pull a new commit: the checkout is restored to match the remote.

## 4. Browser

- [ ] `app` mode (default) with Chrome **and** Edge **[Windows]** / Chrome or Chromium **[Linux]**: a window with no tabs or address bar opens on the site.
- [ ] Closing the app window stops the program ("Window closed; stopping").
- [ ] `app` mode while your normal Chrome/Edge is already open: still a separate app window, and closing it still stops the program.
- [ ] `browser.path` pointing at a specific browser is used.
- [ ] **[Linux]** No Chromium-family browser installed: falls back to the default browser.
- [ ] `--browser default` opens the default browser; Ctrl+C stops the program.
- [ ] `--browser none` / `--no-browser` only prints the URL.
- [ ] **[Linux]** Over SSH with no display: prints the URL, no errors.
- [ ] **[Linux]** (if available) Works on both Wayland and X11 sessions.

## 5. Multiple copies

- [ ] Start `ggs`, then start `ggs` again in a second terminal: the second prints "already running … opening it", opens a window, exits 0, and does not pull.
- [ ] Start a copy on a fresh, large-ish repo and immediately start a second: the second opens the "starting" page, which refreshes into the site.
- [ ] `ggs --daemon` while one is running: exits 1, "already running".
- [ ] `ggs2` (renamed copy) configured on the same port: exits 1, "in use by another go-git-serve app (ggs)".
- [ ] Another program on the port (e.g. `python -m http.server 8888`): exits 1, "in use by another program", before any git activity.
- [ ] With a copy running, `ggs sync --repo <a different repo URL>`: refused ("being served by another running copy").
- [ ] Kill a running copy hard (`kill -9` **[Linux]**, Task Manager → *End task* **[Windows]**), then start again: starts normally (leftover `repo.lock` / `serve.lock` files are harmless).
- [ ] Kill the app-window copy while its browser window stays open, then start again: starts normally.

## 6. Background service

**[Linux]** (systemd user service)
- [ ] `ggs service install` (no sudo) → `ggs service start` → `ggs service status` shows *running*.
- [ ] `systemctl --user status ggs` shows it; the site is reachable; log at `~/.cache/ggs/service.log` has timestamps.
- [ ] Start `ggs` interactively while the service runs: opens the service's site and exits.
- [ ] `kill -9` the service process: systemd restarts it.
- [ ] Log out and back in (or reboot): starts at login. With `loginctl enable-linger $USER`, it keeps running after logout.
- [ ] `ggs service stop` / `uninstall` remove it cleanly.

**[Windows]** (Windows service, runs as LocalSystem; use an **Administrator** terminal)
- [ ] Use `ssh-key` with an absolute `key_file`, or `token` with an absolute `token_file` (the keychain is per-user and not visible to the service).
- [ ] `ggs service install` warns about `~` paths or keychain tokens if configured that way.
- [ ] `ggs service install` → `start` → `status` shows *running*; it appears in `services.msc`.
- [ ] Site reachable; log at `%LocalAppData%\ggs\service.log` (your user's folder, as printed by install).
- [ ] Start `ggs` interactively (normal, non-admin terminal) while the service runs: opens the service's site and exits.
- [ ] End the service process in Task Manager: it restarts after about 10 seconds.
- [ ] Reboot: the service starts automatically.
- [ ] `ggs service stop` / `uninstall` remove it cleanly.

## 7. Security spot checks

With a copy running on the default `127.0.0.1:8888`:

- [ ] `curl -i http://127.0.0.1:8888/.git/config` → 404.
- [ ] `curl -i -H "Host: evil.example" http://127.0.0.1:8888/` → 421.
- [ ] `curl -i -X POST http://127.0.0.1:8888/` → 405.
- [ ] **[Linux]** Commit a symlink to a file outside the repo (e.g. `ln -s /etc/hostname leak.txt`); `/leak.txt` is not served.
- [ ] Not reachable from another machine by default.
- [ ] `--listen 0.0.0.0:8888`: prints the "all network interfaces" warning and is reachable from another machine.
  - [ ] **[Windows]** The Defender Firewall prompt appears only in this case.
- [ ] Nothing secret (token, passphrase, key) appears in output with `-v`.

## 8. Exit codes and stopping

- [ ] Ctrl+C stops cleanly ("Stopping"), exit code 0.
- [ ] Config or usage error (e.g. `--browser kiosk`): exit code 2.
- [ ] Runtime error (e.g. clone of a non-existent repo with no cache): exit code 1.

## Not covered here

macOS (Apple Silicon and Intel) is built and unit-tested in CI only. Before
calling a release macOS-ready, repeat sections 1–8 there, with `launchd`
(LaunchAgent) in place of systemd and the macOS Keychain for tokens.
