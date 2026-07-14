// package: ranke / universe_materialize
// type:    logic
// job:     DefaultMaterialize — the fallback diff-overlay a Universe applies to its raw claims (resolve the contribution/diff chain into merged field/edge views)
// limits:  called only by Universe implementations (byte stores delegate here; a graph-native backend may materialise + cache itself); read-path callers do not call it — they trust the Universe to return materialised claims
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
		c, ok := cl.(*claim)
		if !ok {
			continue // foreign implementation — leave as-is
		}
		if err := materializeOne(ctx, u, c); err != nil {
			return nil, err
		}
	}
	return claims, nil
}

// materializeOne resolves one claim's contribution/diff predecessor (if
// any) and computes its merged field/edge views. The delta (node.fields,
// c.edges) is untouched, so ID()/Encode() stay the claim's own bytes.
func materializeOne(ctx context.Context, u Universe, c *claim) error {
	if c.diffClaim != nil {
		return nil // already materialised
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
	pc, ok := p.(*claim)
	if !ok {
		return errForeignClaim
	}
	c.diffClaim = pc
	c.node.diffNode = pc.node
	c.node.computeDiffFields()
	c.computeDiffEdges()
	return nil
}
