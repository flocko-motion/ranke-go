package tests

import (
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

// Scenarios — multi-stage stories from the paper that build a graph,
// commit it through SetBranch, Reset the archive, and verify the
// state survived. Each Reset is observably a no-op for an honest
// backend: handles change, ids stay stable, closures still validate.

// commit takes a graph (single- or multi-headed) and SetBranches it
// under "main", consolidating first if needed. After commit, the
// archive is Reset — the work has been "persisted" (or no-op'd on
// mem) and the test handle is fresh.
func commit(t *testing.T, ts *testArchive, g ranke.Graph, contributor ranke.Contributor) {
	t.Helper()
	if !g.IsConsolidated() {
		t.Logf("    consolidating %d open heads", len(g.Heads()))
		must(g.Consolidate(contributor))
	}
	t.Logf("    SetBranch main → %s", g.Heads()[0].String()[:16]+"…")
	require.NoError(t, ts.SetBranch("main", g, contributor), "SetBranch main")
	t.Logf("    [Reset] drop handle, reload from backend")
	ts.Reset()
}

// fetchMain returns the current state of branch "main" as a fresh
// Graph plus the bound head id.
func fetchMain(t *testing.T, ts *testArchive) (ranke.Graph, ranke.Id) {
	t.Helper()
	require.True(t, ts.HasBranch("main"), "branch main exists after reset")
	b := must(ts.GetBranch("main"))
	head := b.Latest().Head()
	g := must(ts.GetGraph(head))
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

// relation builds a relation/<sub> claim with derivation/source
// edges per source and relation/<sub> edges per from/to entity.
func relation(t *testing.T, ctr ranke.Contributor, sub, text string, sources, froms, tos []ranke.Claim) ranke.Claim {
	t.Helper()
	edges := make([]ranke.Edge, 0, len(sources)+len(froms)+len(tos))
	for _, s := range sources {
		edges = append(edges, derivSourceEdge(t, s))
	}
	for _, f := range froms {
		edges = append(edges, must(ranke.NewEdge(ranke.EdgeConfig{
			Reference:         f.ID(),
			Type:              ranke.TypeRelation(sub),
			RelationDirection: ranke.RelationFrom,
		})))
	}
	for _, x := range tos {
		edges = append(edges, must(ranke.NewEdge(ranke.EdgeConfig{
			Reference:         x.ID(),
			Type:              ranke.TypeRelation(sub),
			RelationDirection: ranke.RelationTo,
		})))
	}
	return must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation(sub),
		Content:     []byte(text),
		Contributor: ctr,
		Edges:       edges,
	}.Sign())
}

// symRelation builds a relation/<sub> claim where every member is
// RelationFrom — symmetric (§4.7).
func symRelation(t *testing.T, ctr ranke.Contributor, sub, text string, sources []ranke.Claim, members ...ranke.Claim) ranke.Claim {
	t.Helper()
	edges := make([]ranke.Edge, 0, len(sources)+len(members))
	for _, s := range sources {
		edges = append(edges, derivSourceEdge(t, s))
	}
	for _, m := range members {
		edges = append(edges, must(ranke.NewEdge(ranke.EdgeConfig{
			Reference:         m.ID(),
			Type:              ranke.TypeRelation(sub),
			RelationDirection: ranke.RelationFrom,
		})))
	}
	return must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation(sub),
		Content:     []byte(text),
		Contributor: ctr,
		Edges:       edges,
	}.Sign())
}

// --- Scenario: AliceEmail ---

func runAliceEmail(t *testing.T, ts *testArchive) {
	t.Logf("Scenario: a single email is ingested as a source claim.")
	t.Logf("  Paper §1 — 'A file exists, attributed to Alice by its")
	t.Logf("             headers, that appears to be a copy of an email")
	t.Logf("             to Bob in which Alice claims to like apples.'")

	operator := contributor(t, "operator@example.com")
	t.Logf("[Stage 1] bootstrap with operator (the only no-edge claim, §4.3)")
	t.Logf("    operator id: %s", operator.ID().String()[:16]+"…")
	g := ranke.NewGraph(operator)
	commit(t, ts, g, operator)

	// Branch points at a contribution/head claim that wraps the
	// graph's open head (operator), per §4.6 — not at operator itself.
	g0, _ := fetchMain(t, ts)
	require.True(t, g0.Contains(operator.ID()),
		"operator reachable through new branch's head closure")

	t.Logf("[Stage 2] add Alice's email as a source/email claim")
	g2, _ := fetchMain(t, ts)
	em := email(t, operator,
		"alice@example.com", "bob@example.com",
		"Bob, just so you know, I really do like apples.\r\n— Alice\r\n")
	t.Logf("    email id: %s", em.ID().String()[:16]+"…")
	must(g2.Add(em))
	commit(t, ts, g2, operator)

	t.Logf("[Verify] reload, walk closure, validate")
	g3, _ := fetchMain(t, ts)
	require.True(t, g3.Contains(em.ID()), "email survived reset")
	require.True(t, g3.Contains(operator.ID()), "operator survived reset")
	require.NoError(t, g3.Validate(), "graph validates after reload")

	t.Logf("[Verify] mid-fetch reload: drop handle, refetch, head id must be stable")
	ts.Reset()
	g4, _ := fetchMain(t, ts)
	require.True(t, g4.Heads()[0].Equal(g3.Heads()[0]), "head id stable across reset")
}

// --- Scenario: AgentAnalyzesEmails ---

func runAgentAnalyzes(t *testing.T, ts *testArchive) {
	t.Logf("Scenario: an agent contributor analyzes two emails, extracting")
	t.Logf("  derivations, entities, and semantic relations (paper §3.5,")
	t.Logf("  §4.7). Two Bobs from two sources get distinct ids by")
	t.Logf("  content-addressing; the structural reading stays acyclic")
	t.Logf("  even though Alice—knows→Bob + Bob—ignores→Alice would")
	t.Logf("  cycle in the semantic reading.")

	operator := contributor(t, "operator@example.com")
	ag := agent(t, operator, "extraction-agent-v1")
	t.Logf("[Stage 1] bootstrap operator + agent (agent's contributor is operator)")
	t.Logf("    operator id: %s", operator.ID().String()[:16]+"…")
	t.Logf("    agent    id: %s", ag.ID().String()[:16]+"…")

	g := ranke.NewGraph(operator)
	must(g.Add(ag))
	commit(t, ts, g, operator)
	g, _ = fetchMain(t, ts)
	require.True(t, g.Contains(ag.ID()), "agent survived reset 1")

	t.Logf("[Stage 2] add two source/email claims (Alice → Bob, ingested by operator)")
	emailApples := email(t, operator,
		"alice@example.com", "bob@example.com",
		"Bob, just so you know, I really do like apples.\r\n— Alice\r\n")
	emailFamily := email(t, operator,
		"alice@example.com", "bob@example.com",
		"Bob, please tell Bob Jr. I miss him.\r\n— Alice\r\n")
	must(g.Add(emailApples))
	must(g.Add(emailFamily))
	commit(t, ts, g, operator)
	g, _ = fetchMain(t, ts)
	require.True(t, g.Contains(emailApples.ID()))
	require.True(t, g.Contains(emailFamily.ID()))

	t.Logf("[Stage 3] agent extracts derivations, entities, and relations")
	t.Logf("    (every derived claim cites its source via a derivation/source edge — §3.5)")
	summary := must(ranke.ClaimBuilder{
		Type:        ranke.TypeDerivation("summary"),
		Content:     []byte("Alice expresses preference for apples."),
		Contributor: ag,
		Edges:       []ranke.Edge{derivSourceEdge(t, emailApples)},
	}.Sign())
	alice := entity(t, ag, "person", "Alice", emailApples)
	apples := entity(t, ag, "object", "apples", emailApples)
	bobSr := entity(t, ag, "person", "Bob", emailApples)
	bobJr := entity(t, ag, "person", "Bob Jr.", emailFamily)
	likes := relation(t, ag, "likes",
		"Alice expresses preference for apples in the email.",
		[]ranke.Claim{emailApples}, []ranke.Claim{alice}, []ranke.Claim{apples})
	knows := relation(t, ag, "knows",
		"Alice addresses Bob directly, implying they are acquainted.",
		[]ranke.Claim{emailApples}, []ranke.Claim{alice}, []ranke.Claim{bobSr})
	ignores := relation(t, ag, "ignores",
		"Bob does not respond to Alice (inferred; low conviction).",
		[]ranke.Claim{emailApples}, []ranke.Claim{bobSr}, []ranke.Claim{alice})
	family := symRelation(t, ag, "family",
		"Bob Sr. and Bob Jr. share a surname; Alice's email implies kinship.",
		[]ranke.Claim{emailFamily}, bobSr, bobJr)

	for _, c := range []ranke.Claim{summary, alice, apples, bobSr, bobJr, likes, knows, ignores, family} {
		must(g.Add(c))
	}

	t.Logf("    bob_sr id: %s", bobSr.ID().String()[:16]+"…")
	t.Logf("    bob_jr id: %s — distinct from bob_sr (different sources)",
		bobJr.ID().String()[:16]+"…")
	require.False(t, bobSr.ID().Equal(bobJr.ID()),
		"two Bob entities from different sources have distinct ids")

	commit(t, ts, g, operator)

	t.Logf("[Verify] final reload: walk closure end-to-end and validate")
	g, _ = fetchMain(t, ts)
	for _, c := range []ranke.Claim{summary, alice, apples, bobSr, bobJr, likes, knows, ignores, family} {
		require.True(t, g.Contains(c.ID()),
			"%s/%s survived final reset", c.Node().TypeClass(), c.Node().TypeSub())
	}
	require.NoError(t, g.Validate(), "full graph validates after final reset")
	t.Logf("    closure intact, integrity verified ✓")
}
