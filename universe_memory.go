// package: ranke / universe_memory
// type:    io
// job:     NewMemoryUniverse — a naive in-process Universe (live claims + content in maps): the fallback store a Graph uses when given no Universe, and the reference Universe for tests
// limits:  ephemeral (nothing survives a restart); no native fast paths — closure and copy delegate to the Default* helpers; persistent/graph-native backends live under adapter/
package ranke

import (
	"bytes"
	"context"
	"io"
	"sync"
)

// NewMemoryUniverse returns an ephemeral in-process Universe backed by maps —
// live claims keyed by id, content keyed by hash. It is the fallback a Graph
// uses when constructed with a nil Universe, and the reference Universe for
// tests. Persistent and graph-native backends live under adapter/.
//
// It holds live claim objects, so diff materialisation (applied on the
// default read) mutates the stored instance in place and is therefore sticky
// — a caching-store trait, harmless for build/verify where it only saves
// recomputation.
func NewMemoryUniverse() Universe {
	return &memoryUniverse{
		claims:  make(map[string]Claim),
		content: make(map[string][]byte),
	}
}

type memoryUniverse struct {
	mu      sync.RWMutex
	claims  map[string]Claim
	content map[string][]byte
}

func (u *memoryUniverse) GetClaims(ctx context.Context, ids []Id, opts ...GetOption) ([]Claim, error) {
	out := make([]Claim, len(ids))
	u.mu.RLock()
	for i, id := range ids {
		if id == nil {
			u.mu.RUnlock()
			return nil, errNilID
		}
		c, ok := u.claims[id.String()]
		if !ok {
			u.mu.RUnlock()
			return nil, ErrNotFound
		}
		out[i] = c
	}
	u.mu.RUnlock()
	// Diff-materialised by default; honours WithNotDiffMaterialized.
	return DefaultMaterialize(ctx, u, out, opts...)
}

func (u *memoryUniverse) PutClaims(_ context.Context, cs []Claim) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, c := range cs {
		if c == nil || c.ID() == nil {
			return errNilClaim
		}
		u.claims[c.ID().String()] = c
	}
	return nil
}

func (u *memoryUniverse) HasClaims(_ context.Context, ids []Id) ([]bool, error) {
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

func (u *memoryUniverse) GetContents(_ context.Context, refs []ContentRef) ([][]byte, error) {
	out := make([][]byte, len(refs))
	u.mu.RLock()
	defer u.mu.RUnlock()
	for i, ref := range refs {
		if ref.Hash == nil {
			return nil, errNilHash
		}
		b, ok := u.content[ref.Hash.String()]
		if !ok {
			return nil, ErrNotFound
		}
		out[i] = append([]byte(nil), b...) // defensive copy
	}
	return out, nil
}

func (u *memoryUniverse) PutContents(_ context.Context, blobs []ContentBlob) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, bl := range blobs {
		if bl.Hash == nil {
			return errNilHash
		}
		u.content[bl.Hash.String()] = append([]byte(nil), bl.Content...)
	}
	return nil
}

func (u *memoryUniverse) HasContents(_ context.Context, hashes []Id) ([]bool, error) {
	out := make([]bool, len(hashes))
	u.mu.RLock()
	defer u.mu.RUnlock()
	for i, h := range hashes {
		if h == nil {
			continue
		}
		_, out[i] = u.content[h.String()]
	}
	return out, nil
}

func (u *memoryUniverse) StreamContent(ctx context.Context, hash Id, size uint64) (io.ReadCloser, error) {
	bs, err := u.GetContents(ctx, []ContentRef{{Hash: hash, ContentSize: size}})
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(bs[0])), nil
}

func (u *memoryUniverse) InClosure(ctx context.Context, heads []Id, id Id) (bool, error) {
	return DefaultInClosure(ctx, u, heads, id)
}

func (u *memoryUniverse) GetFromClosure(ctx context.Context, heads []Id, id Id) (Claim, error) {
	return DefaultGetFromClosure(ctx, u, heads, id)
}

// GetClaimHeights reads heights straight from the live claim map — the map is
// already the memo, so no separate HeightCache is warranted here (unlike a
// byte store, which would re-decode). Height is the node's own field, so no
// materialisation is needed.
func (u *memoryUniverse) GetClaimHeights(_ context.Context, ids []Id) ([]uint64, error) {
	out := make([]uint64, len(ids))
	u.mu.RLock()
	defer u.mu.RUnlock()
	for i, id := range ids {
		if id == nil {
			return nil, errNilID
		}
		c, ok := u.claims[id.String()]
		if !ok {
			return nil, ErrNotFound
		}
		out[i] = c.Node().Height()
	}
	return out, nil
}

func (u *memoryUniverse) CopyClaims(ctx context.Context, src Universe, ids []Id, opts ...CopyOption) error {
	return DefaultCopyClaims(ctx, u, src, ids, opts...)
}

func (u *memoryUniverse) CopyContents(ctx context.Context, src Universe, refs []ContentRef, opts ...CopyOption) error {
	return DefaultCopyContents(ctx, u, src, refs, opts...)
}

// Capabilities: a map can overwrite, delete, and enumerate; it is not
// persistent (lost on process exit).
func (u *memoryUniverse) Capabilities() Capabilities {
	return Capabilities{Overwrite: true, Delete: true, Enumerate: true}
}

func (u *memoryUniverse) Close() error { return nil }
