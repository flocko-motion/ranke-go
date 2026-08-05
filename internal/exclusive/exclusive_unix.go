//go:build unix

// package: internal/exclusive / lock_unix
// type:    tool
// job:     flock(2) as the lock primitive, where the kernel releases it when the holder exits
// limits:  the primitive only; the lock's contract is exclusive.go's
package exclusive

import (
	"os"

	"golang.org/x/sys/unix"
)

// The kernel drops a flock when the file descriptor closes, including on a process
// that died, so a crashed test leaves nothing stale behind.
func lockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX)
}

func tryLockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
