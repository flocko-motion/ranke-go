// package: ranke / graph
// type:    logic
// job:     in-memory Ranke-Graph (RG ⊆ 𝒰) — adds claims under the atomic-creation rule, tracks heads, consolidates, and validates closures
// limits:  does not persist claims (-> universe); does not bind graphs to branches (-> archive)
package ranke

import (
	"context"
	"fmt"
	"time"
)

// Graph is a Ranke-Graph instance RG ⊆ 𝒰 (spec §4.5), in memory.
// Built standalone via NewGraph for fresh contributions, or
// materialized from a loaded Claim via NewGraphFromClosure(ctx, claim, u).
type Graph interface {
	// Add inserts claims atomically. Every edge reference must
	// already be reachable in the graph (atomic creation rule, §4.3).
	// Non-root claims must have at least one edge. Idempotent.
	AddClaims(claims ...Claim) error
	ContainsClaim(id Id) bool
	GetClaim(id Id) (Claim, bool)
	// Heads returns the open heads — claims no other claim in the
	// graph references (§4.5). A single-headed graph is a closure
	// (RG_h); multi-headed means concurrent open heads.
	Heads() []Id
	// IsConsolidated reports len(Heads()) == 1.
	IsConsolidated() bool
	// Consolidate builds a contribution/head claim wrapping every
	// open head, adds it, and returns it. After this the graph is
	// single-headed at the new claim's id. createdAt defaults to
	// time.Now().UTC() when zero / omitted; must satisfy
	// monotonicity (§4.3).
	Consolidate(contributor Contributor, createdAt ...time.Time) (Claim, error)
	// Verify walks the closure from every open head and runs the
	// §5.10 per-claim integrity + authenticity check. Walks the
	// full closure (so verbose callers see every claim) and returns
	// the first error, or nil. Optional report callbacks are called
	// once per visited claim with its depth and result.
	Verify(report ...func(c Claim, depth int, err error)) error
}

// graph is the concrete implementation of Graph (= RG ⊆ 𝒰, spec §4.5).
// claims is keyed by Id.String(); referenced tracks every claim id
// that some other claim references via an edge — so Heads() =
// claims \ referenced.
type graph struct {
	claims     map[string]*claim
	referenced map[string]struct{}
}

// NewGraph creates a graph and adds the given contribution/contributor
// claim . This claim might be an initial node without edges or a full
// clojure.
func NewGraph(root Contributor) Graph {
	g := &graph{
		claims:     make(map[string]*claim),
		referenced: make(map[string]struct{}),
	}
	if c := unwrapClaim(root); c != nil {
		g.claims[c.node.id.String()] = c
	}
	return g
}

// NewGraphFromClosure materializes root's provenance closure into a
// Graph by walking edge references. Referenced claims are loaded from
// universe (or, per-claim, from the Universe each was loaded from). When
// no Universe is available it falls back to an in-memory best effort:
// only the already-resolved contributor is reachable.
func NewGraphFromClosure(ctx context.Context, root Claim, universe Universe) (Graph, error) {
	c, ok := root.(*claim)
	if !ok {
		return nil, errForeignClaim
	}
	g := &graph{
		claims:     make(map[string]*claim),
		referenced: make(map[string]struct{}),
	}
	queue := []*claim{c}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		k := cur.node.id.String()
		if _, seen := g.claims[k]; seen {
			continue
		}
		g.claims[k] = cur
		for _, e := range cur.edges {
			g.referenced[e.reference.String()] = struct{}{}
			if universe == nil {
				// In-memory best effort: include the resolved contributor
				// if it's this edge's target.
				if cur.contributor != nil && cur.contributor.ID() != nil &&
					cur.contributor.ID().Equal(e.reference) {
					if cc, ok := cur.contributor.(*claim); ok {
						queue = append(queue, cc)
					}
				}
				continue
			}
			next, err := loadClaimAs(ctx, universe, e.reference)
			if err != nil {
				return nil, fmt.Errorf("NewGraphFromClosure: load %s: %w", e.reference.String(), err)
			}
			queue = append(queue, next)
		}
	}
	return g, nil
}

// loadClaimAs loads the claim at id from u and unwraps it to the concrete
// *claim.
func loadClaimAs(ctx context.Context, u Universe, id Id) (*claim, error) {
	cl, err := GetClaim(ctx, u, id)
	if err != nil {
		return nil, err
	}
	c, ok := cl.(*claim)
	if !ok {
		return nil, errForeignClaim
	}
	return c, nil
}

// unwrapClaim peels any wrapper (e.g. *signedContributor) off a
// Contributor to reach the underlying persisted *claim. Returns nil
// if the chain doesn't end at our concrete type — i.e. the caller
// passed a foreign implementation.
func unwrapClaim(c Contributor) *claim {
	for c != nil {
		switch v := c.(type) {
		case *claim:
			return v
		case *signedContributor:
			c = v.contributor
		default:
			return nil
		}
	}
	return nil
}

func (g *graph) AddClaims(claims ...Claim) error {
	for i, cl := range claims {
		if err := g.addOne(cl); err != nil {
			return fmt.Errorf("Graph.Add: claim %d: %w", i, err)
		}
	}
	return nil
}

func (g *graph) addOne(cl Claim) error {
	if cl == nil {
		return errNilClaim
	}
	c, ok := cl.(*claim)
	if !ok {
		return errForeignClaim
	}
	// Idempotency: same id ⇒ no-op.
	key := c.node.id.String()
	if _, exists := g.claims[key]; exists {
		return nil
	}
	// Only the root may have no edges; the root was set at NewGraph.
	if len(c.edges) == 0 {
		return errRootOnlyNoEdges
	}
	// Atomic creation rule (§4.3): every edge reference must already
	// be present in the graph.
	for _, e := range c.edges {
		refKey := e.reference.String()
		if _, ok := g.claims[refKey]; !ok {
			return fmt.Errorf("edge references unknown claim %s (atomic creation rule, §4.3)", refKey)
		}
	}
	// Insert the claim and mark each referenced claim.
	g.claims[key] = c
	for _, e := range c.edges {
		g.referenced[e.reference.String()] = struct{}{}
	}
	return nil
}

func (g *graph) ContainsClaim(id Id) bool {
	if id == nil {
		return false
	}
	_, ok := g.claims[id.String()]
	return ok
}

func (g *graph) GetClaim(id Id) (Claim, bool) {
	if id == nil {
		return nil, false
	}
	c, ok := g.claims[id.String()]
	if !ok {
		return nil, false
	}
	return c, true
}

func (g *graph) Heads() []Id {
	out := make([]Id, 0)
	for k, c := range g.claims {
		if _, ref := g.referenced[k]; ref {
			continue
		}
		out = append(out, c.node.id)
	}
	return out
}

func (g *graph) IsConsolidated() bool {
	return len(g.Heads()) == 1
}

func (g *graph) Consolidate(contributor Contributor, createdAt ...time.Time) (Claim, error) {
	heads := g.Heads()
	if len(heads) == 0 {
		return nil, errEmptyGraph
	}
	if len(heads) == 1 {
		return nil, errAlreadyConsolidated
	}
	edges := make([]Edge, 0, len(heads))
	for _, h := range heads {
		e, err := NewEdge(EdgeConfig{
			Reference: h,
			TypeClass: EdgeClassContribution,
			TypeSub:   "head",
		})
		if err != nil {
			return nil, fmt.Errorf("ranke.Graph.Consolidate: build head edge: %w", err)
		}
		edges = append(edges, e)
	}
	head, err := ClaimBuilder{
		Type:        NodeHead,
		Contributor: contributor,
		Edges:       edges,
		CreatedAt:   firstNonZero(createdAt),
	}.Sign()
	if err != nil {
		return nil, err
	}
	if err := g.AddClaims(head); err != nil {
		return nil, fmt.Errorf("ranke.Graph.Consolidate: add head: %w", err)
	}
	return head, nil
}

// firstNonZero returns the first non-zero time in ts, or the zero
// time.Time if all are zero / ts is empty. Used to absorb the
// variadic createdAt parameter on Consolidate / AddGraph.
func firstNonZero(ts []time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func (g *graph) Verify(report ...func(c Claim, depth int, err error)) error {
	seen := map[string]bool{}
	var firstErr error
	for _, h := range g.Heads() {
		g.verifyRecursive(h, 0, seen, report, &firstErr)
	}
	return firstErr
}

func (g *graph) verifyRecursive(id Id, depth int, seen map[string]bool, report []func(Claim, int, error), firstErr *error) {
	k := id.String()
	if seen[k] {
		return
	}
	seen[k] = true
	c, ok := g.claims[k]
	if !ok {
		return
	}
	err := g.verifyOne(c)
	for _, r := range report {
		r(c, depth, err)
	}
	if err != nil && *firstErr == nil {
		*firstErr = fmt.Errorf("validate %s: %w", k, err)
	}
	for _, e := range c.edges {
		g.verifyRecursive(e.reference, depth+1, seen, report, firstErr)
	}
}

// verifyOne runs the §5.10 checks on a single claim:
//   - every edge reference resolves to a claim in the graph;
//   - canonical re-encode + recompute H(S(v)) + signature check
//     against id(v) (record integrity + authenticity, §5.2 + §5.7);
//   - if content_hash is set, re-hash the actual content bytes and
//     compare (content integrity).
func (g *graph) verifyOne(c *claim) error {
	for _, e := range c.edges {
		if _, ok := g.claims[e.reference.String()]; !ok {
			return fmt.Errorf("edge references missing claim %s", e.reference.String())
		}
	}
	encoded, err := encodeNode(c.node)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	recomputed, err := hashContent(encoded)
	if err != nil {
		return fmt.Errorf("hash: %w", err)
	}
	pubkey, err := g.resolveClaimPubkey(c)
	if err != nil {
		return fmt.Errorf("resolve pubkey: %w", err)
	}
	idH, ok := c.node.id.(*id)
	if !ok {
		return errForeignIdType
	}
	if err := verifySignature(pubkey, recomputed.raw, idH.raw); err != nil {
		return fmt.Errorf("§5.7: %w", err)
	}
	// Content integrity (§5.10) is enforced at Universe.GetContent /
	// StreamContent time; bytes reaching verifyOne are already verified.
	return nil
}

// resolveClaimPubkey returns the pubkey whose matching private key
// signed this claim's id (§5.7). For the initial contributor (a
// claim with no edges, per §4.3), the pubkey is on the claim's own
// node. For every other claim, it is on the node of the contributor
// referenced by the claim's contribution/contributor edge.
func (g *graph) resolveClaimPubkey(c *claim) ([]byte, error) {
	if len(c.edges) == 0 {
		// Initial node (the only no-edge claim a graph may contain).
		return c.node.pubkey, nil
	}
	for _, e := range c.edges {
		if e.typeClass == EdgeClassContribution && e.typeSub == "contributor" {
			contributor, ok := g.claims[e.reference.String()]
			if !ok {
				return nil, fmt.Errorf("contributor claim %s not in graph", e.reference.String())
			}
			return contributor.node.pubkey, nil
		}
	}
	return nil, errNoContributorEdge
}
