//go:build windows

package lock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

var errWouldBlock = errors.New("lock held by another process")

// lockOffsetHigh places the locked byte at 4 GiB, far beyond the PID text at
// the start of the file. Windows locks are mandatory, so locking byte 0
// would stop waiters from reading the holder's PID.
const lockOffsetHigh = 1

func tryLock(f *os.File, mode Mode) error {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if mode == Exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	ol := &windows.Overlapped{OffsetHigh: lockOffsetHigh}
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, ol)
	switch {
	case err == nil:
		return nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return errWouldBlock
	default:
		return err
	}
}
