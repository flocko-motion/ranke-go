// package: ranke / universe_default
// type:    logic
// job:     the reference Default* implementations a Universe delegates to — diff materialisation, closure membership/lookup, and copy walkers — all in terms of the public Universe API
// limits:  called by Universe implementations (byte-store adapters delegate here; a graph-native backend may override with native queries); does no storage of its own
package ranke

import "context"

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
		if err := materializeOne(ctx, u, unwrapClaim(cl)); err != nil {
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

// DefaultInClosure reports whether id is in the closure of any of heads — the
// heads and every claim reachable from them by following edge references — by
// walking the graph. It is the fallback a byte-store Universe uses for
// InClosure; a graph-native backend answers with a single query instead. All
// heads seed one walk sharing a single visited set, so extra heads only add
// starting points, never re-traversal. Claims are loaded in delta form
// (WithNotDiffMaterialized) since only their edge references are needed, and
// the delta edges (including a contribution/diff edge) reach the full closure.
func DefaultInClosure(ctx context.Context, u Universe, heads []Id, id Id) (bool, error) {
	if id == nil {
		return false, errNilID
	}
	seen := map[string]struct{}{}
	queue := make([]Id, 0, len(heads))
	for _, h := range heads {
		if h != nil {
			queue = append(queue, h)
		}
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		cur := queue[0]
		queue = queue[1:]
		k := cur.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		if cur.Equal(id) {
			return true, nil
		}
		c, err := GetClaim(ctx, u, cur, WithNotDiffMaterialized())
		if err != nil {
			return false, err // a gap in the closure is a real error here
		}
		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
		}
	}
	return false, nil
}

// DefaultGetFromClosure returns the claim at id when it is in head's closure
// (materialised, like any read), else ErrNotFound. The fallback a byte-store
// Universe uses for GetFromClosure.
func DefaultGetFromClosure(ctx context.Context, u Universe, head, id Id) (Claim, error) {
	in, err := DefaultInClosure(ctx, u, []Id{head}, id)
	if err != nil {
		return nil, err
	}
	if !in {
		return nil, ErrNotFound
	}
	return GetClaim(ctx, u, id)
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

		if cfg.Content {
			if ch := c.Node().GetContentHash(); ch != nil {
				b, err := GetContent(ctx, src, ch, c.Node().GetContentSize())
				if err != nil {
					return wrapDetail(errCopyClaims, "src.GetContent "+ch.String(), err)
				}
				if err := PutContent(ctx, dst, ch, b); err != nil {
					return wrapDetail(errCopyClaims, "dst.PutContent "+ch.String(), err)
				}
				prog.BytesCopied += uint64(len(b))
			}
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
			b, err := GetContent(ctx, src, ref.Hash, ref.ContentSize)
			if err != nil {
				return wrapDetail(errCopyContents, "src.GetContent "+ref.Hash.String(), err)
			}
			if err := PutContent(ctx, dst, ref.Hash, b); err != nil {
				return wrapDetail(errCopyContents, "dst.PutContent "+ref.Hash.String(), err)
			}
			prog.BytesCopied += uint64(len(b))
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
