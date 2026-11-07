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

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/meeas/go-git-serve/internal/logx"
)

// Syncer clones or updates URL into Dir.
type Syncer struct {
	URL      string
	Branch   string // empty: the remote's default branch
	Dir      string // must be a directory owned by go-git-serve; it may be deleted and re-cloned
	Auth     transport.AuthMethod
	Progress io.Writer // optional git progress output
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
// so a failed clone never destroys a working cached copy.
func (s *Syncer) clone(ctx context.Context) (Result, error) {
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
