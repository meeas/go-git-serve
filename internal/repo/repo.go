// Package repo keeps a local, read-only checkout of the remote repository
// in sync. The checkout is a cache: local changes are discarded on update.
package repo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/meeas/go-git-serve/internal/lock"
	"github.com/meeas/go-git-serve/internal/logx"
)

// DefaultLockWait is how long Sync and Update wait for another copy of the
// app that is updating the same checkout.
const DefaultLockWait = 30 * time.Second

// ErrInUse means a re-clone was needed but another running copy is serving
// the checkout, so it cannot be replaced.
var ErrInUse = errors.New("the cached checkout is being served by another running copy of this app; stop it first")

// Syncer clones or updates URL into Dir.
type Syncer struct {
	URL      string
	Branch   string // empty: the remote's default branch
	Dir      string // must be a directory owned by go-git-serve; it may be deleted and re-cloned
	Auth     transport.AuthMethod
	Progress io.Writer // optional git progress output

	// LockWait bounds the wait for another copy's update (default
	// DefaultLockWait).
	LockWait time.Duration
}

// Two locks live next to the checkout:
//
//   - repo.lock (exclusive) is held for every clone, fetch and reset, so two
//     copies never write to the checkout at the same time.
//   - serve.lock is held shared by every copy that is serving the checkout;
//     a re-clone needs it exclusively, so it never replaces files that a
//     running copy is serving.
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

// HoldServing marks the checkout as being served until the returned lock is
// released, waiting up to wait for a re-clone by another copy to finish.
func (s *Syncer) HoldServing(ctx context.Context, wait time.Duration) (*lock.Lock, error) {
	l, err := lock.Acquire(ctx, s.serveLockPath(), lock.Shared, wait)
	var busy *lock.BusyError
	if errors.As(err, &busy) {
		return nil, fmt.Errorf("another copy of this app is still re-cloning the checkout")
	}
	return l, err
}

// Result describes what Sync did.
type Result struct {
	Cloned  bool
	Changed bool
	Commit  string
	Branch  string
}

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

// Sync clones the repository if needed, otherwise fetches and hard-resets
// to the remote branch. If the configured URL or branch differs from the
// existing checkout, the checkout is replaced.
func (s *Syncer) Sync(ctx context.Context) (Result, error) {
	l, err := s.lockRepo(ctx)
	if err != nil {
		return Result{}, err
	}
	defer l.Release()

	r, err := git.PlainOpen(s.Dir)
	if err == nil {
		reason := s.mismatch(r)
		if reason == "" {
			defer closeRepo(r)
			return s.update(ctx, r)
		}
		closeRepo(r)
		logx.Infof("%s; re-cloning", reason)
		return s.clone(ctx)
	}
	if !errors.Is(err, git.ErrRepositoryNotExists) {
		logx.Warnf("cached checkout at %s is unusable (%v); re-cloning", s.Dir, err)
	}
	return s.clone(ctx)
}

// Update fetches and fast-forwards an existing checkout in place. Unlike
// Sync it never deletes or re-clones, so it is safe while files are served.
func (s *Syncer) Update(ctx context.Context) (Result, error) {
	l, err := s.lockRepo(ctx)
	if err != nil {
		return Result{}, err
	}
	defer l.Release()

	r, err := git.PlainOpen(s.Dir)
	if err != nil {
		return Result{}, fmt.Errorf("opening checkout: %w", err)
	}
	defer closeRepo(r)
	if reason := s.mismatch(r); reason != "" {
		return Result{}, fmt.Errorf("%s; restart to re-clone", reason)
	}
	return s.update(ctx, r)
}

// mismatch returns a reason when the checkout is for a different URL/branch.
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
	return ""
}

// clone clones into a temporary sibling directory and swaps it into place,
// so a failed clone never destroys a working cached copy. It refuses to run
// while another copy is serving the checkout.
func (s *Syncer) clone(ctx context.Context) (Result, error) {
	serving, err := lock.Acquire(ctx, s.serveLockPath(), lock.Exclusive, 0)
	var busy *lock.BusyError
	if errors.As(err, &busy) {
		return Result{}, ErrInUse
	}
	if err != nil {
		return Result{}, err
	}
	defer serving.Release()

	parent := filepath.Dir(s.Dir)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return Result{}, err
	}
	tmp, err := os.MkdirTemp(parent, ".clone-")
	if err != nil {
		return Result{}, err
	}
	defer os.RemoveAll(tmp)

	opts := &git.CloneOptions{
		URL:          s.URL,
		Auth:         s.Auth,
		SingleBranch: true,
		Progress:     s.Progress,
	}
	if s.Branch != "" {
		opts.ReferenceName = plumbing.NewBranchReferenceName(s.Branch)
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

	if err := os.RemoveAll(s.Dir); err != nil {
		return Result{}, fmt.Errorf("removing old checkout: %w", err)
	}
	if err := os.Rename(tmp, s.Dir); err != nil {
		return Result{}, fmt.Errorf("moving checkout into place: %w", err)
	}
	return Result{Cloned: true, Changed: true, Commit: head.Hash().String(), Branch: head.Name().Short()}, nil
}

func (s *Syncer) update(ctx context.Context, r *git.Repository) (Result, error) {
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
	err = r.FetchContext(ctx, &git.FetchOptions{
		RemoteName: "origin",
		RefSpecs:   []gitconfig.RefSpec{spec},
		Auth:       s.Auth,
		Force:      true,
		Progress:   s.Progress,
	})
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
