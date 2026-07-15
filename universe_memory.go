// package: ranke / universe_memory
// type:    io
// job:     NewMemoryUniverse — a naive in-process Universe storing canonical CBOR in maps, decoded on read: the Graph's nil-Universe fallback and the reference Universe for tests
// limits:  ephemeral; correctness over speed (decodes per read, no caching); closure/copy via the Default* helpers; persistent/graph-native backends live under adapter/
package ranke

import (
	"bytes"
	"context"
	"io"
	"sync"
)

// NewMemoryUniverse returns an ephemeral in-process Universe: canonical claim
// CBOR keyed by id, content keyed by hash. It stores the exact bytes like any
// byte store and decodes on read, so it is stable and drift-free — the right
// reference behaviour, with no live-claim aliasing.
func NewMemoryUniverse() Universe {
	return &memoryUniverse{
		claims:  make(map[string][]byte),
		content: make(map[string][]byte),
		tags:    make(map[string]map[string]uint64),
	}
}

type memoryUniverse struct {
	mu      sync.RWMutex
	claims  map[string][]byte            // canonical CBOR by id
	content map[string][]byte            // content by hash
	tags    map[string]map[string]uint64 // id → branch → entry revision (a side-map; see Tagger)
}

func (u *memoryUniverse) GetClaims(ctx context.Context, ids []Id, opts ...GetOption) ([]Claim, error) {
	out := make([]Claim, len(ids))
	u.mu.RLock()
	for i, id := range ids {
		if id == nil {
			u.mu.RUnlock()
			return nil, errNilID
		}
		b, ok := u.claims[id.String()]
		if !ok {
			u.mu.RUnlock()
			return nil, ErrNotFound
		}
		c, err := DecodeClaim(id, b)
		if err != nil {
			u.mu.RUnlock()
			return nil, err
		}
		out[i] = c
	}
	u.mu.RUnlock()
	return DefaultMaterialize(ctx, u, out, opts...)
}

func (u *memoryUniverse) PutClaims(_ context.Context, cs []Claim) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, c := range cs {
		if c == nil || c.ID() == nil {
			return errNilClaim
		}
		b, err := c.Encode()
		if err != nil {
			return err
		}
		u.claims[c.ID().String()] = b
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

func (u *memoryUniverse) GetClaimsRaw(_ context.Context, ids []Id) ([][]byte, error) {
	out := make([][]byte, len(ids))
	u.mu.RLock()
	defer u.mu.RUnlock()
	for i, id := range ids {
		if id == nil {
			return nil, errNilID
		}
		b, ok := u.claims[id.String()]
		if !ok {
			return nil, ErrNotFound
		}
		out[i] = append([]byte(nil), b...) // stored bytes, verbatim
	}
	return out, nil
}

// GetClaimHeights decodes and reads the height (via the closure default) — no
// cache; correctness over speed.
func (u *memoryUniverse) GetClaimHeights(ctx context.Context, ids []Id) ([]uint64, error) {
	return DefaultGetClaimHeights(ctx, u, ids)
}

// TagBranch uses the reference walk (mem has no native pass).
func (u *memoryUniverse) TagBranch(ctx context.Context, branch string, head Id, revision uint64) error {
	return DefaultTagBranch(ctx, u, u, branch, head, revision)
}

func (u *memoryUniverse) SetBranchRevision(_ context.Context, claim Id, branch string, revision uint64) error {
	if claim == nil {
		return errNilID
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	m := u.tags[claim.String()]
	if m == nil {
		m = make(map[string]uint64)
		u.tags[claim.String()] = m
	}
	m[branch] = revision
	return nil
}

func (u *memoryUniverse) BranchRevision(_ context.Context, claim Id, branch string) (uint64, bool, error) {
	if claim == nil {
		return 0, false, errNilID
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	r, ok := u.tags[claim.String()][branch]
	return r, ok, nil
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
		out[i] = append([]byte(nil), b...)
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

func (u *memoryUniverse) CopyClaims(ctx context.Context, src Universe, ids []Id, opts ...CopyOption) error {
	return DefaultCopyClaims(ctx, u, src, ids, opts...)
}

func (u *memoryUniverse) CopyContents(ctx context.Context, src Universe, refs []ContentRef, opts ...CopyOption) error {
	return DefaultCopyContents(ctx, u, src, refs, opts...)
}

// Capabilities: a map can overwrite, delete, and enumerate, and serves raw
// claim CBOR and content blobs; it is not persistent (lost on process exit).
func (u *memoryUniverse) Capabilities() Capabilities {
	return Capabilities{Overwrite: true, Delete: true, Enumerate: true, RawClaims: true, ExternalContent: true, Tags: true}
}

func (u *memoryUniverse) Close() error { return nil }
