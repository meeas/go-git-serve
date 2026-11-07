// Package repo keeps a local, read-only checkout of the remote repository
// in sync. The checkout is a cache: local changes are discarded on update.
//
// By default the checkout is shallow (only the latest commit). Updates fetch
// with depth 1 and hard-reset. If a shallow update fails for a reason other
// than the network or credentials, the checkout is replaced by a fresh
// shallow clone, which costs about the same as the update would have.
package repo

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"

	"github.com/meeas/go-git-serve/internal/lock"
	"github.com/meeas/go-git-serve/internal/logx"
)

// DefaultLockWait is how long Sync and Update wait for another copy of the
// app that is updating the same checkout.
const DefaultLockWait = 30 * time.Second

// compactAfterPacks: every fetch that brings new commits adds a pack file
// and go-git never garbage-collects, so .git only grows. Past this many
// packs the checkout is compacted (shallow: re-cloned; full: repacked).
// A variable so tests can lower it.
var compactAfterPacks = 30

// ErrInUse means the checkout had to be replaced but another running copy
// is serving it, so it was left alone.
var ErrInUse = errors.New("the cached checkout is being served by another running copy of this app; stop it first")

// Syncer clones or updates URL into Dir.
type Syncer struct {
	URL      string
	Branch   string // empty: the remote's default branch
	Dir      string // must be a directory owned by go-git-serve; it may be replaced
	Shallow  bool   // clone and fetch only the latest commit
	Auth     transport.AuthMethod
	Progress io.Writer // optional git progress output

	// LockWait bounds the wait for another copy's update (default
	// DefaultLockWait).
	LockWait time.Duration
}

// Result describes what Sync or Update did.
type Result struct {
	Cloned  bool   // a fresh clone was made
	Changed bool   // the served files changed
	Commit  string // commit now checked out
	Branch  string
	Note    string // why a re-clone happened, if it did
}

// updateHook lets tests make an update fail.
var updateHook func() error

// Exists reports whether a usable checkout is present in Dir.
func (s *Syncer) Exists() bool {
	r, err := git.PlainOpen(s.Dir)
	if err != nil {
		return false
	}
	closeRepo(r)
	return true
}

// Origin returns the remote URL of the existing checkout, or "".
func (s *Syncer) Origin() string {
	r, err := git.PlainOpen(s.Dir)
	if err != nil {
		return ""
	}
	defer closeRepo(r)
	remote, err := r.Remote("origin")
	if err != nil || len(remote.Config().URLs) == 0 {
		return ""
	}
	return remote.Config().URLs[0]
}

// closeRepo releases go-git's open pack files. Windows cannot rename or
// delete a directory while files in it are open, and periodic updates would
// otherwise leak file handles.
func closeRepo(r *git.Repository) {
	if c, ok := r.Storer.(io.Closer); ok {
		_ = c.Close()
	}
}

// Two locks live next to the checkout:
//
//   - repo.lock (exclusive) is held for every clone, fetch and reset, so two
//     copies never write to the checkout at the same time.
//   - serve.lock is held shared by every copy that is serving the checkout;
//     replacing the checkout needs it exclusively, so files are never
//     replaced under another running copy.
func (s *Syncer) repoLockPath() string  { return s.Dir + ".lock" }
func (s *Syncer) serveLockPath() string { return filepath.Join(filepath.Dir(s.Dir), "serve.lock") }

func (s *Syncer) lockRepo(ctx context.Context) (*lock.Lock, error) {
	wait := s.LockWait
	if wait == 0 {
		wait = DefaultLockWait
	}
	l, err := lock.Acquire(ctx, s.repoLockPath(), lock.Exclusive, wait)
	var busy *lock.BusyError
	if errors.As(err, &busy) {
		who := "another copy of this app"
		if busy.PID > 0 {
			who = fmt.Sprintf("another copy of this app (process %d)", busy.PID)
		}
		return nil, fmt.Errorf("%s has been updating the checkout for over %s; try again later", who, wait)
	}
	return l, err
}

// Serving marks the checkout as in use by this process.
type Serving struct {
	s *Syncer
	l *lock.Lock
}

// HoldServing marks the checkout as being served until Release, waiting up
// to wait for a re-clone by another copy to finish.
func (s *Syncer) HoldServing(ctx context.Context, wait time.Duration) (*Serving, error) {
	l, err := lock.Acquire(ctx, s.serveLockPath(), lock.Shared, wait)
	var busy *lock.BusyError
	if errors.As(err, &busy) {
		return nil, fmt.Errorf("another copy of this app is still re-cloning the checkout")
	}
	if err != nil {
		return nil, err
	}
	return &Serving{s: s, l: l}, nil
}

// Release stops marking the checkout as served.
func (h *Serving) Release() {
	if h != nil {
		h.l.Release()
	}
}

// exclusive runs fn while this process is the only one serving the
// checkout. The shared hold is dropped and re-taken around it.
func (h *Serving) exclusive(ctx context.Context, fn func() error) error {
	path := h.s.serveLockPath()
	h.l.Release()
	x, xerr := lock.Acquire(ctx, path, lock.Exclusive, 0)
	var ferr error
	if xerr == nil {
		ferr = fn()
		x.Release()
	}
	l, err := lock.Acquire(ctx, path, lock.Shared, DefaultLockWait)
	if err != nil {
		return fmt.Errorf("re-acquiring serve lock: %w", err)
	}
	h.l = l
	var busy *lock.BusyError
	if errors.As(xerr, &busy) {
		return ErrInUse
	}
	if xerr != nil {
		return xerr
	}
	return ferr
}

// Sync brings the checkout up to date when it is not being served by this
// process (start-up and the `sync` command). It clones if needed, otherwise
// fetches and hard-resets; it re-clones when the URL, branch or clone mode
// changed, when a shallow update fails, or when .git needs compacting.
func (s *Syncer) Sync(ctx context.Context) (Result, error) {
	l, err := s.lockRepo(ctx)
	if err != nil {
		return Result{}, err
	}
	defer l.Release()
	s.removeStale()

	r, err := git.PlainOpen(s.Dir)
	if err != nil {
		if !errors.Is(err, git.ErrRepositoryNotExists) {
			logx.Warnf("cached checkout at %s is unusable (%v); re-cloning", s.Dir, err)
		}
		return s.clone(ctx, "")
	}
	if reason := s.mismatch(r); reason != "" {
		closeRepo(r)
		return s.clone(ctx, reason)
	}
	res, err := s.update(ctx, r)
	closeRepo(r)
	if err != nil {
		if !s.Shallow || !recoverable(err) {
			return Result{}, err
		}
		logx.Warnf("shallow update failed (%v); re-cloning", err)
		res, cerr := s.clone(ctx, "shallow update failed")
		if cerr != nil {
			return Result{}, fmt.Errorf("%w; re-clone also failed: %v", err, cerr)
		}
		return res, nil
	}
	if s.needsCompaction() {
		if s.Shallow {
			cres, cerr := s.clone(ctx, "compacting the checkout")
			if cerr == nil {
				return cres, nil
			}
			logx.Debugf("compaction skipped: %v", cerr)
		} else {
			s.repack()
		}
	}
	return res, nil
}

// Update brings the checkout up to date while this process is serving it
// (periodic updates). It never changes URL or branch. When the checkout has
// to be replaced (failed shallow update, compaction), the new checkout is
// moved into place and swap is called so the caller can switch to serving
// it before the old one is deleted.
func (s *Syncer) Update(ctx context.Context, h *Serving, swap func() error) (Result, error) {
	l, err := s.lockRepo(ctx)
	if err != nil {
		return Result{}, err
	}
	defer l.Release()
	s.removeStale()

	r, err := git.PlainOpen(s.Dir)
	if err != nil {
		return Result{}, fmt.Errorf("opening checkout: %w", err)
	}
	if reason := s.mismatch(r); reason != "" {
		closeRepo(r)
		return Result{}, fmt.Errorf("%s; restart to re-clone", reason)
	}
	res, err := s.update(ctx, r)
	closeRepo(r)
	if err != nil {
		if !s.Shallow || !recoverable(err) {
			return Result{}, err
		}
		logx.Warnf("shallow update failed (%v); re-cloning", err)
		res, rerr := s.replace(ctx, h, swap, "shallow update failed")
		if rerr != nil {
			return Result{}, fmt.Errorf("%w; re-clone also failed: %v", err, rerr)
		}
		return res, nil
	}
	if s.needsCompaction() {
		if s.Shallow {
			cres, cerr := s.replace(ctx, h, swap, "compacting the checkout")
			if cerr == nil {
				return cres, nil
			}
			logx.Debugf("compaction skipped: %v", cerr)
		} else {
			s.repack()
		}
	}
	return res, nil
}

// mismatch returns a reason when the checkout does not match the settings.
func (s *Syncer) mismatch(r *git.Repository) string {
	remote, err := r.Remote("origin")
	if err != nil || len(remote.Config().URLs) == 0 {
		return "cached checkout has no origin remote"
	}
	if got := remote.Config().URLs[0]; got != s.URL {
		return fmt.Sprintf("repository URL changed (was %s)", got)
	}
	if s.Branch != "" {
		head, err := r.Head()
		if err != nil || head.Name() != plumbing.NewBranchReferenceName(s.Branch) {
			return fmt.Sprintf("branch changed to %s", s.Branch)
		}
	}
	// A shallow checkout cannot serve as a full one. (The reverse is fine:
	// depth-1 fetches work in a full checkout, and compaction later makes
	// it shallow. A shallow clone of a one-commit repo looks "full", so
	// only this direction can be detected reliably.)
	if !s.Shallow {
		if hs, err := r.Storer.Shallow(); err == nil && len(hs) > 0 {
			return "switching to a full clone"
		}
	}
	return ""
}

// clone replaces the checkout with a fresh clone, refusing while another
// copy is serving it.
func (s *Syncer) clone(ctx context.Context, reason string) (Result, error) {
	serving, err := lock.Acquire(ctx, s.serveLockPath(), lock.Exclusive, 0)
	var busy *lock.BusyError
	if errors.As(err, &busy) {
		return Result{}, ErrInUse
	}
	if err != nil {
		return Result{}, err
	}
	defer serving.Release()
	return s.cloneAndSwap(ctx, reason, nil)
}

// replace is clone for the process that is serving the checkout.
func (s *Syncer) replace(ctx context.Context, h *Serving, swap func() error, reason string) (Result, error) {
	var res Result
	err := h.exclusive(ctx, func() error {
		var err error
		res, err = s.cloneAndSwap(ctx, reason, swap)
		return err
	})
	return res, err
}

// cloneAndSwap clones into a temporary sibling directory, moves it into
// place (the old checkout is renamed aside, never deleted first), calls
// swap, then deletes the old checkout. A failed clone leaves the existing
// checkout untouched.
func (s *Syncer) cloneAndSwap(ctx context.Context, reason string, swap func() error) (Result, error) {
	if reason != "" {
		logx.Infof("Re-cloning: %s", reason)
	}
	parent := filepath.Dir(s.Dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Result{}, err
	}
	tmp, err := os.MkdirTemp(parent, ".clone-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp) // no-op once moved into place

	opts := &git.CloneOptions{
		URL:          s.URL,
		Auth:         s.Auth,
		SingleBranch: true,
		Progress:     s.Progress,
	}
	if s.Branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(s.Branch)
	}
	if s.Shallow {
		opts.Depth = 1
	}
	logx.Infof("Cloning %s", s.URL)
	r, err := git.PlainCloneContext(ctx, tmp, false, opts)
	if err != nil {
		return Result{}, fmt.Errorf("clone %s: %w", s.URL, err)
	}
	head, err := r.Head()
	closeRepo(r)
	if err != nil {
		return Result{}, fmt.Errorf("clone %s: %w", s.URL, err)
	}

	old := ""
	if _, err := os.Stat(s.Dir); err == nil {
		old = fmt.Sprintf("%s.old-%s", s.Dir, randomSuffix())
		if err := os.Rename(s.Dir, old); err != nil {
			return Result{}, fmt.Errorf("moving old checkout aside: %w", err)
		}
	}
	if err := os.Rename(tmp, s.Dir); err != nil {
		if old != "" {
			_ = os.Rename(old, s.Dir)
		}
		return Result{}, fmt.Errorf("moving checkout into place: %w", err)
	}
	res := Result{Cloned: true, Changed: true, Commit: head.Hash().String(), Branch: head.Name().Short(), Note: reason}
	if swap != nil {
		if err := swap(); err != nil {
			// Still serving the old files from their new location; keep them.
			return res, fmt.Errorf("switching to the new checkout: %w", err)
		}
	}
	if old != "" {
		if err := os.RemoveAll(old); err != nil {
			logx.Debugf("old checkout %s not removed yet: %v", old, err)
		}
	}
	return res, nil
}

// removeStale deletes old checkouts and temporary clones left behind by a
// crash or by files that were still open (Windows). Called with repo.lock
// held, so no other copy is using them.
func (s *Syncer) removeStale() {
	for _, pattern := range []string{s.Dir + ".old-*", filepath.Join(filepath.Dir(s.Dir), ".clone-*")} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			if err := os.RemoveAll(m); err != nil {
				logx.Debugf("cannot remove %s yet: %v", m, err)
			}
		}
	}
}

func (s *Syncer) update(ctx context.Context, r *git.Repository) (Result, error) {
	if updateHook != nil {
		if err := updateHook(); err != nil {
			return Result{}, err
		}
	}
	head, err := r.Head()
	if err != nil {
		return Result{}, fmt.Errorf("reading checkout HEAD: %w", err)
	}
	if !head.Name().IsBranch() {
		return Result{}, fmt.Errorf("checkout at %s is not on a branch", s.Dir)
	}
	branch := head.Name().Short()

	// Fetch the checked-out branch explicitly: a single-branch clone of the
	// default branch only records "+HEAD:refs/remotes/origin/HEAD".
	spec := gitconfig.RefSpec(fmt.Sprintf("+refs/heads/%s:refs/remotes/origin/%s", branch, branch))
	opts := &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []gitconfig.RefSpec{spec},
		Auth:       s.Auth,
		Force:      true,
		Progress:   s.Progress,
	}
	if s.Shallow {
		opts.Depth = 1
	}
	err = r.FetchContext(ctx, opts)
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return Result{}, fmt.Errorf("fetch %s: %w", s.URL, err)
	}

	remoteRef, err := r.Reference(plumbing.NewRemoteReferenceName("origin", branch), true)
	if err != nil {
		return Result{}, fmt.Errorf("branch %s not found on origin: %w", branch, err)
	}
	res := Result{Commit: remoteRef.Hash().String(), Branch: branch}

	if remoteRef.Hash() == head.Hash() {
		return res, nil
	}
	w, err := r.Worktree()
	if err != nil {
		return Result{}, err
	}
	if err := w.Reset(&git.ResetOptions{Commit: remoteRef.Hash(), Mode: git.HardReset}); err != nil {
		return Result{}, fmt.Errorf("updating checkout: %w", err)
	}
	res.Changed = true
	return res, nil
}

// recoverable reports whether a failed update might be fixed by a fresh
// clone: not for cancellation, network, credential or server errors, which
// a re-clone would hit as well.
func recoverable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	for _, e := range []error{
		transport.ErrAuthenticationRequired, transport.ErrAuthorizationFailed,
		transport.ErrRepositoryNotFound, transport.ErrEmptyRemoteRepository,
		plumbing.ErrReferenceNotFound,
	} {
		if errors.Is(err, e) {
			return false
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return false
	}
	var httpErr *githttp.Err
	if errors.As(err, &httpErr) {
		return false
	}
	msg := err.Error()
	for _, s := range []string{"ssh: handshake failed", "unable to authenticate", "connection refused", "no such host"} {
		if strings.Contains(msg, s) {
			return false
		}
	}
	return true
}

// needsCompaction reports whether .git has accumulated enough pack files to
// be worth compacting.
func (s *Syncer) needsCompaction() bool {
	entries, err := os.ReadDir(filepath.Join(s.Dir, ".git", "objects", "pack"))
	if err != nil {
		return false
	}
	packs := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".pack") {
			packs++
		}
	}
	return packs > compactAfterPacks
}

// repack compacts a full (non-shallow) checkout in place. Not used for
// shallow checkouts: go-git's repack walks history, which a shallow
// checkout does not have.
func (s *Syncer) repack() {
	r, err := git.PlainOpen(s.Dir)
	if err != nil {
		return
	}
	defer closeRepo(r)
	if err := r.RepackObjects(&git.RepackConfig{}); err != nil {
		logx.Warnf("compacting the checkout failed: %v", err)
		return
	}
	if err := r.Prune(git.PruneOptions{Handler: r.DeleteObject}); err != nil {
		logx.Debugf("pruning loose objects: %v", err)
	}
	logx.Debugf("compacted the checkout")
}

func randomSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
