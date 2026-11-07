package repo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
)

// upstream is a local repository standing in for the remote. go-git serves
// local paths by running the real git-upload-pack, as GitHub does.
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

// update runs Update the way the serving process does.
func update(t *testing.T, s *Syncer, swap func() error) (Result, error) {
	t.Helper()
	h, err := s.HoldServing(context.Background(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Release()
	if swap == nil {
		swap = func() error { return nil }
	}
	return s.Update(context.Background(), h, swap)
}

func isShallow(t *testing.T, dir string) bool {
	t.Helper()
	r, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRepo(r)
	hs, _ := r.Storer.Shallow()
	return len(hs) > 0
}

func commitCount(t *testing.T, dir string) int {
	t.Helper()
	r, err := git.PlainOpen(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer closeRepo(r)
	iter, err := r.CommitObjects()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	iter.ForEach(func(*object.Commit) error { n++; return nil })
	return n
}

func TestSyncCloneUpdateAndReclone(t *testing.T) {
	for _, shallow := range []bool{true, false} {
		t.Run(fmt.Sprintf("shallow=%v", shallow), func(t *testing.T) {
			ctx := context.Background()
			up := newUpstream(t)
			up.commit(map[string]string{"index.html": "v0"})
			up.commit(map[string]string{"index.html": "v1", "old.txt": "x"})

			dir := filepath.Join(t.TempDir(), "cache", "repo")
			s := &Syncer{URL: up.dir, Dir: dir, Shallow: shallow}
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
			if got := isShallow(t, dir); got != shallow {
				t.Errorf("checkout shallow = %v, want %v", got, shallow)
			}

			res, err = s.Sync(ctx)
			if err != nil || res.Changed {
				t.Fatalf("no-op sync: %+v %v", res, err)
			}

			up.commit(map[string]string{"index.html": "v2"}, "old.txt")
			res, err = update(t, s, nil)
			if err != nil || !res.Changed || res.Cloned {
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
			if _, err := update(t, s, nil); err == nil {
				t.Error("Update should refuse to switch branches in place")
			}
			res, err = s.Sync(ctx)
			if err != nil || !res.Cloned || res.Branch != "pages" {
				t.Fatalf("branch switch: %+v %v", res, err)
			}
			if got := read(t, filepath.Join(dir, "index.html")); got != "pages" {
				t.Errorf("index on pages = %q", got)
			}
		})
	}
}

// gitCLI runs the git command line in dir (for history operations go-git
// cannot do: merges, rewinds, amends).
func gitCLI(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func cliCommit(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	gitCLI(t, dir, "add", "-A")
	gitCLI(t, dir, "commit", "-qm", content)
}

// TestShallowUpdateScenarios covers history shapes reported to break
// go-git shallow fetches (e.g. go-git issues #305 and #1443). Every update
// must succeed in place (no fallback re-clone) and match the remote.
func TestShallowUpdateScenarios(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git command line not available")
	}
	up := t.TempDir()
	gitCLI(t, up, "init", "-q", "-b", "main")
	for i := 0; i < 20; i++ {
		cliCommit(t, up, fmt.Sprintf("base-%d", i))
	}
	dir := filepath.Join(t.TempDir(), "repo")
	s := &Syncer{URL: up, Dir: dir, Shallow: true}
	if _, err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := commitCount(t, dir); n != 1 {
		t.Errorf("shallow clone has %d commits, want 1", n)
	}

	steps := []struct {
		name string
		do   func()
	}{
		{"fast-forward 1 commit", func() { cliCommit(t, up, "ff1") }},
		{"fast-forward 5 commits", func() {
			for i := 0; i < 5; i++ {
				cliCommit(t, up, fmt.Sprintf("ff5-%d", i))
			}
		}},
		{"no change", func() {}},
		{"merge commit", func() {
			gitCLI(t, up, "checkout", "-q", "-b", "feat")
			cliCommit(t, up, "feat-1")
			os.WriteFile(filepath.Join(up, "other.txt"), []byte("x"), 0o644)
			gitCLI(t, up, "add", "-A")
			gitCLI(t, up, "commit", "-qm", "feat-2")
			gitCLI(t, up, "checkout", "-q", "main")
			cliCommit(t, up, "main-parallel")
			gitCLI(t, up, "merge", "-q", "--no-ff", "-X", "theirs", "-m", "merge", "feat")
		}},
		{"force-push rewind to an older commit (#1443)", func() { gitCLI(t, up, "reset", "-q", "--hard", "HEAD~7") }},
		{"fast-forward after rewind", func() { cliCommit(t, up, "after-rewind") }},
		{"rewritten history (amend)", func() { gitCLI(t, up, "commit", "-q", "--amend", "-m", "amended") }},
		{"history replaced by an unrelated root", func() {
			gitCLI(t, up, "checkout", "-q", "--orphan", "tmp")
			cliCommit(t, up, "orphan-root")
			gitCLI(t, up, "branch", "-q", "-D", "main")
			gitCLI(t, up, "branch", "-q", "-m", "main")
		}},
	}
	for i := 0; i < 10; i++ {
		n := i
		steps = append(steps, struct {
			name string
			do   func()
		}{fmt.Sprintf("sequential update %d", n), func() { cliCommit(t, up, fmt.Sprintf("seq-%d", n)) }})
	}

	for _, st := range steps {
		st.do()
		res, err := update(t, s, nil)
		if err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		if res.Cloned {
			t.Errorf("%s: needed a fallback re-clone (%s)", st.name, res.Note)
		}
		if got, want := read(t, filepath.Join(dir, "index.html")), read(t, filepath.Join(up, "index.html")); got != want {
			t.Errorf("%s: index = %q, want %q", st.name, got, want)
		}
	}
}

func TestShallowFallbackReclone(t *testing.T) {
	ctx := context.Background()
	up := newUpstream(t)
	up.commit(map[string]string{"index.html": "v1"})
	dir := filepath.Join(t.TempDir(), "repo")
	s := &Syncer{URL: up.dir, Dir: dir, Shallow: true}
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() { updateHook = nil }()

	// Start-up path: a broken update leads to a fresh clone.
	updateHook = func() error { return errors.New("object not found") }
	up.commit(map[string]string{"index.html": "v2"})
	res, err := s.Sync(ctx)
	if err != nil || !res.Cloned || res.Note == "" {
		t.Fatalf("Sync fallback: %+v %v", res, err)
	}
	if got := read(t, filepath.Join(dir, "index.html")); got != "v2" {
		t.Errorf("index = %q", got)
	}

	// Serving path: the new checkout is swapped in and the old one removed.
	up.commit(map[string]string{"index.html": "v3"})
	swapped := false
	res, err = update(t, s, func() error {
		swapped = true
		if got := read(t, filepath.Join(dir, "index.html")); got != "v3" {
			t.Errorf("swap called before the new checkout was in place: %q", got)
		}
		return nil
	})
	if err != nil || !res.Cloned || !swapped {
		t.Fatalf("Update fallback: %+v %v swapped=%v", res, err, swapped)
	}
	if old, _ := filepath.Glob(dir + ".old-*"); len(old) > 0 {
		t.Errorf("old checkout left behind: %v", old)
	}

	// Not while another copy is also serving the checkout.
	other, err := s.HoldServing(ctx, 0)
	if err != nil {
		t.Fatal(err)
	}
	up.commit(map[string]string{"index.html": "v4"})
	_, err = update(t, s, nil)
	other.Release()
	if !errors.Is(err, ErrInUse) && (err == nil || !strings.Contains(err.Error(), ErrInUse.Error())) {
		t.Fatalf("fallback with another copy serving: %v", err)
	}
	if got := read(t, filepath.Join(dir, "index.html")); got != "v3" {
		t.Errorf("checkout replaced under another copy: %q", got)
	}

	// No re-clone for errors a re-clone would hit too.
	updateHook = func() error { return fmt.Errorf("fetch: %w", transport.ErrAuthenticationRequired) }
	res, err = s.Sync(ctx)
	if err == nil || res.Cloned {
		t.Fatalf("auth error should not re-clone: %+v %v", res, err)
	}
	updateHook = nil

	// Full-clone mode does not fall back.
	full := &Syncer{URL: up.dir, Dir: filepath.Join(t.TempDir(), "full")}
	if _, err := full.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	updateHook = func() error { return errors.New("object not found") }
	if res, err := full.Sync(ctx); err == nil || res.Cloned {
		t.Fatalf("full mode should report the error: %+v %v", res, err)
	}
}

func TestCompaction(t *testing.T) {
	old := compactAfterPacks
	compactAfterPacks = 3
	defer func() { compactAfterPacks = old }()

	packs := func(dir string) int {
		m, _ := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
		return len(m)
	}
	for _, shallow := range []bool{true, false} {
		t.Run(fmt.Sprintf("shallow=%v", shallow), func(t *testing.T) {
			up := newUpstream(t)
			up.commit(map[string]string{"index.html": "v0"})
			dir := filepath.Join(t.TempDir(), "repo")
			s := &Syncer{URL: up.dir, Dir: dir, Shallow: shallow}
			if _, err := s.Sync(context.Background()); err != nil {
				t.Fatal(err)
			}
			compacted := false
			for i := 1; i <= 6; i++ {
				up.commit(map[string]string{"index.html": fmt.Sprintf("v%d", i)})
				res, err := update(t, s, nil)
				if err != nil {
					t.Fatalf("update %d: %v", i, err)
				}
				if res.Cloned {
					compacted = true
				}
				if got := read(t, filepath.Join(dir, "index.html")); got != fmt.Sprintf("v%d", i) {
					t.Fatalf("update %d: index = %q", i, got)
				}
				if n := packs(dir); n > compactAfterPacks+1 {
					t.Fatalf("update %d: %d packs, compaction did not run", i, n)
				}
			}
			if shallow && !compacted {
				t.Error("shallow checkout was never re-cloned to compact it")
			}
			if !shallow && commitCount(t, dir) != 7 {
				t.Errorf("full checkout lost history after repack: %d commits", commitCount(t, dir))
			}
		})
	}
}

func TestSwitchShallowToFull(t *testing.T) {
	ctx := context.Background()
	up := newUpstream(t)
	for i := 0; i < 3; i++ {
		up.commit(map[string]string{"index.html": fmt.Sprintf("v%d", i)})
	}
	dir := filepath.Join(t.TempDir(), "repo")
	if _, err := (&Syncer{URL: up.dir, Dir: dir, Shallow: true}).Sync(ctx); err != nil {
		t.Fatal(err)
	}
	res, err := (&Syncer{URL: up.dir, Dir: dir, Shallow: false}).Sync(ctx)
	if err != nil || !res.Cloned {
		t.Fatalf("switch to full: %+v %v", res, err)
	}
	if isShallow(t, dir) || commitCount(t, dir) != 3 {
		t.Errorf("not a full clone: shallow=%v commits=%d", isShallow(t, dir), commitCount(t, dir))
	}
	// Full -> shallow keeps the checkout (depth-1 fetches work in it).
	res, err = (&Syncer{URL: up.dir, Dir: dir, Shallow: true}).Sync(ctx)
	if err != nil || res.Cloned {
		t.Fatalf("switch to shallow should not re-clone: %+v %v", res, err)
	}
}

func TestFailedCloneKeepsCache(t *testing.T) {
	ctx := context.Background()
	up := newUpstream(t)
	up.commit(map[string]string{"index.html": "v1"})
	dir := filepath.Join(t.TempDir(), "repo")
	s := &Syncer{URL: up.dir, Dir: dir, Shallow: true}
	if _, err := s.Sync(ctx); err != nil {
		t.Fatal(err)
	}

	bad := &Syncer{URL: filepath.Join(t.TempDir(), "does-not-exist"), Dir: dir, Shallow: true}
	if _, err := bad.Sync(ctx); err == nil {
		t.Fatal("expected clone error")
	}
	if got := read(t, filepath.Join(dir, "index.html")); got != "v1" {
		t.Errorf("cache destroyed by failed re-clone: %q", got)
	}
	entries, _ := os.ReadDir(filepath.Dir(dir))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".clone-") || strings.Contains(e.Name(), ".old-") {
			t.Errorf("temporary dir left behind: %s", e.Name())
		}
	}
}

func TestStaleDirsRemoved(t *testing.T) {
	up := newUpstream(t)
	up.commit(map[string]string{"index.html": "v1"})
	dir := filepath.Join(t.TempDir(), "repo")
	s := &Syncer{URL: up.dir, Dir: dir, Shallow: true}
	if _, err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	// As left by a crash mid-swap, or an old checkout Windows could not delete.
	os.MkdirAll(dir+".old-deadbeef", 0o700)
	os.MkdirAll(filepath.Join(filepath.Dir(dir), ".clone-123"), 0o700)
	if _, err := s.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m, _ := filepath.Glob(dir + ".old-*"); len(m) > 0 {
		t.Errorf("stale old checkout not removed: %v", m)
	}
	if m, _ := filepath.Glob(filepath.Join(filepath.Dir(dir), ".clone-*")); len(m) > 0 {
		t.Errorf("stale temp clone not removed: %v", m)
	}
}

func TestRecloneRefusedWhileServed(t *testing.T) {
	ctx := context.Background()
	up := newUpstream(t)
	up.commit(map[string]string{"index.html": "v1"})
	dir := filepath.Join(t.TempDir(), "repo")
	s := &Syncer{URL: up.dir, Dir: dir, Shallow: true}
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
	s2 := &Syncer{URL: other.dir, Dir: dir, Shallow: true}
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

func TestConcurrentUpdatesAreSerialised(t *testing.T) {
	ctx := context.Background()
	up := newUpstream(t)
	up.commit(map[string]string{"index.html": "v1"})
	dir := filepath.Join(t.TempDir(), "repo")
	if _, err := (&Syncer{URL: up.dir, Dir: dir, Shallow: true}).Sync(ctx); err != nil {
		t.Fatal(err)
	}
	up.commit(map[string]string{"index.html": "v2"})

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s := &Syncer{URL: up.dir, Dir: dir, Shallow: true}
			h, err := s.HoldServing(ctx, 5*time.Second)
			if err != nil {
				errs <- err
				return
			}
			defer h.Release()
			_, err = s.Update(ctx, h, func() error { return nil })
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
