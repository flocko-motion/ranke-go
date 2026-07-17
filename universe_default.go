// package: ranke / universe_default
// type:    logic
// job:     the reference Default* implementations a Universe delegates to — diff materialisation, closure membership/lookup, and copy walkers — all in terms of the public Universe API
// limits:  called by Universe implementations (byte-store adapters delegate here; a graph-native backend may override with native queries); does no storage of its own
package ranke

import (
	"context"
	"sync"
)

// GetOption configures a claim read (Universe.GetClaims and the package
// GetClaim helper).
type GetOption func(*getConfig)

type getConfig struct {
	rawDelta bool // return stored delta claims, skipping diff materialisation
}

// WithNotDiffMaterialized returns claims in their stored delta form — the
// contribution/diff overlay is NOT applied. The default is materialised
// (the overlay is resolved so GetField/Edges/content reflect the chain).
// Use it for the codec round-trip, tooling that inspects a diff, and the
// materialiser's own predecessor reads.
func WithNotDiffMaterialized() GetOption { return func(c *getConfig) { c.rawDelta = true } }

func newGetConfig(opts ...GetOption) getConfig {
	var c getConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// DefaultMaterialize is the fallback a Universe applies to its freshly
// loaded (raw) claims: it resolves each claim's contribution/diff overlay
// in place and returns the same slice. A byte-store backend calls it from
// GetClaims/GetFromClosure, passing the read opts straight through — this
// honours WithNotDiffMaterialized (returns the delta untouched). A
// graph-native backend may materialise itself instead. Idempotent: a
// non-diff or already-materialised claim is untouched. Predecessors are
// loaded through GetClaim (so they are materialised recursively by the
// same Universe).
func DefaultMaterialize(ctx context.Context, u Universe, claims []Claim, opts ...GetOption) ([]Claim, error) {
	if newGetConfig(opts...).rawDelta {
		return claims, nil // opt-out: return stored delta, no overlay
	}
	for _, cl := range claims {
		if err := materializeOne(ctx, u, cl.unwrap()); err != nil {
			return nil, err
		}
	}
	return claims, nil
}

// materializeOne resolves one claim's contribution/diff predecessor (if
// any) and computes its merged field/edge views. The delta (node.fields,
// c.edges) is untouched, so ID()/Encode() stay the claim's own bytes.
func materializeOne(ctx context.Context, u Universe, c *claim) error {
	if c == nil || c.diffClaim != nil {
		return nil // nothing to do / already materialised
	}
	var diff *edge
	for _, e := range c.edges {
		if e.typeClass == EdgeClassContribution && e.typeSub == string(EdgeSubtypeDiff) {
			diff = e
			break
		}
	}
	if diff == nil {
		return nil // not a diff claim
	}
	p, err := GetClaim(ctx, u, diff.reference) // materialises the predecessor recursively
	if err != nil {
		return err
	}
	pc := p.unwrap()
	c.diffClaim = pc
	c.node.diffNode = pc.node
	c.node.computeDiffFields()
	c.computeDiffEdges()
	return nil
}

// DefaultGetClaimHeights is the fallback GetClaimHeights for a Universe with no
// height index: it loads the claims (in delta form — height is the claim's own
// field, never inherited, so materialising the diff chain is unnecessary) and
// reads each committed height, positionally. A backend that keeps an id→height
// cache answers from it instead, deserialising only the misses — the whole
// point of putting height on the Universe port (§4.1). See HeightCache.
func DefaultGetClaimHeights(ctx context.Context, u Universe, ids []Id) ([]uint64, error) {
	cs, err := u.GetClaims(ctx, ids, WithNotDiffMaterialized())
	if err != nil {
		return nil, err
	}
	out := make([]uint64, len(cs))
	for i, c := range cs {
		out[i] = c.Node().Height()
	}
	return out, nil
}

// HeightCache memoises claim heights for a Universe. A height is immutable —
// fixed at creation and bound into the content-addressed id (§4.1) — so a
// cached entry is valid forever and needs no invalidation; the same id yields
// the same height in any Universe. A byte-store backend embeds one to answer
// GetClaimHeights without re-reading and re-decoding the claim, and populates
// it opportunistically from any get/put that already holds a claim (NoteClaims),
// so it fills naturally and the fallback load is rarely taken. The zero value
// is not usable — construct with NewHeightCache. All methods are safe for
// concurrent use, and a nil *HeightCache is a no-op reader (Get misses), so it
// degrades gracefully.
type HeightCache struct {
	mu sync.RWMutex
	m  map[string]uint64
}

// NewHeightCache returns an empty, ready-to-use cache.
func NewHeightCache() *HeightCache { return &HeightCache{m: make(map[string]uint64)} }

// Note records id→height if the id is not already cached. It checks under the
// read lock first and takes the write lock only on a miss — heights never
// change, so a present entry is authoritative and the hot path never
// serialises writers. Cheap enough to call on every claim a get/put handles.
func (c *HeightCache) Note(id Id, height uint64) {
	if c == nil || id == nil {
		return
	}
	k := id.String()
	c.mu.RLock()
	_, ok := c.m[k]
	c.mu.RUnlock()
	if ok {
		return // already known; heights are immutable, so never overwrite
	}
	c.mu.Lock()
	c.m[k] = height
	c.mu.Unlock()
}

// NoteClaims records the height of every non-nil claim — the convenience a
// get/put path calls after handling a batch, so reads and writes both warm the
// cache.
func (c *HeightCache) NoteClaims(cs ...Claim) {
	for _, cl := range cs {
		if cl != nil {
			c.Note(cl.ID(), cl.Node().Height())
		}
	}
}

// Get returns the cached height for id, if present.
func (c *HeightCache) Get(id Id) (uint64, bool) {
	if c == nil || id == nil {
		return 0, false
	}
	c.mu.RLock()
	h, ok := c.m[id.String()]
	c.mu.RUnlock()
	return h, ok
}

// GetClaimHeights answers ids from the cache, loading only the misses from u in
// one bulk DefaultGetClaimHeights and caching them. This is the cached
// GetClaimHeights a backend delegates to instead of the naive default.
func (c *HeightCache) GetClaimHeights(ctx context.Context, u Universe, ids []Id) ([]uint64, error) {
	out := make([]uint64, len(ids))
	var missIDs []Id
	var missAt []int
	for i, id := range ids {
		if h, ok := c.Get(id); ok {
			out[i] = h
			continue
		}
		missIDs = append(missIDs, id)
		missAt = append(missAt, i)
	}
	if len(missIDs) > 0 {
		hs, err := DefaultGetClaimHeights(ctx, u, missIDs)
		if err != nil {
			return nil, err
		}
		for k, h := range hs {
			out[missAt[k]] = h
			c.Note(missIDs[k], h)
		}
	}
	return out, nil
}

// DefaultCopyClaims is the reference CopyClaims for a Universe without a
// native fast path. It walks claim-by-claim via the public single-item
// helpers; a cloud backend should provide its own batched implementation.
// See Universe.CopyClaims for the option semantics. Progress is best-effort:
// a naive walker cannot know a closure's size in advance, so
// DiscoveryComplete stays false until the walk drains, then flips true.
func DefaultCopyClaims(ctx context.Context, dst, src Universe, ids []Id, opts ...CopyOption) error {
	if dst == nil || src == nil {
		return withDetail(errCopyClaims, "nil Universe")
	}
	cfg := NewCopyConfig(opts...)

	var prog CopyProgress
	report := func() {
		if cfg.Progress != nil {
			cfg.Progress(prog)
		}
	}

	seen := map[string]struct{}{}
	queue := make([]Id, 0, len(ids))
	for _, id := range ids {
		if id == nil {
			return withDetail(errCopyClaims, "nil id")
		}
		queue = append(queue, id)
	}

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		cur := queue[0]
		queue = queue[1:]
		k := cur.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}

		// A present claim is treated as fully copied — under the merge
		// invariant a claim never exists without its closure (and, for
		// copies made WithContent, its content). This makes the walk
		// idempotent and lets re-runs short-circuit.
		has, err := HasClaim(ctx, dst, cur)
		if err != nil {
			return wrapDetail(errCopyClaims, "dst.HasClaim "+k, err)
		}
		if has {
			continue
		}

		c, err := GetClaim(ctx, src, cur)
		if err != nil {
			return wrapDetail(errCopyClaims, "src.GetClaim "+k, err)
		}
		if err := PutClaim(ctx, dst, c); err != nil {
			return wrapDetail(errCopyClaims, "dst.PutClaim "+k, err)
		}
		prog.ClaimsCopied++

		// Copy content only when it is EXTERNAL: inline bytes travel inside the
		// claim record (already copied by PutClaim above) — they have no
		// content_hash and no separate blob (§Content). External content is a
		// blob in the content keyspace, keyed by content_hash, copied here.
		if cfg.Content && c.Node().ContentKind() == ContentExternal {
			ch := c.Node().GetContentHash()
			bs, err := src.GetContents(ctx, []ContentRef{{Hash: ch, ContentSize: c.Node().GetContentSize()}})
			if err != nil {
				return wrapDetail(errCopyClaims, "src.GetContents "+ch.String(), err)
			}
			if err := PutContent(ctx, dst, ch, bs[0]); err != nil {
				return wrapDetail(errCopyClaims, "dst.PutContent "+ch.String(), err)
			}
			prog.BytesCopied += uint64(len(bs[0]))
		}

		if cfg.Closure {
			for _, e := range c.Edges() {
				queue = append(queue, e.Reference())
			}
		}

		prog.ClaimsRemaining = uint64(len(queue))
		report()
	}

	prog.ClaimsRemaining = 0
	prog.BytesRemaining = 0
	prog.DiscoveryComplete = true
	report()
	return nil
}

// DefaultCopyContents is the reference CopyContents for a Universe without a
// native fast path. It copies blob-by-blob via the public single-item
// helpers, skipping any the receiver already has. WithClosure/WithContent
// are ignored; WithProgress is honoured.
func DefaultCopyContents(ctx context.Context, dst, src Universe, refs []ContentRef, opts ...CopyOption) error {
	if dst == nil || src == nil {
		return withDetail(errCopyContents, "nil Universe")
	}
	cfg := NewCopyConfig(opts...)

	var prog CopyProgress
	for i, ref := range refs {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ref.Hash == nil {
			return withDetail(errCopyContents, "nil hash")
		}
		has, err := HasContent(ctx, dst, ref.Hash)
		if err != nil {
			return wrapDetail(errCopyContents, "dst.HasContent "+ref.Hash.String(), err)
		}
		if !has {
			bs, err := src.GetContents(ctx, []ContentRef{{Hash: ref.Hash, ContentSize: ref.ContentSize}})
			if err != nil {
				return wrapDetail(errCopyContents, "src.GetContents "+ref.Hash.String(), err)
			}
			if err := PutContent(ctx, dst, ref.Hash, bs[0]); err != nil {
				return wrapDetail(errCopyContents, "dst.PutContent "+ref.Hash.String(), err)
			}
			prog.BytesCopied += uint64(len(bs[0]))
		}
		prog.ClaimsRemaining = uint64(len(refs) - i - 1)
		if cfg.Progress != nil {
			cfg.Progress(prog)
		}
	}

	prog.ClaimsRemaining = 0
	prog.DiscoveryComplete = true
	if cfg.Progress != nil {
		cfg.Progress(prog)
	}
	return nil
}

// DefaultSync is the reference Sync: it fills dst for id's closure by copying
// from src (claims + content), which walks the closure and skips what dst
// already has, so only the gap is fetched. A nil src means there is nothing to
// copy from, so dst is taken as already synced.
func DefaultSync(ctx context.Context, dst, src Universe, id Id) <-chan SyncResult {
	out := make(chan SyncResult, 1)
	go func() {
		defer close(out)
		if src == nil {
			out <- SyncResult{SyncedTo: id}
			return
		}
		if err := dst.CopyClaims(ctx, src, []Id{id}, WithClosure(), WithContent()); err != nil {
			out <- SyncResult{Err: err}
			return
		}
		out <- SyncResult{SyncedTo: id}
	}()
	return out
}
