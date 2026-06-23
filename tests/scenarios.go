// package: tests / conformance
// type:    test
// job:     multi-stage scenario stories that build graphs, commit, reset, and verify survival
// limits:  no public entry point; driven by IntegrationTest (-> tests/integration.go)
package tests

import (
	"context"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

// Scenarios — multi-stage stories that build a graph, commit it via
// AddGraph, Reset, and verify the state survived. Each Reset is
// observably a no-op for an honest backend.
func commit(t *testing.T, ctx context.Context, ts *testArchive, g ranke.Graph, contributor ranke.Contributor) {
	t.Helper()
	require.NoError(t, ts.AddGraph(ctx, "main", g, contributor), "AddGraph main")
	ts.Reset()
}

// fetchMain returns the current state of branch "main" as a fresh
// Graph plus the bound head id.
func fetchMain(t *testing.T, ctx context.Context, ts *testArchive) (ranke.Graph, ranke.Id) {
	t.Helper()
	require.True(t, ts.HasBranch(ctx, "main"), "branch main exists after reset")
	b := must(ts.GetBranch(ctx, "main"))
	head := b.Latest().Head()
	headClaim := must(ts.GetClaim(ctx, head))
	g := must(headClaim.Graph(ctx))
	t.Logf("    fetched main → head %s (%d open heads)", head.String()[:16]+"…", len(g.Heads()))
	return g, head
}

// contributor builds a root contribution/contributor claim with id
// as content.
func contributor(t *testing.T, id string) ranke.Contributor {
	t.Helper()
	c := must(ranke.ClaimBuilder{
		Type:    ranke.NodeContributor,
		Content: []byte(id),
	}.Sign())
	return must(c.AsContributor())
}

// agent builds a contribution/contributor claim attributed to operator.
func agent(t *testing.T, operator ranke.Contributor, name string) ranke.Contributor {
	t.Helper()
	c := must(ranke.ClaimBuilder{
		Type:        ranke.NodeContributor,
		Content:     []byte(name),
		Contributor: operator,
	}.Sign())
	return must(c.AsContributor())
}

// email builds a source/email claim with the given from/to/body.
func email(t *testing.T, ctr ranke.Contributor, from, to, body string) ranke.Claim {
	t.Helper()
	return must(ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     []byte("From: " + from + "\r\nTo: " + to + "\r\n\r\n" + body),
		Contributor: ctr,
	}.Sign())
}

// derivSourceEdge builds a derivation/source edge from a derived
// claim back to source.
func derivSourceEdge(t *testing.T, source ranke.Claim) ranke.Edge {
	t.Helper()
	return must(ranke.NewEdge(ranke.EdgeConfig{
		Reference: source.ID(),
		Type:      ranke.TypeDerivation("source"),
	}))
}

// entity builds an entity/<sub> claim with label as content and a
// derivation/source edge to source.
func entity(t *testing.T, ctr ranke.Contributor, sub, label string, source ranke.Claim) ranke.Claim {
	t.Helper()
	return must(ranke.ClaimBuilder{
		Type:        ranke.TypeEntity(sub),
		Content:     []byte(label),
		Contributor: ctr,
		Edges:       []ranke.Edge{derivSourceEdge(t, source)},
	}.Sign())
}

// --- Scenario: AddClaimExtendsBranch ---
//
// arch.AddClaim is the "insert one row" convenience: it loads the
// branch's current closure, adds the claim, and commits via
// AddGraph. After two AddClaim calls the second claim should be
// present AND the first should still be reachable.

func runAddClaimExtendsBranch(t *testing.T, ctx context.Context, ts *testArchive) {
	t.Logf("Scenario: AddClaim extends a branch (insert-one-row convenience)")

	// Isolated branch so we don't trip on state from sibling tests.
	const branchName = "addclaim-extends"
	operator := contributor(t, "addclaim-test@example.com")

	emA := email(t, operator, "alice@example.com", "bob@example.com", "first\n")
	require.NoError(t, ts.AddClaim(ctx, branchName, emA), "AddClaim emA")
	ts.Reset()

	emB := email(t, operator, "alice@example.com", "bob@example.com", "second\n")
	require.NoError(t, ts.AddClaim(ctx, branchName, emB), "AddClaim emB")
	ts.Reset()

	b, err := ts.GetBranch(ctx, branchName)
	require.NoError(t, err)
	headClaim, err := ts.GetClaim(ctx, b.Latest().Head())
	require.NoError(t, err)
	g, err := headClaim.Graph(ctx)
	require.NoError(t, err)

	require.True(t, g.Contains(emA.ID()), "first claim still reachable after second AddClaim")
	require.True(t, g.Contains(emB.ID()), "second claim reachable")
	require.True(t, g.Contains(operator.ID()), "contributor reachable")
	require.NoError(t, g.Validate(), "closure validates")
	t.Logf("    ✓ both claims survived the second AddClaim")
}

// --- Scenario: AddGraphAutoConsolidates ---
//
// Recommended write workflow: open archive, load the existing
// contributor from the branch, NewGraph(existing), add claims
// freely (open heads are fine), AddGraph — the archive wraps every
// open head in a single contribution/head claim. No explicit
// Consolidate needed.

func runAddGraphAutoConsolidates(t *testing.T, ctx context.Context, ts *testArchive) {
	t.Logf("Scenario: load existing contributor from archive, append")
	t.Logf("  a multi-headed graph, AddGraph auto-consolidates.")

	// Phase 1 — bootstrap with operator.
	operator := contributor(t, "operator@example.com")
	g0 := ranke.NewGraph(operator)
	require.NoError(t, ts.AddGraph(ctx, "main", g0, operator), "bootstrap")
	ts.Reset()

	// Phase 2 — RELOAD the contributor from the archive. This is the
	// pattern: never mint a fresh contributor when one already exists
	// in the archive; load and reuse.
	b, err := ts.GetBranch(ctx, "main")
	require.NoError(t, err)
	existing := b.Latest().Contributor()
	require.NotNil(t, existing, "branch entry exposes its contributor")
	require.True(t, existing.ID().Equal(operator.ID()),
		"loaded contributor id matches the original — same identity, no new claim")
	t.Logf("    loaded contributor from archive: %s", existing.ID().String()[:16]+"…")

	// Build a fresh graph against the EXISTING contributor and add
	// two unrelated entities — both become open heads.
	g := ranke.NewGraph(existing)
	em := email(t, existing, "alice@example.com", "bob@example.com", "second\n")
	e1 := entity(t, existing, "person", "Alice", em)
	e2 := entity(t, existing, "object", "apples", em)
	must(g.Add(em))
	must(g.Add(e1))
	must(g.Add(e2))

	require.False(t, g.IsConsolidated(), "graph should be multi-headed before AddGraph")
	require.Len(t, g.Heads(), 2, "expected exactly 2 open heads")
	t.Logf("    2 open heads: %s, %s",
		e1.ID().String()[:16]+"…", e2.ID().String()[:16]+"…")

	// AddGraph directly — no Consolidate call.
	require.NoError(t, ts.AddGraph(ctx, "main", g, existing),
		"AddGraph on multi-headed graph")
	ts.Reset()

	b, err = ts.GetBranch(ctx, "main")
	require.NoError(t, err)
	headId := b.Latest().Head()
	t.Logf("    branch head: %s", headId.String()[:16]+"…")

	freshClaim, err := ts.GetClaim(ctx, headId)
	require.NoError(t, err)
	fresh, err := freshClaim.Graph(ctx)
	require.NoError(t, err)
	require.True(t, fresh.IsConsolidated(), "loaded graph should be single-headed")
	require.True(t, fresh.Heads()[0].Equal(headId), "loaded graph's head matches branch head")

	headClaim, ok := fresh.Get(headId)
	require.True(t, ok, "branch head claim is in the closure")
	require.Equal(t, "contribution/head", headClaim.Node().Type(),
		"branch head is a contribution/head claim")

	// Confirm the auto-consolidation wrapped EVERY original open head.
	refs := map[string]bool{}
	for _, e := range headClaim.Edges() {
		if e.Type() == "contribution/head" {
			refs[e.Reference().String()] = true
		}
	}
	require.True(t, refs[e1.ID().String()], "auto-consolidate edges include entity1")
	require.True(t, refs[e2.ID().String()], "auto-consolidate edges include entity2")
	t.Logf("    ✓ contribution/head wraps both original heads")
}

// --- Scenario: AliceEmail ---

func runAliceEmail(t *testing.T, ctx context.Context, ts *testArchive) {
	t.Logf("Scenario: a single email is ingested as a source claim.")
	t.Logf("  Paper §1 — 'A file exists, attributed to Alice by its")
	t.Logf("             headers, that appears to be a copy of an email")
	t.Logf("             to Bob in which Alice claims to like apples.'")

	operator := contributor(t, "operator@example.com")
	t.Logf("[Stage 1] bootstrap with operator (the only no-edge claim, §4.3)")
	t.Logf("    operator id: %s", operator.ID().String()[:16]+"…")
	g := ranke.NewGraph(operator)
	commit(t, ctx, ts, g, operator)

	// Branch points at a contribution/head claim that wraps the
	// graph's open head (operator), per §4.6 — not at operator itself.
	g0, _ := fetchMain(t, ctx, ts)
	require.True(t, g0.Contains(operator.ID()),
		"operator reachable through new branch's head closure")

	t.Logf("[Stage 2] add Alice's email as a source/email claim")
	g2, _ := fetchMain(t, ctx, ts)
	em := email(t, operator,
		"alice@example.com", "bob@example.com",
		"Bob, just so you know, I really do like apples.\r\n— Alice\r\n")
	t.Logf("    email id: %s", em.ID().String()[:16]+"…")
	must(g2.Add(em))
	commit(t, ctx, ts, g2, operator)

	t.Logf("[Verify] reload, walk closure, validate")
	g3, _ := fetchMain(t, ctx, ts)
	require.True(t, g3.Contains(em.ID()), "email survived reset")
	require.True(t, g3.Contains(operator.ID()), "operator survived reset")
	require.NoError(t, g3.Validate(), "graph validates after reload")

	t.Logf("[Verify] mid-fetch reload: drop handle, refetch, head id must be stable")
	ts.Reset()
	g4, _ := fetchMain(t, ctx, ts)
	require.True(t, g4.Heads()[0].Equal(g3.Heads()[0]), "head id stable across reset")
}
