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

func srcClaim(t *testing.T, ctr Contributor, body string) Claim {
	t.Helper()
	c, err := NewClaim(TypeSource("note"), ctr).WithInlineContent([]byte(body)).Sign()
	require.NoError(t, err)
	return c
}

func entityClaim(t *testing.T, ctr Contributor, sub, label string, source Claim) Claim {
	t.Helper()
	c, err := NewClaim(TypeEntity(sub), ctr).
		WithInlineContent([]byte(label)).
		WithEdges(mustDerivEdge(t, source)).
		Sign()
	require.NoError(t, err)
	return c
}

// --- construction & heads ----------------------------------------------

// TestNewGraphRootIsHead: a fresh graph holds its root contributor as the
// sole open head, so it is already consolidated (§4.5).
func TestNewGraphRootIsHead(t *testing.T) {
	root := contributor(t)
	g := NewGraph(root)
	require.True(t, g.ContainsClaim(root.ID()), "root is in the graph")
	require.Equal(t, []Id{root.ID()}, g.Heads(), "root is the sole head")
	require.True(t, g.IsConsolidated())
}

// TestAddClaimTracksHeads: adding a claim only updates head tracking — the
// referenced claim (here root, via the contributor edge) drops out of Heads
// and the new claim becomes an open head. AddClaims never consolidates.
func TestAddClaimTracksHeads(t *testing.T) {
	root := contributor(t)
	g := NewGraph(root)
	em := srcClaim(t, root, "hello")

	require.NoError(t, g.AddClaims(em))
	require.True(t, g.ContainsClaim(em.ID()))
	require.Equal(t, []Id{em.ID()}, g.Heads(),
		"em references root, so root drops out and em is the open head")
}

// --- the atomic-creation rule (§4.3) -----------------------------------

// TestAddClaimUnknownReference: a claim whose edge points at something not
// yet in the graph is rejected — provenance must already be present.
func TestAddClaimUnknownReference(t *testing.T) {
	root := contributor(t)
	g := NewGraph(root)

	orphanSource := srcClaim(t, root, "not added") // built, never added
	ent := entityClaim(t, root, "person", "Alice", orphanSource)

	err := g.AddClaims(ent) // references orphanSource, which isn't in g
	require.Error(t, err, "a dangling reference violates the atomic-creation rule")
}

// TestAddClaimRootOnlyNoEdges: only the root may be edge-less; a second
// no-edge claim is rejected.
func TestAddClaimRootOnlyNoEdges(t *testing.T) {
	root := contributor(t)
	g := NewGraph(root)
	// Another bare contributor — a no-edge claim that isn't the root.
	stray := contributor(t)
	require.Error(t, g.AddClaims(stray), "a non-root no-edge claim is rejected")
}

// TestAddClaimIdempotent: adding the same claim twice is a no-op (§5.4).
func TestAddClaimIdempotent(t *testing.T) {
	root := contributor(t)
	g := NewGraph(root)
	em := srcClaim(t, root, "hello")

	require.NoError(t, g.AddClaims(em))
	require.NoError(t, g.AddClaims(em), "re-adding is a no-op, not an error")
	require.Equal(t, []Id{em.ID()}, g.Heads(), "still a single head after re-add")
}

// TestAddClaimNil: a nil claim is a usage error.
func TestAddClaimNil(t *testing.T) {
	g := NewGraph(contributor(t))
	require.Error(t, g.AddClaims(nil))
}

// --- consolidation lifecycle (§4.6) ------------------------------------

// TestGraphConsolidationLifecycle walks the whole arc: build an
// UNCONSOLIDATED graph (two independent claims over one source leave two
// open heads — AddClaims does not merge them), assert that state, then
// Consolidate and assert the graph is single-headed at a contribution/head
// claim wrapping both former heads.
func TestGraphConsolidationLifecycle(t *testing.T) {
	root := contributor(t)
	g := NewGraph(root)
	em := srcClaim(t, root, "seed")
	require.NoError(t, g.AddClaims(em))

	// Two independent interpretations of the same source → two open heads.
	e1 := entityClaim(t, root, "person", "Alice", em)
	e2 := entityClaim(t, root, "object", "apples", em)
	require.NoError(t, g.AddClaims(e1, e2))

	// Before: NOT consolidated — AddClaims left both heads open.
	require.False(t, g.IsConsolidated(), "two independent adds leave the graph multi-headed")
	require.ElementsMatch(t, []Id{e1.ID(), e2.ID()}, g.Heads(), "both are open heads")

	// Consolidate: one contribution/head claim wraps every open head.
	head, err := g.Consolidate(root)
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

// TestConsolidateAlreadyConsolidated: consolidating a single-headed graph
// is refused — there is nothing to wrap.
func TestConsolidateAlreadyConsolidated(t *testing.T) {
	root := contributor(t)
	g := NewGraph(root)
	_, err := g.Consolidate(root)
	require.Error(t, err, "a single-headed graph is already consolidated")
}

// TestConsolidateEmptyGraph: consolidating an empty graph is refused.
func TestConsolidateEmptyGraph(t *testing.T) {
	g := NewGraph(nil) // no root → empty
	require.Empty(t, g.Heads())
	_, err := g.Consolidate(contributor(t))
	require.Error(t, err)
}

// --- NewGraphFromClosure ------------------------------------------------

// TestNewGraphFromClosure: materializing a claim's closure from a Universe
// walks its edge references and pulls in the full provenance — here the head
// claim and its contributor.
func TestNewGraphFromClosure(t *testing.T) {
	ctx := context.Background()
	u := newMapUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "hello")
	u.put(root, em)

	g, err := NewGraphFromClosure(ctx, em, u)
	require.NoError(t, err)
	require.True(t, g.ContainsClaim(em.ID()), "head claim present")
	require.True(t, g.ContainsClaim(root.ID()), "contributor pulled in via the closure walk")
}
