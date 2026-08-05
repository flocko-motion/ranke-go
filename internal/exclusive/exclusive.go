// package: internal/exclusive / lock
// type:    tool
// job:     a cross-process lock, so tests sharing one live service serialise without `go test -p 1`
// limits:  test support; it orders access and wipes nothing itself
//
// `go test ./...` runs each package in its own process, so a sync.Mutex never sees the
// other one. Every test opening the shared neo4j flushes it at the start, which is why
// two packages running at once wiped each other's graph. A file lock is what one
// process can hold against another.
package exclusive

import (
	"os"
	"path/filepath"
	"time"
)

// Neo4j names the lock every test touching the shared neo4j takes. Keyed by the
// service rather than by the caller, so unrelated services stay parallel.
const Neo4j = "ranke-neo4j"

// Lock blocks until no other process holds name, and returns the release. It never
// fails: a lock that cannot be taken would otherwise turn a working test red, and the
// worst it costs is the interference this exists to prevent.
func Lock(name string) (release func()) {
	path := filepath.Join(os.TempDir(), name+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return func() {}
	}
	if err := lockFile(f); err != nil {
		_ = f.Close()
		return func() {}
	}
	return func() {
		_ = unlockFile(f)
		_ = f.Close()
	}
}

// Held reports whether another process holds name, without waiting. A test asserting
// the serialisation uses this; nothing in a normal run needs it.
func Held(name string) bool {
	path := filepath.Join(os.TempDir(), name+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	if err := tryLockFile(f); err != nil {
		return true
	}
	_ = unlockFile(f)
	return false
}

// waitStep is how long a fallback implementation sleeps between attempts.
const waitStep = 20 * time.Millisecond
