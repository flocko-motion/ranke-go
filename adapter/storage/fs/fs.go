// package: fs / persistence
// type:    adapter
// job:     stores claims, content blobs and bookmark records as files in a single flat directory
// limits:  no indexing or codec logic; a BlobStore behind storage.NewBlobUniverse (-> adapter)
//
// Package fs names claims and blobs by their id strings in one flat directory.
// Open (storage.Streamer) streams large content straight off disk.
package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage"
)

var (
	errEmptyDir = errors.New("adapter/fs.New: empty directory path")
	errIO       = errors.New("adapter/fs: io")
)

// New returns a Universe backed by a single flat directory. Claims and
// content blobs share the namespace, addressed by their id strings as
// filenames.
func New(dir string) (ranke.Universe, error) {
	s, err := open(dir)
	if err != nil {
		return nil, err
	}
	return storage.NewBlobUniverse(s), nil
}

func open(dir string) (*store, error) {
	if dir == "" {
		return nil, errEmptyDir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("%w: mkdir %s: %w", errIO, dir, err)
	}
	return &store{dir: dir, caps: detectCaps(dir)}, nil
}

type store struct {
	dir  string
	caps ranke.Capabilities
}

// detectCaps probes real access once at startup: a read-only mount can still show
// writable permission bits, so it attempts a create+remove rather than os.Stat.
func detectCaps(dir string) ranke.Capabilities {
	caps := ranke.Capabilities{Persistent: true}
	if _, err := os.ReadDir(dir); err == nil {
		caps.Enumerate = true
	}
	if f, err := os.CreateTemp(dir, ".ranke-probe-*"); err == nil {
		name := f.Name()
		_ = f.Close()
		caps.Overwrite = true
		caps.Delete = os.Remove(name) == nil
	}
	return caps
}

func (s *store) path(key string) string { return filepath.Join(s.dir, key) }

func (s *store) Get(_ context.Context, key string) ([]byte, error) {
	data, err := os.ReadFile(s.path(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ranke.ErrNotFound
		}
		return nil, fmt.Errorf("%w: read %s: %w", errIO, key, err)
	}
	return data, nil
}

// Put writes data under key by temp+rename. Keys are content-addressed, so the
// overwrite repairs a corrupted file in place; callers dedup via Has.
func (s *store) Put(_ context.Context, key string, data []byte) error {
	return atomicWrite(s.path(key), data)
}

func (s *store) Has(_ context.Context, key string) (bool, error) {
	_, err := os.Stat(s.path(key))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// Delete unlinks key's file, an already-absent one being no error.
func (s *store) Delete(_ context.Context, key string) error {
	if err := os.Remove(s.path(key)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Open implements storage.Streamer: a raw file handle, so content streams
// off disk instead of being read whole into memory.
func (s *store) Open(_ context.Context, key string) (io.ReadCloser, error) {
	f, err := os.Open(s.path(key))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ranke.ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (s *store) Close() error { return nil }

// Capabilities returns the access rights probed at New: persistence is intrinsic
// to a filesystem, while overwrite/delete/enumerate reflect the directory's
// actual permissions (a read-only mount reports neither overwrite nor delete).
func (s *store) Capabilities() ranke.Capabilities {
	return s.caps
}

func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("%w: write tmp %s: %w", errIO, tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: rename %s -> %s: %w", errIO, tmp, path, err)
	}
	return nil
}
