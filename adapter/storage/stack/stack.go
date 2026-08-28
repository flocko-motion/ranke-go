// package: stack / persistence
// type:    adapter
// job:     compose ordered Universe layers into one read-through / write-through Universe — cache +
// durable tiers with self-healing repair (paper §Composing Universes)
// limits:  no storage of its own (-> the layer Universes); naming/selecting layers is the caller's
// (-> ranke-db config)
package stack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/rankegraph/ranke-go"
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
	heal   *filler // opportunistic cache writes, off the read path
}

// couldHoldContent reports whether a layer with caps could hold a blob of size:
// an external-content store holds any size, a capped cache up to its cap.
func couldHoldContent(c ranke.Capabilities, size uint64) bool {
	return c.ExternalContent || (c.ContentCap > 0 && size <= c.ContentCap)
}

// holdsSomeContent reports whether a layer holds content, the size being unknown.
func holdsSomeContent(c ranke.Capabilities) bool {
	return c.ExternalContent || c.ContentCap > 0
}

// NewStack composes layers top (first) to bottom (last) by each one's
// Capabilities.Tier; one layer must be authoritative.
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
	s := &stack{layers: layers}
	s.heal = newFiller(s.writeBatch)
	return s, nil
}

// PutClaims routes by tier: authoritative+eager in parallel (only an
// authoritative failure fails the call), background async, lazy not written.
func (s *stack) PutClaims(ctx context.Context, cs []ranke.Claim) error {
	s.background(ctx, func(ctx context.Context, l layer) { _ = l.u.PutClaims(ctx, cs) })
	return s.writeSync(ctx, func(ctx context.Context, l layer) error { return l.u.PutClaims(ctx, cs) })
}

// PutContents is PutClaims for content, sending each blob to the layers whose cap holds it.
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
// the authoritative layers' joined error.
// DeleteClaims removes the bytes from EVERY layer, not by tier. A lawful deletion is
// only done when no layer still serves the claim, so a cache left holding it would
// undo the deletion on the next read — which is why a stack reports Delete only when
// all its layers do, and refuses as a whole when one cannot.
func (s *stack) DeleteClaims(ctx context.Context, ids []ranke.Id) error {
	return s.deleteEverywhere(ctx, func(ctx context.Context, l layer) error {
		return l.u.DeleteClaims(ctx, ids)
	})
}

// DeleteContents removes the blobs from every layer, on DeleteClaims' terms.
func (s *stack) DeleteContents(ctx context.Context, hashes []ranke.Id) error {
	return s.deleteEverywhere(ctx, func(ctx context.Context, l layer) error {
		return l.u.DeleteContents(ctx, hashes)
	})
}

// deleteEverywhere refuses before removing anything where a layer cannot delete, so a
// partial removal is not reported as a deletion. Sequential: a half-deleted stack is
// worse than a slow one, and the first failure names the layer.
func (s *stack) deleteEverywhere(ctx context.Context, del func(context.Context, layer) error) error {
	for i := range s.layers {
		if !s.layers[i].caps.Delete {
			return fmt.Errorf("%w: layer %d cannot delete, so the stack cannot", ranke.ErrUnsupported, i)
		}
	}
	for i := range s.layers {
		if err := del(ctx, s.layers[i]); err != nil {
			return fmt.Errorf("delete in layer %d: %w", i, err)
		}
	}
	return nil
}

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

// background fires put at each background-tier layer in a detached goroutine.
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

// GetClaims routes by the form asked for: a RawClaims layer keeps deltas, so it is
// asked in delta form and materialised at stack level, where predecessors resolve.
func (s *stack) GetClaims(ctx context.Context, ids []ranke.Id, opts ...ranke.GetOption) ([]ranke.Claim, error) {
	wantDelta := ranke.WantsDelta(opts...)
	out := make([]ranke.Claim, len(ids))
	pending := seq(len(ids))
	var resolve []ranke.Claim // fetched as deltas, still to be materialised
	for li := range s.layers {
		if len(pending) == 0 {
			break
		}
		keepsDeltas := s.layers[li].caps.RawClaims
		if wantDelta && !keepsDeltas {
			continue // it has no delta to give — do not spend a round trip
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
					map[string]any{"layer": li, "hits": len(hitIDs), "delta": keepsDeltas})
			}
			var layerOpts []ranke.GetOption
			if keepsDeltas {
				layerOpts = append(layerOpts, ranke.WithNotDiffMaterialized())
			}
			got, err := s.layers[li].u.GetClaims(ctx, hitIDs, layerOpts...)
			if err != nil {
				return nil, err
			}
			for j, c := range got {
				out[hitAt[j]] = c
			}
			if keepsDeltas && !wantDelta {
				resolve = append(resolve, got...)
			}
			s.fillClaims(ctx, li, got)
		}
		pending = still
	}
	if len(pending) > 0 {
		return nil, ranke.ErrNotFound
	}
	if len(resolve) > 0 {
		if _, err := ranke.DefaultMaterialize(ctx, s, resolve); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *stack) GetContents(ctx context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	out := make([][]byte, len(refs))
	pending := seq(len(refs))
	for li := range s.layers {
		if len(pending) == 0 {
			break
		}
		caps := s.layers[li].caps
		// Cap-aware descent: ask this layer only about refs its cap could hold;
		// larger blobs stay pending for a lower layer.
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
				// Corrupt or capped bytes here (a cache filled under a smaller cap):
				// keep them pending, so a full copy below repairs on the way back.
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

// GetClaimHeights resolves heights through the stack's own GetClaims, the one
// place layer traversal lives, so DefaultGetClaimHeights keeps it unduplicated.
func (s *stack) GetClaimHeights(ctx context.Context, ids []ranke.Id) ([]uint64, error) {
	return ranke.DefaultGetClaimHeights(ctx, s, ids)
}

// ClaimsInBranches asks the topmost layer that indexes branch membership, so a
// graph-native cache answers it in one query.
func (s *stack) ClaimsInBranches(ctx context.Context, branches map[string]ranke.Id, ids []ranke.Id) ([]bool, error) {
	for _, l := range s.layers {
		if l.u.Capabilities().Tags {
			return l.u.ClaimsInBranches(ctx, branches, ids)
		}
	}
	return ranke.DefaultClaimsInBranches(ctx, s, branches, ids)
}

// GetClaimsRaw returns the stored CBOR from the topmost layer holding the bytes.
// Verification and replication use it, off the hot path, so it only fetches.
func (s *stack) GetClaimsRaw(ctx context.Context, ids []ranke.Id) ([][]byte, error) {
	var lastErr error = ranke.ErrNotFound
	for li := range s.layers {
		// Capability-aware descent: only a layer keeping verbatim canonical
		// bytes can serve raw claims, so the rest cost no round trip.
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

// GetClaimTags reads from the first (top-down) tag-holding layer. Tags are a
// per-claim runtime overlay one layer owns, so a stack without one is unsupported.
func (s *stack) GetClaimTags(ctx context.Context, claims []ranke.Id) ([]map[string]string, error) {
	for li := range s.layers {
		if !s.layers[li].caps.Tags {
			continue
		}
		return s.layers[li].u.GetClaimTags(ctx, claims)
	}
	return nil, ranke.ErrUnsupported
}

// SetClaimsTags writes to every tag-holding layer, keeping them consistent.
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

// Tag broadcasts the signal to every layer reporting Tags, so each updates its
// accelerators its own way. One failure fails the call: a half-tagged stack lies.
func (s *stack) Tag(ctx context.Context, head ranke.Id) error {
	for li := range s.layers {
		if !s.layers[li].caps.Tags {
			continue
		}
		if err := s.layers[li].u.Tag(ctx, head); err != nil {
			return err
		}
	}
	return nil
}

// Capabilities derives the stack's from its layers: Overwrite needs every
// synchronous layer and Delete every layer, while the rest need one layer to
// have it (ContentCap the largest, or 0 for an unbounded one). Tier is authoritative.
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

// Sync tells the topmost eager layer to sync itself from the layers below it,
// passed as the source; without one the authoritative tier already serves reads.
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
	s.heal.close() // drains the filler while its target layers are still open
	var errs []error
	for _, l := range s.layers {
		if err := l.u.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// fillClaims repairs the layers above the one that served, best-effort.
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
// hash-verified, so any layer may fill from any other.
func (s *stack) fillContents(ctx context.Context, servedAt int, refs []ranke.ContentRef, datas [][]byte) {
	for i := 0; i < servedAt; i++ {
		l := s.layers[i]
		for j, r := range refs {
			// Each layer takes what its own cap allows — neo4j wants content for
			// full-text search, up to its threshold.
			if couldHoldContent(l.caps, uint64(len(datas[j]))) {
				s.heal.offerBlob(ctx, i, ranke.ContentBlob{Hash: r.Hash, Content: datas[j]})
			}
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
