# Manual testing checklist

CI runs the unit tests on Linux, Windows and macOS and cross-builds every
target. This checklist covers what CI cannot: real GitHub credentials, OS
keychains, SSH agents, real browser windows, OS services, and multi-process
behaviour on a real desktop.

Test the latest commit on the branch; it contains every earlier change.
Tick items as you go (copy this file into an issue or PR comment to keep a
record per release). Items marked **[Linux]**, **[Windows]** or **[macOS]**
apply only to those platforms; everything else applies to all three.

## 0. Setup

**Binaries.** Every CI run uploads an artifact named `go-git-serve-<commit>`
(Actions → the run → *Artifacts*, kept 30 days) containing all six binaries
and `checksums.txt`. Alternatively run `make build`.

| Platform | Binary |
|---|---|
| Linux | `go-git-serve_linux_amd64` |
| Windows | `go-git-serve_windows_amd64.exe` |
| macOS, Apple Silicon | `go-git-serve_darwin_arm64` |
| macOS, Intel | `go-git-serve_darwin_amd64` |

- [ ] **[Linux, macOS]** The artifact zip does not keep the executable bit: run `chmod +x` on the binary.
- [ ] `go-git-serve version` prints `0.0.0-SNAPSHOT-<commit>` and the right OS/arch.
- [ ] **[Windows]** The unsigned binary triggers SmartScreen ("More info → Run anyway") only on first run.
- [ ] **[macOS]** A binary downloaded with a browser is blocked by Gatekeeper ("cannot be verified"). Either *System Settings → Privacy & Security → Open Anyway*, or `xattr -d com.apple.quarantine <file>`; afterwards it runs without prompting.
- [ ] **[macOS]** (optional) On Apple Silicon, the `darwin_amd64` binary also runs (under Rosetta).

**Test repositories on GitHub** (all small, with an `index.html` at the root
and another in `dist/`):

| Repo | Purpose |
|---|---|
| `ggs-test-public` | public, for `auth = none` |
| `ggs-test-private` | private; add a second branch `pages` with a different `index.html` |

Credentials for `ggs-test-private`:
- a **read-only deploy key** without a passphrase, and a second one *with* a passphrase;
- a **fine-grained personal access token**: only this repo, *Contents: Read-only*, short expiry.

Use a test copy of the binary named e.g. `ggs` (`ggs.exe`), so its config and
cache don't touch anything else:

| Platform | Config | Cache (checkout, browser profile, service log) |
|---|---|---|
| Linux | `~/.config/ggs.toml` | `~/.cache/ggs/` |
| Windows | `%AppData%\ggs.toml` | `%LocalAppData%\ggs\` |
| macOS | `~/Library/Application Support/ggs.toml` | `~/Library/Caches/ggs/` |

To reset between sections, delete the config file and the cache folder.

**Reporting a problem:** include `ggs version`, the OS version, the command,
and output with `-v`.

## 1. Configuration and first run

- [ ] With no config, run `ggs` in a terminal: the wizard asks the questions, writes the config, then serves.
- [ ] With no config, run non-interactively (`ggs < /dev/null` **[Linux, macOS]**, `ggs < NUL` **[Windows]**): writes a commented template, exits code 2, does not overwrite an existing file on a second run.
- [ ] Config is written to the location in the table above.
- [ ] `ggs init` with an existing config asks before overwriting.
- [ ] In the wizard, long answers (repository URL, file paths, token) are typed on their own line after `> `.
- [ ] Arrow keys, Home/End and backspace edit the current answer (e.g. fix a typo in the branch name); Ctrl+C cancels with "cancelled".
- [ ] Pasting a token shows one `*` per character; backspace removes them.
- [ ] `ssh-key` → `paste`: a pasted private key (shown on screen) is accepted at its `-----END ... PRIVATE KEY-----` line; a broken paste is rejected and asked again; a passphrase-protected key gets the passphrase note.
- [ ] Token stored in `config`, key stored in `config`: the config file then holds `token = '...'` / `key = '''...'''` and works on its own.
- [ ] **[Windows]** All of the above in both Windows Terminal and the classic console (cmd.exe window): no stray escape characters when using arrow keys.
- [ ] Config file contains no secrets after choosing `token`.
- [ ] **[Linux, macOS]** Config and token files are created mode `600`.
- [ ] Env var overrides a file setting (`GGS_SERVER_LISTEN=127.0.0.1:9999 ggs`), and a flag overrides the env var (`--listen 127.0.0.1:9998`).
- [ ] A typo'd key in the TOML (e.g. `lisen =`) prints an "unknown setting" warning.
- [ ] **[Windows]** A single-quoted backslash path works: `key_file = 'C:\Users\me\.ssh\ggs_deploy'`. A `~\.ssh\...` path also works.
- [ ] **[Windows]** App name drops `.exe`: `ggs.exe` uses `ggs.toml`.
- [ ] **[macOS]** Paths with spaces work (the config folder itself is under `Application Support`).
- [ ] A copy renamed to `ggs2` uses its own `ggs2.toml` and cache.

## 2. Authentication against GitHub

- [ ] `none`: `ggs-test-public` over `https://` clones and serves.
- [ ] `ssh-key`, no passphrase: `git@github.com:<you>/ggs-test-private.git` clones; **no** passphrase prompt.
- [ ] `ssh-key` with passphrase: prompted once (input hidden); wrong passphrase gives a clear error.
- [ ] `ssh-key` with passphrase and `GGS_AUTH_PASSPHRASE` set: no prompt.
- [ ] `ssh-key` with passphrase, non-interactive (`--daemon`) and no env var: clear error naming `GGS_AUTH_PASSPHRASE`.
- [ ] **[Linux, macOS]** Key file mode `644` prints a "chmod 600" warning.
- [ ] `ssh-agent`: clones without a key file, with the key loaded in:
  - [ ] **[Linux]** `ssh-agent` + `ssh-add`.
  - [ ] **[Windows]** the *OpenSSH Authentication Agent* service (`ssh-add`).
  - [ ] **[macOS]** the built-in agent (`ssh-add --apple-use-keychain ~/.ssh/<key>`); `SSH_AUTH_SOCK` is set by default in Terminal.
- [ ] `ssh-agent` with the agent stopped (or no keys loaded): clear error with a hint.
- [ ] (optional) `ssh-agent` via the 1Password or Bitwarden SSH agent; **[Windows]** also Pageant.
- [ ] `token` via `GGS_AUTH_TOKEN`: clones over `https://`.
- [ ] `token` via `token_file`.
- [ ] `token` via keychain: `ggs auth set-token` (hidden prompt), then run with no env var or file.
  - [ ] **[Windows]** Entry appears in *Credential Manager → Windows Credentials* as `go-git-serve`.
  - [ ] **[macOS]** Entry appears in *Keychain Access* (login keychain) as `go-git-serve`, account `ggs`. Note whether macOS asks to allow access, and whether "Always Allow" sticks across runs.
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

- [ ] First clone is shallow by default: the checkout's `.git/shallow` file exists.
- [ ] Push a commit to the repo, run again: "Updated main to …" and the new content is served.
- [ ] Run with nothing new: "Up to date".
- [ ] `--interval 1m`: push while running; within about a minute the page shows the change after a reload.
- [ ] `--interval 1m -v`: the log says "first periodic update in Ns" with N below 60, different on each run; later updates are one minute apart. With `--randomize-start=false` it says 1m0s.
- [ ] (classroom, optional) Start the app on several machines at the same time with `--interval 5m -v`: their first-update times differ.
- [ ] `--subdir dist` serves `dist/index.html`; a non-existent subdir gives a clear error.
- [ ] `--branch pages`: re-clones ("branch changed … re-cloning") and serves the `pages` content; switching back works.
- [ ] Disconnect the network, run: "update failed … serving the cached copy", site still works.
- [ ] `--offline` serves the cache without network; with an empty cache, a clear error.
- [ ] `ggs sync` clones or pulls and exits 0 without serving.
- [ ] Force-push `main` back to an older commit (`git reset --hard HEAD~2 && git push -f`), then update: the older content is served, with no "Re-cloning" message.
- [ ] Amend and force-push the latest commit, then update: served content matches, no "Re-cloning".
- [ ] `ggs sync --full`: "Re-cloning: switching to a full clone"; `.git/shallow` is gone. Running without `--full` again does not re-clone.
- [ ] (optional, slow) With `--interval 30s`, push 31+ changing commits over time: a "Re-cloned … (compacting the checkout)" message appears and the site keeps working without a restart.
- [ ] Delete a file inside the cached checkout, then pull a new commit: the checkout is restored to match the remote.
- [ ] **[macOS]** (only if you have a git server on your local network) macOS 15+ may ask to allow access to devices on the local network; allowing it lets the clone proceed.

## 4. Browser

- [ ] `app` mode (default): a window with no tabs or address bar opens on the site, using
  - [ ] **[Windows]** Chrome, and Edge (uninstall or rename Chrome's path, or set `browser.path`).
  - [ ] **[Linux]** Chrome or Chromium.
  - [ ] **[macOS]** Chrome, Edge, Chromium or Brave from `/Applications` or `~/Applications`.
- [ ] Closing the app window stops the program ("Window closed; stopping").
  - [ ] **[macOS]** **Known risk:** on macOS, closing a Chrome window usually leaves Chrome running (still in the Dock), so the program may not stop until you quit it with Cmd+Q. Record which happens: stop on window close, stop only on Cmd+Q, or never.
- [ ] `app` mode while your normal Chrome/Edge is already open: still a separate app window, and closing it still stops the program.
- [ ] **[macOS]** The app window shows as a separate app in the Dock and Cmd+Tab (it uses its own profile).
- [ ] `browser.path` pointing at a specific browser is used (**[macOS]** the binary inside the bundle, e.g. `/Applications/Google Chrome.app/Contents/MacOS/Google Chrome`).
- [ ] No Chromium-family browser installed (**[macOS]** Safari only): falls back to the default browser.
- [ ] `--browser default` opens the default browser (**[macOS]** including Safari); Ctrl+C stops the program.
- [ ] `--browser none` / `--no-browser` only prints the URL.
- [ ] **[Linux]** Over SSH with no display: prints the URL, no errors.
- [ ] **[Linux]** (if available) Works on both Wayland and X11 sessions.

## 5. Multiple copies

- [ ] Start `ggs`, then start `ggs` again in a second terminal: the second prints "already running … opening it", opens a window, exits 0, and does not pull.
- [ ] Start a copy on a fresh, large-ish repo and immediately start a second: the second opens the "starting" page, which refreshes into the site.
- [ ] `ggs --daemon` while one is running: exits 1, "already running".
- [ ] `ggs2` (renamed copy) configured on the same port: exits 1, "in use by another go-git-serve app (ggs)".
- [ ] Another program on the port (e.g. `python3 -m http.server 8888`): exits 1, "in use by another program", before any git activity.
- [ ] With a copy running, `ggs sync --repo <a different repo URL>`: refused ("being served by another running copy").
- [ ] Kill a running copy hard (`kill -9` **[Linux, macOS]**, Task Manager → *End task* **[Windows]**, Activity Monitor → *Force Quit* **[macOS]**), then start again: starts normally (leftover `repo.lock` / `serve.lock` files are harmless).
- [ ] Kill the app-window copy while its browser window stays open, then start again: starts normally.

## 6. Background service

**[Linux]** (systemd user service)
- [ ] `ggs service install` (no sudo) → `ggs service start` → `ggs service status` shows *running*.
- [ ] `ggs service install --interval 5m` (or with `GGS_UPDATE_INTERVAL` set) warns that it is not passed to the service; the service runs with the config file's value.
- [ ] With `interval` and `randomize_start = true` in the config, the service log shows its first periodic update at a random point within the first interval.
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

**[macOS]** (LaunchAgent, runs as you; no sudo)
- [ ] `ggs service install` creates `~/Library/LaunchAgents/ggs.plist` → `ggs service start` → `ggs service status` shows *running*.
- [ ] `launchctl list | grep ggs` shows it; the site is reachable; log at `~/Library/Caches/ggs/service.log` has timestamps.
- [ ] Start `ggs` interactively while the service runs: opens the service's site and exits.
- [ ] `kill -9` the service process: launchd restarts it.
- [ ] Log out and back in (or reboot): starts at login.
- [ ] With `token` from the keychain: the service can read it (note any keychain access prompt).
- [ ] With `ssh-agent`: record whether the service reaches the agent (install warns it may not).
- [ ] (optional) If the binary lives in `~/Downloads`, `~/Desktop` or `~/Documents`, note whether macOS privacy prompts block the service; moving it to e.g. `~/bin` avoids them.
- [ ] `ggs service stop` / `uninstall` remove it cleanly (plist gone).

## 7. Security spot checks

With a copy running on the default `127.0.0.1:8888`:

- [ ] `curl -i http://127.0.0.1:8888/.git/config` → 404.
- [ ] `curl -i -H "Host: evil.example" http://127.0.0.1:8888/` → 421.
- [ ] `curl -i -X POST http://127.0.0.1:8888/` → 405.
- [ ] **[Linux, macOS]** Commit a symlink to a file outside the repo (e.g. `ln -s /etc/hosts leak.txt`); `/leak.txt` is not served.
- [ ] Not reachable from another machine by default.
- [ ] `--listen 0.0.0.0:8888`: prints the "all network interfaces" warning and is reachable from another machine.
  - [ ] **[Windows]** The Defender Firewall prompt appears only in this case.
  - [ ] **[macOS]** With the firewall on, the "accept incoming network connections" prompt appears only in this case (as the binary is unsigned, it may reappear after replacing the binary).
- [ ] Nothing secret (token, passphrase, key) appears in output with `-v`.

## 8. Classroom deployment

**Load**
- [ ] A config with inline `auth.token` (and one with inline `auth.key`) works without any other file.
- [ ] `course load ~/Downloads/x.toml`: access confirmed, installed, the downloaded file is gone, then the app starts.
- [ ] Loading over an existing configuration asks first; answering N changes nothing. `--yes` skips the question; `--keep` keeps the file.
- [ ] A broken file (e.g. both `token` and `token_file`) is refused with exit code 2 and the existing configuration is untouched.
- [ ] With the network off, `load` warns that access could not be confirmed but still installs.
- [ ] **[Windows]** Drag the `.toml` file onto `course.exe` in Explorer: a console opens, asks to replace (if needed), installs and starts.
- [ ] With the service running: Linux/macOS restart it automatically ("Restarted the course service"); Windows prints the Administrator command.
- [ ] **[Linux, macOS]** A config file with an inline secret and mode `644` gets a "chmod 600" warning at start.

**Bundle** (build each from any one machine with `--base`; test each on its own platform)
- [ ] `go-git-serve bundle course.toml -o course[.exe] --base <release binary>` for this platform: the bundled app starts with no configuration file present.
- [ ] Rename the bundled file (e.g. `course (1).exe`): still the same app (same cache folder, `course version` shows `course`).
- [ ] `course --interval 5m` is refused ("fixed by course's built-in configuration"); `--browser none` and `--listen` work.
- [ ] `course load x.toml`, `course init`, `course --config x.toml` are refused.
- [ ] `course bundle --show` shows the configuration with the token masked.
- [ ] Open the bundled executable in a text editor and search for the token: not found.
- [ ] `course service install`: the service uses the built-in configuration.
- [ ] **[Windows]** Microsoft Defender (and any other installed antivirus) does not quarantine the bundled `.exe`, including after a full scan. SmartScreen asks once per new bundle.
- [ ] **[macOS]** The bundled Apple Silicon binary runs (no "killed" / "damaged" message) after Gatekeeper approval; `codesign --verify --strict course` succeeds. The Intel bundle runs too.

## 9. Exit codes and stopping

- [ ] Ctrl+C stops cleanly ("Stopping"), exit code 0.
- [ ] Config or usage error (e.g. `--browser kiosk`): exit code 2.
- [ ] Runtime error (e.g. clone of a non-existent repo with no cache): exit code 1.
