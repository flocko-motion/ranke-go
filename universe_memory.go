// package: ranke / universe_memory
// type:    io
// job:     NewMemoryUniverse — a naive in-process Universe storing canonical CBOR in maps, decoded on read: the Graph's nil-Universe fallback and the reference Universe for tests
// limits:  ephemeral; correctness over speed (decodes per read, no caching); closure/copy via the Default* helpers; persistent/graph-native backends live under adapter/
package ranke

import (
	"bytes"
	"context"
	"io"
	"path"
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
		tags:    make(map[string]map[string]string),
	}
}

type memoryUniverse struct {
	mu      sync.RWMutex
	claims  map[string][]byte            // canonical CBOR by id
	content map[string][]byte            // content by hash
	tags    map[string]map[string]string // id → tag key → value (side-map)
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
		if t := u.tags[id.String()]; len(t) > 0 {
			cp := make(map[string]string, len(t))
			for k, v := range t {
				cp[k] = v
			}
			c.unwrap().tags = cp // inject the tag overlay (tag-aware Universe)
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

// GetClaimTags returns each claim's tags positionally (nil when a claim has
// none). Each entry is a copy, so callers can't mutate the store.
func (u *memoryUniverse) GetClaimTags(_ context.Context, claims []Id) ([]map[string]string, error) {
	out := make([]map[string]string, len(claims))
	u.mu.RLock()
	defer u.mu.RUnlock()
	for i, id := range claims {
		if id == nil {
			return nil, errNilID
		}
		src := u.tags[id.String()]
		if len(src) == 0 {
			continue
		}
		cp := make(map[string]string, len(src))
		for k, v := range src {
			cp[k] = v
		}
		out[i] = cp
	}
	return out, nil
}

// SetClaimsTags applies tags per claim (keyed by id string): for each claim it
// first clears every existing tag whose key matches a clearTags glob, then
// applies the new key→value pairs.
func (u *memoryUniverse) SetClaimsTags(_ context.Context, clearTags []string, tags map[string]map[string]string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	for s, kv := range tags {
		m := u.tags[s]
		if m == nil {
			m = map[string]string{}
			u.tags[s] = m
		}
		for _, pat := range clearTags {
			for k := range m {
				if ok, _ := path.Match(pat, k); ok {
					delete(m, k)
				}
			}
		}
		for k, v := range kv {
			m[k] = v
		}
	}
	return nil
}

func (u *memoryUniverse) Query(ctx context.Context, q Query, scope Scope) (ResultStream, error) {
	return DefaultQuery(ctx, u, q, scope)
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
