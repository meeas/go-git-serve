package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// upstream is a local repository standing in for the remote.
type upstream struct {
	t   *testing.T
	dir string
	r   *git.Repository
}

func newUpstream(t *testing.T) *upstream {
	t.Helper()
	dir := t.TempDir()
	r, err := git.PlainInitWithOptions(dir, &git.PlainInitOptions{
		InitOptions: git.InitOptions{DefaultBranch: plumbing.NewBranchReferenceName("main")},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &upstream{t: t, dir: dir, r: r}
}

func (u *upstream) commit(files map[string]string, remove ...string) {
	u.t.Helper()
	w, err := u.r.Worktree()
	if err != nil {
		u.t.Fatal(err)
	}
	for name, body := range files {
		p := filepath.Join(u.dir, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			u.t.Fatal(err)
		}
		if _, err := w.Add(name); err != nil {
			u.t.Fatal(err)
		}
	}
	for _, name := range remove {
		if _, err := w.Remove(name); err != nil {
			u.t.Fatal(err)
		}
	}
	_, err = w.Commit("c", &git.CommitOptions{Author: &object.Signature{Name: "t", Email: "t@example.com", When: time.Now()}})
	if err != nil {
		u.t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return "<missing>"
	}
	return string(b)
}

func TestSyncCloneUpdateAndReclone(t *testing.T) {
	ctx := context.Background()
	up := newUpstream(t)
	up.commit(map[string]string{"index.html": "v1", "old.txt": "x"})

	dir := filepath.Join(t.TempDir(), "cache", "repo")
	s := &Syncer{URL: up.dir, Dir: dir}
	if s.Exists() {
		t.Fatal("Exists before clone")
	}

	res, err := s.Sync(ctx)
	if err != nil || !res.Cloned || res.Branch != "main" {
		t.Fatalf("clone: %+v %v", res, err)
	}
	if got := read(t, filepath.Join(dir, "index.html")); got != "v1" {
		t.Fatalf("index = %q", got)
	}

	res, err = s.Sync(ctx)
	if err != nil || res.Changed {
		t.Fatalf("no-op sync: %+v %v", res, err)
	}

	up.commit(map[string]string{"index.html": "v2"}, "old.txt")
	res, err = s.Update(ctx)
	if err != nil || !res.Changed {
		t.Fatalf("update: %+v %v", res, err)
	}
	if got := read(t, filepath.Join(dir, "index.html")); got != "v2" {
		t.Errorf("index after update = %q", got)
	}
	if got := read(t, filepath.Join(dir, "old.txt")); got != "<missing>" {
		t.Errorf("deleted file still present: %q", got)
	}

	// Switching branch re-clones.
	w, _ := up.r.Worktree()
	if err := w.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("pages"), Create: true}); err != nil {
		t.Fatal(err)
	}
	up.commit(map[string]string{"index.html": "pages"})
	s.Branch = "pages"
	if _, err := s.Update(ctx); err == nil {
		t.Error("Update should refuse to switch branches in place")
	}
	res, err = s.Sync(ctx)
	if err != nil || !res.Cloned || res.Branch != "pages" {
		t.Fatalf("branch switch: %+v %v", res, err)
	}
	if got := read(t, filepath.Join(dir, "index.html")); got != "pages" {
		t.Errorf("index on pages = %q", got)
	}
}

func TestFailedCloneKeepsCache(t *testing.T) {
	ctx := context.Background()
	up := newUpstream(t)
	up.commit(map[string]string{"index.html": "v1"})
	dir := filepath.Join(t.TempDir(), "repo")
	s := &Syncer{URL: up.dir, Dir: dir}
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	bad := &Syncer{URL: filepath.Join(t.TempDir(), "does-not-exist"), Dir: dir}
	if _, err := bad.Sync(ctx); err == nil {
		t.Fatal("expected clone error")
	}
	if got := read(t, filepath.Join(dir, "index.html")); got != "v1" {
		t.Errorf("cache destroyed by failed re-clone: %q", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(dir))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".clone-") {
			t.Errorf("temporary clone dir left behind: %s", e.Name())
		}
	}
}

func TestRecloneRefusedWhileServed(t *testing.T) {
	ctx := context.Background()
	up := newUpstream(t)
	up.commit(map[string]string{"index.html": "v1"})
	dir := filepath.Join(t.TempDir(), "repo")
	s := &Syncer{URL: up.dir, Dir: dir}
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	serving, err := s.HoldServing(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	// In-place updates are fine while serving...
	if _, err := s.Sync(ctx); err != nil {
		t.Fatalf("update while served: %v", err)
	}
	// ...but replacing the checkout is not.
	other := newUpstream(t)
	other.commit(map[string]string{"index.html": "other"})
	s2 := &Syncer{URL: other.dir, Dir: dir}
	if _, err := s2.Sync(ctx); !errors.Is(err, ErrInUse) {
		t.Fatalf("re-clone while served: %v", err)
	}
	if got := read(t, filepath.Join(dir, "index.html")); got != "v1" {
		t.Fatalf("checkout replaced while served: %q", got)
	}

	serving.Release()
	if _, err := s2.Sync(ctx); err != nil {
		t.Fatalf("re-clone after serving stopped: %v", err)
	}
	if got := read(t, filepath.Join(dir, "index.html")); got != "other" {
		t.Errorf("index = %q", got)
	}
	// The re-clone released its exclusive hold, so serving can resume.
	if l, err := s2.HoldServing(ctx, 0); err != nil {
		t.Errorf("HoldServing after re-clone: %v", err)
	} else {
		l.Release()
	}
}

func TestConcurrentSyncsAreSerialised(t *testing.T) {
	ctx := context.Background()
	up := newUpstream(t)
	up.commit(map[string]string{"index.html": "v1"})
	dir := filepath.Join(t.TempDir(), "repo")
	if _, err := (&Syncer{URL: up.dir, Dir: dir}).Sync(ctx); err != nil {
		t.Fatal(err)
	}
	up.commit(map[string]string{"index.html": "v2"})

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := (&Syncer{URL: up.dir, Dir: dir}).Update(ctx)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent update: %v", err)
		}
	}
	if got := read(t, filepath.Join(dir, "index.html")); got != "v2" {
		t.Errorf("index = %q", got)
	}
}

func TestBusyRepoLockGivesUp(t *testing.T) {
	ctx := context.Background()
	up := newUpstream(t)
	up.commit(map[string]string{"index.html": "v1"})
	dir := filepath.Join(t.TempDir(), "repo")
	s := &Syncer{URL: up.dir, Dir: dir, LockWait: 200 * time.Millisecond}
	held, err := s.lockRepo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	_, err = s.Sync(ctx)
	if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("process %d", os.Getpid())) {
		t.Fatalf("expected busy error naming this process, got %v", err)
	}
}
