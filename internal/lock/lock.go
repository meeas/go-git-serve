// Package lock provides inter-process file locks that the operating system
// releases automatically when the holding process exits, however it exits
// (crash, kill, power loss). A leftover lock file is therefore harmless: the
// lock is tied to the open file, not to the file's existence.
//
// Locks use flock(2) on Unix and LockFileEx on Windows. Go opens files as
// non-inheritable, so child processes (such as a launched browser) never
// keep a lock alive after this process exits.
package lock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Mode selects an exclusive or shared lock.
type Mode int

const (
	// Exclusive excludes every other holder.
	Exclusive Mode = iota
	// Shared can be held by many processes at once, but not alongside an
	// Exclusive lock.
	Shared
)

// pollInterval is how often a waiting Acquire retries.
const pollInterval = 100 * time.Millisecond

// BusyError reports that the lock could not be acquired in time.
type BusyError struct {
	Path string
	PID  int // holder's process ID for exclusive locks, 0 if unknown
}

func (e *BusyError) Error() string {
	if e.PID > 0 {
		return fmt.Sprintf("%s is locked by process %d", e.Path, e.PID)
	}
	return fmt.Sprintf("%s is locked by another process", e.Path)
}

// Lock is a held lock. Release it with Release; it is also released when
// the process exits.
type Lock struct {
	f *os.File
}

// Acquire takes the lock at path, waiting up to wait for it (0 means try
// once). An exclusive holder records its process ID in the file, purely so
// that a waiter can report who holds it.
func Acquire(ctx context.Context, path string, mode Mode, wait time.Duration) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err := tryLock(f, mode)
		if err == nil {
			break
		}
		if !errors.Is(err, errWouldBlock) {
			f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, err)
		}
		if !time.Now().Before(deadline) {
			f.Close()
			return nil, &BusyError{Path: path, PID: readPID(path)}
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
	if mode == Exclusive {
		// Best effort: the PID is informational only.
		if err := f.Truncate(0); err == nil {
			_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
		}
	}
	return &Lock{f: f}, nil
}

// Release unlocks. It is safe to call on a nil Lock or more than once.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	err := l.f.Close() // closing the file releases the OS lock
	l.f = nil
	return err
}

func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return pid
}
