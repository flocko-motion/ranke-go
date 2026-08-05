//go:build !unix

// package: internal/exclusive / lock_other
// type:    tool
// job:     an atomic-create sentinel where flock is unavailable, with a staleness bound
// limits:  the primitive only; the lock's contract is exclusive.go's
package exclusive

import (
	"errors"
	"os"
	"time"
)

// Exclusion rests on an atomic create: the first process to make the sentinel holds
// the lock. A process that dies leaves it behind, so a wait past staleWait takes it
// anyway and a suite still finishes.
const staleWait = 5 * time.Minute

func lockFile(f *os.File) error {
	sentinel := f.Name() + ".held"
	deadline := time.Now().Add(staleWait)
	for {
		h, err := os.OpenFile(sentinel, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o666)
		if err == nil {
			_ = h.Close()
			return nil
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if time.Now().After(deadline) {
			return nil // the holder is gone or wedged; proceeding beats hanging
		}
		time.Sleep(waitStep)
	}
}

func tryLockFile(f *os.File) error {
	sentinel := f.Name() + ".held"
	h, err := os.OpenFile(sentinel, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o666)
	if err != nil {
		return err
	}
	_ = h.Close()
	return nil
}

func unlockFile(f *os.File) error {
	return os.Remove(f.Name() + ".held")
}
