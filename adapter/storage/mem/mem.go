// package: mem / persistence
// type:    adapter
// job:     ephemeral in-memory Universe backed by maps, for tests and short-lived sessions
// limits:  nothing persists across process restarts (-> adapter/fs)
//
// Package mem is an ephemeral in-memory persistence adapter for a ranke
// Universe, backed by maps. It holds live claim objects and content bytes
// keyed by id — useful for tests and short-lived sessions. Nothing
// persists across process restarts.
//
// mem deliberately stays standalone rather than folding onto
// storage.BlobStore: holding live claim objects (not encoded bytes) avoids
// codec round-trips and leaves room to optimize over time. The frozen,
// minimal BlobStore illustration lives in adapter/minimal.
package mem

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage"
)

// New returns an ephemeral, in-memory Universe.
func New() ranke.Universe {
	return &memUniverse{
		claims:  make(map[string]ranke.Claim),
		content: make(map[string][]byte),
	}
}

type memUniverse struct {
	mu      sync.RWMutex
	claims  map[string]ranke.Claim
	content map[string][]byte
}

func (u *memUniverse) Close() error { return nil }

func (u *memUniverse) GetClaims(_ context.Context, ids []ranke.Id) ([]ranke.Claim, error) {
	out := make([]ranke.Claim, len(ids))
	u.mu.RLock()
	defer u.mu.RUnlock()
	for i, id := range ids {
		if id == nil {
			return nil, errors.New("adapter/mem.GetClaims: nil id")
		}
		c, ok := u.claims[id.String()]
		if !ok {
			return nil, ranke.ErrNotFound
		}
		out[i] = c
	}
	return out, nil
}

func (u *memUniverse) PutClaims(_ context.Context, cs []ranke.Claim) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, c := range cs {
		if c == nil || c.ID() == nil {
			return errors.New("adapter/mem.PutClaims: nil claim or id")
		}
		u.claims[c.ID().String()] = c
	}
	return nil
}

func (u *memUniverse) HasClaims(_ context.Context, ids []ranke.Id) ([]bool, error) {
	out := make([]bool, len(ids))
	u.mu.RLock()
	defer u.mu.RUnlock()
	for i, id := range ids {
		if id == nil {
			continue
		}
		_, out[i] = u.claims[id.String()]
	}
	return out, nil
}

func (u *memUniverse) GetContents(_ context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	out := make([][]byte, len(refs))
	u.mu.RLock()
	defer u.mu.RUnlock()
	for i, ref := range refs {
		if ref.Hash == nil {
			return nil, errors.New("adapter/mem.GetContents: nil hash")
		}
		b, ok := u.content[ref.Hash.String()]
		if !ok {
			return nil, ranke.ErrNotFound
		}
		cp := make([]byte, len(b))
		copy(cp, b)
		out[i] = cp
	}
	return out, nil
}

func (u *memUniverse) StreamContent(ctx context.Context, hash ranke.Id, size uint64) (io.ReadCloser, error) {
	bs, err := u.GetContents(ctx, []ranke.ContentRef{{Hash: hash, Size: size}})
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytesReader(bs[0])), nil
}

func (u *memUniverse) PutContents(_ context.Context, blobs []ranke.ContentBlob) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, bl := range blobs {
		if bl.Hash == nil {
			return errors.New("adapter/mem.PutContents: nil hash")
		}
		cp := make([]byte, len(bl.Content))
		copy(cp, bl.Content)
		u.content[bl.Hash.String()] = cp
	}
	return nil
}

func (u *memUniverse) HasContents(_ context.Context, hashes []ranke.Id) ([]bool, error) {
	out := make([]bool, len(hashes))
	u.mu.RLock()
	defer u.mu.RUnlock()
	for i, hash := range hashes {
		if hash == nil {
			continue
		}
		_, out[i] = u.content[hash.String()]
	}
	return out, nil
}

func (u *memUniverse) CopyClaims(ctx context.Context, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	return storage.DefaultCopyClaims(ctx, u, src, ids, opts...)
}

func (u *memUniverse) CopyContents(ctx context.Context, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	return storage.DefaultCopyContents(ctx, u, src, refs, opts...)
}

// Capabilities reports the in-memory store can overwrite, delete, and enumerate
// (a map does all three), but is not persistent — it is lost on process exit.
// InClosure reports whether id is in head's closure.
//
// TODO: implement — walk the edge closure from head over the in-memory
// claim map.
func (u *memUniverse) InClosure(ctx context.Context, head, id ranke.Id) (bool, error) {
	panic("TODO: memUniverse.InClosure not implemented")
}

// GetFromClosure returns the claim at id if it is in head's closure.
//
// TODO: implement — closure-membership check, then resolve via GetClaims.
func (u *memUniverse) GetFromClosure(ctx context.Context, head, id ranke.Id) (ranke.Claim, error) {
	panic("TODO: memUniverse.GetFromClosure not implemented")
}

func (u *memUniverse) Capabilities() ranke.Capabilities {
	return ranke.Capabilities{Overwrite: true, Delete: true, Enumerate: true}
}

type bytesReaderT struct {
	b   []byte
	pos int
}

func bytesReader(b []byte) *bytesReaderT { return &bytesReaderT{b: b} }

func (r *bytesReaderT) Read(p []byte) (int, error) {
	if r.pos >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.pos:])
	r.pos += n
	return n, nil
}
