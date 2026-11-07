package lock

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExclusiveBusyReportsPID(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "x.lock")
	l1, err := Acquire(ctx, path, Exclusive, 0)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err = Acquire(ctx, path, Exclusive, 300*time.Millisecond)
	var busy *BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("second exclusive lock: %v", err)
	}
	if busy.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", busy.PID, os.Getpid())
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Errorf("did not wait before giving up")
	}

	// Waiting succeeds once the holder releases.
	go func() { time.Sleep(200 * time.Millisecond); l1.Release() }()
	l2, err := Acquire(ctx, path, Exclusive, 5*time.Second)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	l2.Release()
	l2.Release() // idempotent
}

func TestSharedExcludesExclusive(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "s.lock")
	s1, err := Acquire(ctx, path, Shared, 0)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := Acquire(ctx, path, Shared, 0)
	if err != nil {
		t.Fatalf("two shared holders should coexist: %v", err)
	}
	if _, err := Acquire(ctx, path, Exclusive, 0); err == nil {
		t.Fatal("exclusive acquired while shared locks held")
	}
	s1.Release()
	s2.Release()
	x, err := Acquire(ctx, path, Exclusive, 0)
	if err != nil {
		t.Fatalf("exclusive after shared released: %v", err)
	}
	if _, err := Acquire(ctx, path, Shared, 0); err == nil {
		t.Fatal("shared acquired while exclusive held")
	}
	x.Release()
}

// TestCrashReleasesLockDespiteChild simulates a crash: a helper process takes
// the lock, starts a long-running child (standing in for the browser), and
// exits without unlocking. The lock must be free while the child still runs.
func TestCrashReleasesLockDespiteChild(t *testing.T) {
	if os.Getenv("GGS_LOCK_HELPER") != "" {
		t.Skip("helper mode")
	}
	path := filepath.Join(t.TempDir(), "crash.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "GGS_LOCK_HELPER=holder", "GGS_LOCK_PATH="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var childPID int
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), "child="); ok {
			childPID, _ = strconv.Atoi(v)
		}
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("helper should exit non-zero (simulated crash)")
	}
	if childPID == 0 {
		t.Fatal("helper did not report its child")
	}
	child, _ := os.FindProcess(childPID)
	defer child.Kill()

	l, err := Acquire(context.Background(), path, Exclusive, 2*time.Second)
	if err != nil {
		t.Fatalf("lock still held after holder crashed (child alive): %v", err)
	}
	l.Release()
}

// TestHelperProcess is run as a subprocess by the tests above.
func TestHelperProcess(t *testing.T) {
	switch os.Getenv("GGS_LOCK_HELPER") {
	case "holder":
		if _, err := Acquire(context.Background(), os.Getenv("GGS_LOCK_PATH"), Exclusive, 0); err != nil {
			fmt.Println("error:", err)
			os.Exit(3)
		}
		child := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		child.Env = append(os.Environ(), "GGS_LOCK_HELPER=sleeper")
		if err := child.Start(); err != nil {
			fmt.Println("error:", err)
			os.Exit(3)
		}
		fmt.Printf("child=%d\n", child.Process.Pid)
		os.Exit(2) // "crash" without releasing
	case "sleeper":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	}
}
