// package: ranke / graph
// type:    logic
// job:     a Ranke-Graph handle RG ⊆ 𝒰 — stages claims into a Universe under the atomic-creation rule, tracks open heads, consolidates
// limits:  claims live in the Universe (-> universe); does not bind graphs to branches (-> archive); verification lives in verify.go
package ranke

import (
	"context"
	"time"
)

// Graph is a Ranke-Graph handle: a subset RG ⊆ 𝒰 (spec §4.5). The claims
// live in the Universe; the handle holds only the open-head frontier, so
// membership and provenance are closure(heads, 𝒰) resolved on demand. Built
// via NewGraph for fresh contributions, or NewGraphFromClosure to open at an
// existing head.
type Graph interface {
	// AddClaims stores claims in the Universe and advances the open-head
	// frontier: each added claim becomes a head, and the ids it references
	// drop out. Idempotent (PutClaim is).
	AddClaims(ctx context.Context, claims ...Claim) error
	// ContainsClaim reports whether id is reachable in this graph — in the
	// closure of any open head. A closure query to the Universe.
	ContainsClaim(ctx context.Context, id Id) (bool, error)
	// GetClaim returns the claim at id if it is reachable from any open
	// head, loaded (materialised) from the Universe; ErrNotFound otherwise.
	GetClaim(ctx context.Context, id Id) (Claim, error)
	// Heads returns the open heads — claims no other claim in the graph
	// references (§4.5). A single-headed graph is a closure (RG_h);
	// multi-headed means concurrent open heads.
	Heads() []Id
	// IsConsolidated reports len(Heads()) == 1.
	IsConsolidated() bool
	// Consolidate builds a contribution/head claim wrapping every open
	// head, adds it, and returns it. After this the graph is single-headed
	// at the new claim's id. createdAt defaults to time.Now().UTC() when
	// zero / omitted; must satisfy monotonicity (§4.3).
	Consolidate(ctx context.Context, contributor Contributor, createdAt ...time.Time) (Claim, error)
	// Verify walks the closure from every open head and verifies each claim
	// (§5.10 integrity + authenticity) against the Universe, returning a
	// live run. See verify.go for options.
	Verify(opts ...VerifyOption) VerificationRun
}

// graph is the concrete Graph: a Universe plus the open-head frontier
// (id string → Id). Nothing else is stored — membership, provenance, and
// interior-vs-head are all closure(heads, u), resolved on demand.
type graph struct {
	u     Universe
	heads map[string]Id
}

// NewGraph opens an empty graph over u — no heads yet; populate it with
// AddClaims. u may be nil, in which case an ephemeral in-memory Universe is
// used. An empty graph is a valid handle, though not yet a valid RG.
func NewGraph(ctx context.Context, u Universe) (Graph, error) {
	if u == nil {
		u = NewMemoryUniverse()
	}
	return &graph{u: u, heads: map[string]Id{}}, nil
}

// NewGraphFromClosure opens a graph at an existing head in universe. The
// closure RG_head = closure(head, 𝒰) is recovered on demand from the
// Universe — per §Closures the id alone suffices, so nothing is walked here.
func NewGraphFromClosure(ctx context.Context, head Id, universe Universe) (Graph, error) {
	if universe == nil {
		return nil, errNilUniverse
	}
	if head == nil {
		return nil, errNilID
	}
	return &graph{u: universe, heads: map[string]Id{head.String(): head}}, nil
}

// AddClaims stores each claim in the Universe and updates the open-head
// frontier: the new claim becomes a head, and every id it references drops
// out (it is now interior). Pure public API — the graph never unwraps.
func (g *graph) AddClaims(ctx context.Context, claims ...Claim) error {
	for _, cl := range claims {
		if cl == nil {
			return errNilClaim
		}
		if err := PutClaim(ctx, g.u, cl); err != nil {
			return wrapDetail(errGraphAddClaim, cl.ID().String(), err)
		}
		g.heads[cl.ID().String()] = cl.ID()
		for _, e := range cl.Edges() {
			delete(g.heads, e.Reference().String())
		}
	}
	return nil
}

// ContainsClaim reports whether id is reachable in this graph — in the
// closure of any open head. One closure query to the Universe (which decides
// how to answer it); no local state.
func (g *graph) ContainsClaim(ctx context.Context, id Id) (bool, error) {
	if id == nil {
		return false, nil
	}
	return InClosure(ctx, g.u, BranchUniverse, g.Heads(), id)
}

// GetClaim returns the claim at id if it is reachable from any open head,
// loaded (materialised) from the Universe; ErrNotFound otherwise.
func (g *graph) GetClaim(ctx context.Context, id Id) (Claim, error) {
	return GetFromClosure(ctx, g.u, BranchUniverse, g.Heads(), id)
}

func (g *graph) Heads() []Id {
	out := make([]Id, 0, len(g.heads))
	for _, id := range g.heads {
		out = append(out, id)
	}
	return out
}

func (g *graph) IsConsolidated() bool {
	return len(g.Heads()) == 1
}

func (g *graph) Consolidate(ctx context.Context, contributor Contributor, createdAt ...time.Time) (Claim, error) {
	heads := g.Heads()
	if len(heads) == 0 {
		return nil, errEmptyGraph
	}
	if len(heads) == 1 {
		// Already consolidated — consolidate(RG) = RG (§Consolidation).
		return GetClaim(ctx, g.u, heads[0])
	}
	edges := make([]Edge, 0, len(heads))
	// refs collects the claims the new head references, for its height
	// (§4.1): the consolidated open heads plus the contributor. The heads
	// live in the Universe; the contributor is in hand.
	refs := make([]Claim, 0, len(heads)+1)
	refs = append(refs, contributor)
	for _, h := range heads {
		e, err := NewEdge(EdgeConfig{Reference: h, Type: EdgeTypeHead})
		if err != nil {
			return nil, wrapDetail(errConsolidate, "build head edge", err)
		}
		edges = append(edges, e)
		hc, err := GetClaim(ctx, g.u, h)
		if err != nil {
			return nil, wrapDetail(errConsolidate, "load head for height", err)
		}
		refs = append(refs, hc)
	}
	head, err := ClaimBuilder{
		Type:        NodeHead,
		Contributor: contributor,
		Edges:       edges,
		CreatedAt:   firstNonZero(createdAt),
		Height:      HeightOf(refs...),
	}.Sign()
	if err != nil {
		return nil, err
	}
	if err := g.AddClaims(ctx, head); err != nil {
		return nil, wrapDetail(errConsolidate, "add head", err)
	}
	return head, nil
}

// firstNonZero returns the first non-zero time in ts, or the zero time.Time
// if all are zero / ts is empty. Absorbs the variadic createdAt parameter.
func firstNonZero(ts []time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

// Verify lives in verify.go (the configurable closure verifier).
