package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the Graph datatype (RG ⊆ 𝒰, §4.5) — in memory,
// no adapter. Pins the atomic-creation rule (§4.3), head tracking and
// consolidation (§4.6), and the §5.10 Verify walk. Persistence and
// adapter integration are a layer up (tests/); here the graph is exercised
// purely as a datatype.

// newGraph builds an in-memory Graph and, when root is non-nil, seeds it with
// that initial claim — adding the initial node is optional and done via
// AddClaims, not NewGraph. A nil Universe makes the Graph fall back to its own
// in-process memory store, so these white-box tests need nothing external.
func newGraph(t *testing.T, root Contributor) Graph {
	t.Helper()
	g, err := NewGraph(context.Background(), nil)
	require.NoError(t, err)
	if root != nil {
		require.NoError(t, g.AddClaims(context.Background(), root))
	}
	return g
}

// contains is a test convenience over the now closure-querying (ctx, error)
// ContainsClaim.
func contains(t *testing.T, g Graph, id Id) bool {
	t.Helper()
	ok, err := g.ContainsClaim(context.Background(), id)
	require.NoError(t, err)
	return ok
}

func srcClaim(t *testing.T, ctr Contributor, body string) Claim {
	t.Helper()
	c, err := NewClaim(TypeSource("note"), ctr).
		WithInlineContent([]byte(body)).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(ctr)).
		Sign()
	require.NoError(t, err)
	return c
}

func entityClaim(t *testing.T, ctr Contributor, sub, label string, source Claim) Claim {
	t.Helper()
	c, err := NewClaim(TypeEntity(sub), ctr).
		WithInlineContent([]byte(label)).
		WithEncoding(EncodingPlain).
		WithEdges(mustDerivEdge(t, source)).
		WithHeight(HeightOf(ctr, source)).
		Sign()
	require.NoError(t, err)
	return c
}

// --- construction & heads ----------------------------------------------

// TestNewGraphRootIsHead: a fresh graph holds its root contributor as the
// sole open head, so it is already consolidated (§4.5).
func TestNewGraphRootIsHead(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	require.True(t, contains(t, g, root.ID()), "root is in the graph")
	require.Equal(t, []Id{root.ID()}, g.Heads(), "root is the sole head")
	require.True(t, g.IsConsolidated())
}

// TestAddClaimTracksHeads: adding a claim only updates head tracking — the
// referenced claim (here root, via the contributor edge) drops out of Heads
// and the new claim becomes an open head. AddClaims never consolidates.
func TestAddClaimTracksHeads(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	em := srcClaim(t, root, "hello")

	require.NoError(t, g.AddClaims(context.Background(), em))
	require.True(t, contains(t, g, em.ID()))
	require.Equal(t, []Id{em.ID()}, g.Heads(),
		"em references root, so root drops out and em is the open head")
}

// --- the atomic-creation rule (§4.3) -----------------------------------

// TestAddClaimStoresWithoutValidating: the cleaned Graph is a pure frontier
// tracker over the Universe — AddClaims stores a claim and updates the open
// heads, it does NOT enforce the atomic-creation rule at add time. A claim
// referencing something not present is accepted; a dangling reference is
// caught lazily when the closure is walked (see universe_closure_test.go).
func TestAddClaimStoresWithoutValidating(t *testing.T) {
	ctx := context.Background()
	root := contributor(t)
	g := newGraph(t, root)

	orphanSource := srcClaim(t, root, "never added") // built, deliberately not added
	ent := entityClaim(t, root, "person", "Alice", orphanSource)

	require.NoError(t, g.AddClaims(ctx, ent), "AddClaims stores without validating references")
	require.True(t, contains(t, g, ent.ID()), "the added claim is an open head, reachable")
}

// TestAddClaimAllowsMultipleInitialNodes: the frontier tracker does not forbid
// several edge-less initial nodes — a multi-root graph (§Validity, the merged-
// archive case) is representable, both roots standing as open heads.
func TestAddClaimAllowsMultipleInitialNodes(t *testing.T) {
	ctx := context.Background()
	a := contributor(t)
	b := contributor(t)
	g := newGraph(t, a)
	require.NoError(t, g.AddClaims(ctx, b), "a second initial node is accepted")
	require.ElementsMatch(t, []Id{a.ID(), b.ID()}, g.Heads(), "both initial nodes are open heads")
}

// TestAddClaimIdempotent: adding the same claim twice is a no-op (§5.4).
func TestAddClaimIdempotent(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	em := srcClaim(t, root, "hello")

	require.NoError(t, g.AddClaims(context.Background(), em))
	require.NoError(t, g.AddClaims(context.Background(), em), "re-adding is a no-op, not an error")
	require.Equal(t, []Id{em.ID()}, g.Heads(), "still a single head after re-add")
}

// TestAddClaimNil: a nil claim is a usage error.
func TestAddClaimNil(t *testing.T) {
	g := newGraph(t, contributor(t))
	require.Error(t, g.AddClaims(context.Background(), nil))
}

// --- consolidation lifecycle (§4.6) ------------------------------------

// TestGraphConsolidationLifecycle walks the whole arc: build an
// UNCONSOLIDATED graph (two independent claims over one source leave two
// open heads — AddClaims does not merge them), assert that state, then
// Consolidate and assert the graph is single-headed at a contribution/head
// claim wrapping both former heads.
func TestGraphConsolidationLifecycle(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	em := srcClaim(t, root, "seed")
	require.NoError(t, g.AddClaims(context.Background(), em))

	// Two independent interpretations of the same source → two open heads.
	e1 := entityClaim(t, root, "person", "Alice", em)
	e2 := entityClaim(t, root, "object", "apples", em)
	require.NoError(t, g.AddClaims(context.Background(), e1, e2))

	// Before: NOT consolidated — AddClaims left both heads open.
	require.False(t, g.IsConsolidated(), "two independent adds leave the graph multi-headed")
	require.ElementsMatch(t, []Id{e1.ID(), e2.ID()}, g.Heads(), "both are open heads")

	// Consolidate: one contribution/head claim wraps every open head.
	head, err := g.Consolidate(context.Background(), root)
	require.NoError(t, err)
	require.Equal(t, "contribution/head", head.Node().Type())

	// After: consolidated — single head at the new claim.
	require.True(t, g.IsConsolidated(), "single head after consolidation")
	require.Equal(t, []Id{head.ID()}, g.Heads())

	refs := map[string]bool{}
	for _, e := range head.Edges() {
		if e.Type() == "contribution/head" {
			refs[e.Reference().String()] = true
		}
	}
	require.True(t, refs[e1.ID().String()] && refs[e2.ID().String()],
		"the head wraps both former open heads")
}

// TestConsolidateAlreadyConsolidated: consolidating a single-headed graph is a
// no-op — consolidate(RG) = RG (§Consolidation). It returns the existing head
// unchanged and mints no new claim.
func TestConsolidateAlreadyConsolidated(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	head, err := g.Consolidate(context.Background(), root)
	require.NoError(t, err, "a single-headed graph is already consolidated")
	require.True(t, head.ID().Equal(root.ID()), "returns the existing head, no new claim")
	require.Equal(t, []Id{root.ID()}, g.Heads(), "the graph is unchanged")
}

// TestConsolidateEmptyGraph: consolidating an empty graph is refused.
func TestConsolidateEmptyGraph(t *testing.T) {
	g := newGraph(t, nil) // no root → empty
	require.Empty(t, g.Heads())
	_, err := g.Consolidate(context.Background(), contributor(t))
	require.Error(t, err)
}

// --- NewGraphFromClosure ------------------------------------------------

// TestNewGraphFromClosure: materializing a claim's closure from a Universe
// walks its edge references and pulls in the full provenance — here the head
// claim and its contributor.
func TestNewGraphFromClosure(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "hello")
	putClaims(t, u, root, em)

	g, err := NewGraphFromClosure(ctx, em.ID(), u)
	require.NoError(t, err)
	require.True(t, contains(t, g, em.ID()), "head claim present")
	require.True(t, contains(t, g, root.ID()), "contributor pulled in via the closure walk")
}
