// package: stack / persistence
// type:    adapter
// job:     compose ordered Universe layers into one read-through / write-through Universe — cache + durable tiers with self-healing repair (paper §Composing Universes)
// limits:  no storage of its own (-> the layer Universes); naming/selecting layers is the caller's (-> ranke-db config)
package stack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/flocko-motion/ranke-go"
)

var (
	errNoLayers        = errors.New("stack: at least one layer required")
	errNilLayer        = errors.New("stack: layer has a nil Universe")
	errNoAuthoritative = errors.New("stack: at least one authoritative layer required")
	errInvalidTier     = errors.New("stack: layer reports a tier its capabilities do not allow")
)

type layer struct {
	u    ranke.Universe
	caps ranke.Capabilities
}

func (l layer) tier() ranke.StorageTier { return l.caps.Tier }

// synchronous: written and awaited on the write path (authoritative + eager).
func (l layer) synchronous() bool {
	return l.caps.Tier == ranke.StorageTierAuthoritative || l.caps.Tier == ranke.StorageTierEager
}

type stack struct {
	layers []layer
}

// couldHoldContent reports whether a layer with caps could hold a content blob
// of the given size: an external-content store holds any size; a capped cache
// holds it only up to its cap.
func couldHoldContent(c ranke.Capabilities, size uint64) bool {
	return c.ExternalContent || (c.ContentCap > 0 && size <= c.ContentCap)
}

// holdsSomeContent reports whether a layer holds any content at all (used where
// the blob size is unknown, e.g. HasContents).
func holdsSomeContent(c ranke.Capabilities) bool {
	return c.ExternalContent || c.ContentCap > 0
}

// NewStack composes layers top (first) to bottom (last), each carrying its own
// Capabilities.Tier. At least one authoritative layer is required.
func NewStack(us ...ranke.Universe) (ranke.Universe, error) {
	if len(us) == 0 {
		return nil, errNoLayers
	}
	layers := make([]layer, len(us))
	auth := 0
	for i, u := range us {
		if u == nil {
			return nil, errNilLayer
		}
		c := u.Capabilities()
		if !c.AllowsTier(c.Tier) {
			return nil, fmt.Errorf("%w: layer %d tier %q", errInvalidTier, i, c.Tier)
		}
		if c.Tier == ranke.StorageTierAuthoritative {
			auth++
		}
		layers[i] = layer{u: u, caps: c}
	}
	if auth == 0 {
		return nil, errNoAuthoritative
	}
	return &stack{layers: layers}, nil
}

// PutClaims routes by tier: authoritative+eager in parallel (only an
// authoritative failure fails the call), background async, lazy not written.
func (s *stack) PutClaims(ctx context.Context, cs []ranke.Claim) error {
	s.background(ctx, func(ctx context.Context, l layer) { _ = l.u.PutClaims(ctx, cs) })
	return s.writeSync(ctx, func(ctx context.Context, l layer) error { return l.u.PutClaims(ctx, cs) })
}

// PutContents is PutClaims for content, sending each blob only to layers whose
// cap can hold it (authoritative holds any size, so nothing is ever dropped).
func (s *stack) PutContents(ctx context.Context, blobs []ranke.ContentBlob) error {
	fit := func(l layer) []ranke.ContentBlob {
		if l.caps.ExternalContent {
			return blobs // holds any size — no filtering
		}
		var sel []ranke.ContentBlob
		for _, b := range blobs {
			if couldHoldContent(l.caps, uint64(len(b.Content))) {
				sel = append(sel, b)
			}
		}
		return sel
	}
	s.background(ctx, func(ctx context.Context, l layer) {
		if sel := fit(l); len(sel) > 0 {
			_ = l.u.PutContents(ctx, sel)
		}
	})
	return s.writeSync(ctx, func(ctx context.Context, l layer) error {
		sel := fit(l)
		if len(sel) == 0 {
			return nil
		}
		return l.u.PutContents(ctx, sel)
	})
}

// writeSync runs put over the authoritative+eager layers in parallel, returning
// only the authoritative layers' joined error (eager failures are swallowed).
func (s *stack) writeSync(ctx context.Context, put func(context.Context, layer) error) error {
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		authErr error
	)
	for i := range s.layers {
		l := s.layers[i]
		if !l.synchronous() {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := put(ctx, l); err != nil && l.tier() == ranke.StorageTierAuthoritative {
				mu.Lock()
				authErr = errors.Join(authErr, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return authErr
}

// background fires put at each background-tier layer on a detached context, in a
// goroutine the caller does not wait for (errors discarded, fills eventually).
func (s *stack) background(ctx context.Context, put func(context.Context, layer)) {
	for i := range s.layers {
		l := s.layers[i]
		if l.tier() != ranke.StorageTierBackground {
			continue
		}
		bg := context.WithoutCancel(ctx)
		go put(bg, l)
	}
}

func (s *stack) GetClaims(ctx context.Context, ids []ranke.Id, opts ...ranke.GetOption) ([]ranke.Claim, error) {
	out := make([]ranke.Claim, len(ids))
	pending := seq(len(ids))
	for li := range s.layers {
		if len(pending) == 0 {
			break
		}
		has, err := s.layers[li].u.HasClaims(ctx, pickIds(ids, pending))
		if err != nil {
			return nil, err
		}
		var hitIDs []ranke.Id
		var hitAt []int
		var still []int
		for k, orig := range pending {
			if has[k] {
				hitIDs = append(hitIDs, ids[orig])
				hitAt = append(hitAt, orig)
			} else {
				still = append(still, orig)
			}
		}
		if len(hitIDs) > 0 {
			if ranke.ReportEnabled(ctx, ranke.ReportDebug) {
				ranke.ReportEvent(ctx, "stack", "route", ranke.ReportDebug, "",
					map[string]any{"layer": li, "hits": len(hitIDs)})
			}
			// Fetch raw delta from the layer; materialise at the stack level
			// (below) so a diff's predecessor resolves across all layers.
			got, err := s.layers[li].u.GetClaims(ctx, hitIDs, ranke.WithNotDiffMaterialized())
			if err != nil {
				return nil, err
			}
			for j, c := range got {
				out[hitAt[j]] = c
			}
			s.fillClaims(ctx, li, got)
		}
		pending = still
	}
	if len(pending) > 0 {
		return nil, ranke.ErrNotFound
	}
	// Materialise diff overlays at the stack level, honouring the read opts.
	return ranke.DefaultMaterialize(ctx, s, out, opts...)
}

func (s *stack) GetContents(ctx context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	out := make([][]byte, len(refs))
	pending := seq(len(refs))
	for li := range s.layers {
		if len(pending) == 0 {
			break
		}
		caps := s.layers[li].caps
		// Cap-aware descent: only ask this layer about refs it could hold given
		// its content cap; larger blobs (and all refs, if it holds no content)
		// stay pending for a lower layer — no wasted query.
		var ask, defer_ []int
		for _, orig := range pending {
			if couldHoldContent(caps, refs[orig].ContentSize) {
				ask = append(ask, orig)
			} else {
				defer_ = append(defer_, orig)
			}
		}
		if len(ask) == 0 {
			pending = defer_
			continue
		}
		has, err := s.layers[li].u.HasContents(ctx, pickHashes(refs, ask))
		if err != nil {
			return nil, err
		}
		var hitRefs []ranke.ContentRef
		var hitAt []int
		var still []int
		for k, orig := range ask {
			if has[k] {
				hitRefs = append(hitRefs, refs[orig])
				hitAt = append(hitAt, orig)
			} else {
				still = append(still, orig)
			}
		}
		if len(hitRefs) > 0 {
			got, err := s.layers[li].u.GetContents(ctx, hitRefs)
			switch {
			case errors.Is(err, ranke.ErrIntegrity), errors.Is(err, ranke.ErrContentCapped):
				// Corrupt or capped bytes at this layer (the latter e.g. a cache
				// filled under a smaller cap): descend to a full copy, keep
				// these pending (integrity repairs on the way back).
				still = append(still, hitAt...)
			case err != nil:
				return nil, err
			default:
				for j, b := range got {
					out[hitAt[j]] = b
				}
				s.fillContents(ctx, li, hitRefs, got)
			}
		}
		pending = append(defer_, still...) // cap-exceeded + misses go to the next layer
	}
	if len(pending) > 0 {
		return nil, ranke.ErrNotFound
	}
	return out, nil
}

func (s *stack) HasClaims(ctx context.Context, ids []ranke.Id) ([]bool, error) {
	out := make([]bool, len(ids))
	pending := seq(len(ids))
	for li := range s.layers {
		if len(pending) == 0 {
			break
		}
		has, err := s.layers[li].u.HasClaims(ctx, pickIds(ids, pending))
		if err != nil {
			return nil, err
		}
		var still []int
		for k, orig := range pending {
			if has[k] {
				out[orig] = true
			} else {
				still = append(still, orig)
			}
		}
		pending = still
	}
	return out, nil
}

func (s *stack) HasContents(ctx context.Context, hashes []ranke.Id) ([]bool, error) {
	out := make([]bool, len(hashes))
	pending := seq(len(hashes))
	for li := range s.layers {
		if len(pending) == 0 {
			break
		}
		if !holdsSomeContent(s.layers[li].caps) {
			continue // no content store at all — skip (HasContents lacks sizes to cap-filter)
		}
		has, err := s.layers[li].u.HasContents(ctx, pickIds(hashes, pending))
		if err != nil {
			return nil, err
		}
		var still []int
		for k, orig := range pending {
			if has[k] {
				out[orig] = true
			} else {
				still = append(still, orig)
			}
		}
		pending = still
	}
	return out, nil
}

func (s *stack) StreamContent(ctx context.Context, hash ranke.Id, size uint64) (io.ReadCloser, error) {
	for li := range s.layers {
		if !couldHoldContent(s.layers[li].caps, size) {
			continue // cap-aware: this layer can't hold a blob this large — skip
		}
		ok, err := ranke.HasContent(ctx, s.layers[li].u, hash)
		if err != nil {
			return nil, err
		}
		if ok {
			return s.layers[li].u.StreamContent(ctx, hash, size)
		}
	}
	return nil, ranke.ErrNotFound
}

func (s *stack) CopyClaims(ctx context.Context, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyClaims(ctx, s, src, ids, opts...)
}

func (s *stack) CopyContents(ctx context.Context, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyContents(ctx, s, src, refs, opts...)
}

// GetClaimHeights resolves heights through the stack's own GetClaims — the one
// place the layer traversal (fall-through + read-fill) lives — then reads each
// committed height. Delegating to DefaultGetClaimHeights keeps that traversal
// unduplicated; the stack's fast top layer (and any leaf height cache it warms)
// makes the underlying read cheap.
func (s *stack) GetClaimHeights(ctx context.Context, ids []ranke.Id) ([]uint64, error) {
	return ranke.DefaultGetClaimHeights(ctx, s, ids)
}

// Query resolves an RQL read through the stack's own read path (GetClaims,
// which falls through the layers) via the reference executor; reverse steps
// work via closure inversion (ReverseWalk advertises a native path, not ability).
func (s *stack) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	return ranke.DefaultQuery(ctx, s, q, scope)
}

// GetClaimsRaw returns the stored CBOR, tried layer by layer top-down: the
// first layer holding the bytes wins; a layer that lacks them (a structure-only
// cache like neo4j returns ErrNotFound) is skipped, so the request falls
// through to the authoritative byte layer. Verification and replication use
// this — not the hot read path — so it fetches, it does not read-fill.
func (s *stack) GetClaimsRaw(ctx context.Context, ids []ranke.Id) ([][]byte, error) {
	var lastErr error = ranke.ErrNotFound
	for li := range s.layers {
		// Capability-aware descent: a layer that keeps no verbatim canonical
		// bytes (a structure-only cache like neo4j) can never serve raw claims,
		// so skip it rather than spending a round-trip to be told ErrNotFound.
		if !s.layers[li].caps.RawClaims {
			continue
		}
		raw, err := s.layers[li].u.GetClaimsRaw(ctx, ids)
		if err == nil {
			return raw, nil
		}
		if !errors.Is(err, ranke.ErrNotFound) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// GetClaimTags reads from the first (top-down) tag-holding layer; a stack with
// no such layer is unsupported. Tags are a per-claim runtime overlay, not
// content — no fall-through or read-fill, one layer owns them.
func (s *stack) GetClaimTags(ctx context.Context, claims []ranke.Id) ([]map[string]string, error) {
	for li := range s.layers {
		if !s.layers[li].caps.Tags {
			continue
		}
		return s.layers[li].u.GetClaimTags(ctx, claims)
	}
	return nil, ranke.ErrUnsupported
}

// SetClaimsTags writes to every tag-holding layer (keeping them consistent);
// unsupported if no layer holds tags.
func (s *stack) SetClaimsTags(ctx context.Context, clearTags []string, tags map[string]map[string]string) error {
	found := false
	for li := range s.layers {
		if !s.layers[li].caps.Tags {
			continue
		}
		found = true
		if err := s.layers[li].u.SetClaimsTags(ctx, clearTags, tags); err != nil {
			return err
		}
	}
	if !found {
		return ranke.ErrUnsupported
	}
	return nil
}

// Capabilities derives the stack's from its layers, per capability:
//   - Overwrite: every synchronous layer can (repair must reach the durable tier);
//   - Delete: every layer can (else a deleted key lingers in some layer);
//   - Enumerate: some synchronous layer can (it holds the whole keyset);
//   - Persistent: an authoritative layer is (the durable tier survives);
//   - ReverseWalk: some layer can walk edges backward (a query routes to it);
//   - Tags: some layer holds the tag overlay;
//   - RawClaims: some layer keeps verbatim bytes (raw reads route to it);
//   - ExternalContent: some layer holds unbounded content (the stack does too);
//   - ContentCap: 0 if some layer is unbounded, else the largest layer cap.
//
// Tier is authoritative — a stack has one.
func (s *stack) Capabilities() ranke.Capabilities {
	overwrite, del := true, true
	var enumerate, persistent, reverseWalk, rawClaims, externalContent, tags bool
	var contentCap uint64
	for _, l := range s.layers {
		c := l.caps
		if !c.Delete {
			del = false
		}
		if c.ReverseWalk {
			reverseWalk = true
		}
		if c.Tags {
			tags = true
		}
		if c.RawClaims {
			rawClaims = true
		}
		if c.ExternalContent {
			externalContent = true
		}
		if c.ContentCap > contentCap {
			contentCap = c.ContentCap
		}
		if l.synchronous() {
			if !c.Overwrite {
				overwrite = false
			}
			if c.Enumerate {
				enumerate = true
			}
		}
		if c.Tier == ranke.StorageTierAuthoritative && c.Persistent {
			persistent = true
		}
	}
	if externalContent {
		contentCap = 0 // an unbounded layer means the stack holds any size
	}
	return ranke.Capabilities{
		Overwrite:       overwrite,
		Delete:          del,
		Enumerate:       enumerate,
		Persistent:      persistent,
		ReverseWalk:     reverseWalk,
		RawClaims:       rawClaims,
		ExternalContent: externalContent,
		ContentCap:      contentCap,
		Tags:            tags,
		Tier:            ranke.StorageTierAuthoritative,
	}
}

// Sync routes: it tells the first eager layer from the top to sync itself from
// the layers below it (which it passes as the source) — the stack only picks the
// layer and the source; the layer does the copying. With no eager layer the
// authoritative tier serves reads directly, so it is already synced.
func (s *stack) Sync(ctx context.Context, _ ranke.Universe, id ranke.Id) <-chan ranke.SyncResult {
	ei := -1
	for i := range s.layers {
		if s.layers[i].tier() == ranke.StorageTierEager {
			ei = i
			break
		}
	}
	if ei < 0 || ei == len(s.layers)-1 {
		return ranke.SyncedNow(id)
	}
	below := make([]ranke.Universe, 0, len(s.layers)-ei-1)
	for _, l := range s.layers[ei+1:] {
		below = append(below, l.u)
	}
	src, err := NewStack(below...)
	if err != nil {
		out := make(chan ranke.SyncResult, 1)
		out <- ranke.SyncResult{Err: err}
		close(out)
		return out
	}
	return s.layers[ei].u.Sync(ctx, src, id)
}

// Close closes every layer (best-effort: all are attempted, errors joined).
func (s *stack) Close() error {
	var errs []error
	for _, l := range s.layers {
		if err := l.u.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// fillClaims writes claims served from below into every layer above (best-effort
// repair), except it never seeds a verbatim layer from a lossy projection.
func (s *stack) fillClaims(ctx context.Context, servedAt int, cs []ranke.Claim) {
	verbatimSource := s.layers[servedAt].caps.RawClaims
	for i := 0; i < servedAt; i++ {
		if s.layers[i].caps.RawClaims && !verbatimSource {
			continue // fidelity guard: don't seed a verbatim layer from a projection
		}
		_ = s.layers[i].u.PutClaims(ctx, cs)
	}
}

// fillContents is fillClaims for content, honouring each layer's cap; content is
// hash-verified, so there is no fidelity hazard (any layer fills from any other).
func (s *stack) fillContents(ctx context.Context, servedAt int, refs []ranke.ContentRef, datas [][]byte) {
	for i := 0; i < servedAt; i++ {
		l := s.layers[i]
		var blobs []ranke.ContentBlob
		for j, r := range refs {
			if couldHoldContent(l.caps, uint64(len(datas[j]))) {
				blobs = append(blobs, ranke.ContentBlob{Hash: r.Hash, Content: datas[j]})
			}
		}
		if len(blobs) > 0 {
			_ = l.u.PutContents(ctx, blobs)
		}
	}
}

func seq(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = i
	}
	return s
}

func pickIds(ids []ranke.Id, idx []int) []ranke.Id {
	out := make([]ranke.Id, len(idx))
	for i, j := range idx {
		out[i] = ids[j]
	}
	return out
}

func pickHashes(refs []ranke.ContentRef, idx []int) []ranke.Id {
	out := make([]ranke.Id, len(idx))
	for i, j := range idx {
		out[i] = refs[j].Hash
	}
	return out
}
