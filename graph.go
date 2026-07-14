// package: ranke / graph
// type:    logic
// job:     a Ranke-Graph handle RG ⊆ 𝒰 — stages claims into a Universe under the atomic-creation rule, tracks open heads, consolidates
// limits:  claims live in the Universe (-> universe); does not bind graphs to branches (-> archive); verification lives in verify.go
package ranke

import (
	"context"
	"strconv"
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
	// Verify walks the closure from every open head and verifies each
	// claim (§5.10 integrity + authenticity), returning a live run. See
	// verify.go for options (depth, trusted-prune, external content, …).
	Verify(opts ...VerifyOption) VerificationRun
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
	c := unwrapClaim(root)
	if c == nil {
		return nil, errNilClaim
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
					queue = append(queue, unwrapClaim(cur.contributor))
				}
				continue
			}
			next, err := loadClaimAs(ctx, universe, e.reference)
			if err != nil {
				return nil, wrapDetail(errBuildGraph, "load "+e.reference.String(), err)
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
	return unwrapClaim(cl), nil
}

// unwrapClaim returns the concrete *claim behind c, peeling any wrapper
// (e.g. *signedContributor) via the sealed unwrap method. Returns nil only
// for a nil Claim — Claim is sealed, so there is no foreign case.
func unwrapClaim(c Claim) *claim {
	if c == nil {
		return nil
	}
	return c.unwrap()
}

func (g *graph) AddClaims(claims ...Claim) error {
	for i, cl := range claims {
		if err := g.addOne(cl); err != nil {
			return wrapDetail(errGraphAddClaim, "claim "+strconv.Itoa(i), err)
		}
	}
	return nil
}

func (g *graph) addOne(cl Claim) error {
	if cl == nil {
		return errNilClaim
	}
	c := unwrapClaim(cl)
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
			return withDetail(errUnknownRefClaim, refKey)
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
			return nil, wrapDetail(errConsolidate, "build head edge", err)
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
		return nil, wrapDetail(errConsolidate, "add head", err)
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

// Verify lives in verify.go (the configurable closure verifier).
