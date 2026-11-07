package auth

import (
	"os"
	"runtime"

	"github.com/meeas/go-git-serve/internal/logx"
)

// WarnIfExposed warns when a secret file is readable by other users, as
// OpenSSH does. Windows uses ACLs rather than mode bits, so it is skipped.
func WarnIfExposed(path string) {
	if runtime.GOOS == "windows" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	if fi.Mode().Perm()&0o077 != 0 {
		logx.Warnf("%s is accessible by other users (mode %04o); run: chmod 600 %s", path, fi.Mode().Perm(), path)
	}
}
