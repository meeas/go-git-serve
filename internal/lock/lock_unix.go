//go:build darwin || linux || freebsd || openbsd || netbsd || dragonfly

package lock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

var errWouldBlock = errors.New("lock held by another process")

func tryLock(f *os.File, mode Mode) error {
	how := unix.LOCK_EX
	if mode == Shared {
		how = unix.LOCK_SH
	}
	for {
		err := unix.Flock(int(f.Fd()), how|unix.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EWOULDBLOCK):
			return errWouldBlock
		default:
			return err
		}
	}
}
