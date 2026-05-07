package ranke

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// IntegrationTest runs the full Ranke-Graph integration suite against
// the Archive returned by factory. Use it from your own _test.go to
// confirm a custom Archive backend conforms to the ADT:
//
//	func TestMyStore(t *testing.T) {
//	    dir := t.TempDir()
//	    ranke.IntegrationTest(t, func() ranke.Archive {
//	        s, err := mystore.New(dir)
//	        if err != nil { t.Fatal(err) }
//	        return s
//	    })
//	}
//
// factory is called once eagerly to obtain the initial Archive handle,
// and again at every Reset checkpoint inside a scenario. Implementors
// should make factory return:
//
//   - the same Archive instance every call, for in-memory backends —
//     Reset is then a pointer-equality no-op;
//   - a fresh handle backed by the same durable storage, for fs/S3/...
//     backends — Reset re-reads from durable state and clears caches.
//
// Every scenario sprinkles Reset calls between writes and reads. The
// suite is correct iff each Reset is observably a no-op: the handle
// changes, but values previously returned remain valid (claims,
// graphs, branches, branch entries are self-contained).
func IntegrationTest(t *testing.T, factory func() Archive) {
	t.Helper()
	t.Run("AliceEmail", func(t *testing.T) {
		runAliceEmail(t, newTestArchive(factory))
	})
	t.Run("AgentAnalyzesEmails", func(t *testing.T) {
		runAgentAnalyzes(t, newTestArchive(factory))
	})
}

// --- testArchive wrapper ---

type testArchive struct {
	Archive
	open func() Archive
}

func newTestArchive(open func() Archive) *testArchive {
	return &testArchive{Archive: open(), open: open}
}

func (s *testArchive) Reset() { s.Archive = s.open() }

// --- helpers (build canonical actors and artifacts) ---

func mkContributor(t *testing.T, id string) Contributor {
	t.Helper()
	c, err := NewClaim(ClaimConfig{
		TypeClass:     NodeContribution,
		TypeSub:       "contributor",
		EncodingClass: EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(id),
	})
	require.NoError(t, err, "mkContributor %q", id)
	view, err := c.AsContributor()
	require.NoError(t, err)
	return view
}

func mkAgent(t *testing.T, operator Contributor, name string) Contributor {
	t.Helper()
	c, err := NewClaim(ClaimConfig{
		TypeClass:     NodeContribution,
		TypeSub:       "contributor",
		EncodingClass: EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(name),
		Contributor:   operator,
	})
	require.NoError(t, err, "mkAgent %q", name)
	view, err := c.AsContributor()
	require.NoError(t, err)
	return view
}

func mkEmail(t *testing.T, contributor Contributor, from, to, content string) Claim {
	t.Helper()
	body := fmt.Sprintf("From: %s\r\nTo: %s\r\n\r\n%s", from, to, content)
	c, err := NewClaim(ClaimConfig{
		TypeClass:     NodeSource,
		TypeSub:       "email",
		EncodingClass: EncodingMessage,
		EncodingSub:   "rfc822",
		Content:       []byte(body),
		Contributor:   contributor,
	})
	require.NoError(t, err, "mkEmail")
	return c
}

func derivationSourceEdge(t *testing.T, source Claim) Edge {
	t.Helper()
	e, err := NewEdge(EdgeConfig{
		Reference: source.ID(),
		TypeClass: EdgeDerivation,
		TypeSub:   "source",
	})
	require.NoError(t, err)
	return e
}

func mkSummary(t *testing.T, contributor Contributor, source Claim, text string) Claim {
	t.Helper()
	c, err := NewClaim(ClaimConfig{
		TypeClass:     NodeDerivation,
		TypeSub:       "summary",
		EncodingClass: EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(text),
		Contributor:   contributor,
		Edges:         []Edge{derivationSourceEdge(t, source)},
	})
	require.NoError(t, err, "mkSummary")
	return c
}

func mkEntity(t *testing.T, contributor Contributor, sub, label string, source Claim) Claim {
	t.Helper()
	c, err := NewClaim(ClaimConfig{
		TypeClass:     NodeEntity,
		TypeSub:       sub,
		EncodingClass: EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(label),
		Contributor:   contributor,
		Edges:         []Edge{derivationSourceEdge(t, source)},
	})
	require.NoError(t, err, "mkEntity %s/%s", sub, label)
	return c
}

func mkRelation(t *testing.T, contributor Contributor, sub, content string, sources, froms, tos []Claim) Claim {
	t.Helper()
	edges := make([]Edge, 0, len(sources)+len(froms)+len(tos))
	for _, s := range sources {
		edges = append(edges, derivationSourceEdge(t, s))
	}
	for _, f := range froms {
		e, err := NewEdge(EdgeConfig{
			Reference:         f.ID(),
			TypeClass:         EdgeRelation,
			TypeSub:           sub,
			RelationDirection: RelationFrom,
		})
		require.NoError(t, err, "from edge for relation/%s", sub)
		edges = append(edges, e)
	}
	for _, target := range tos {
		e, err := NewEdge(EdgeConfig{
			Reference:         target.ID(),
			TypeClass:         EdgeRelation,
			TypeSub:           sub,
			RelationDirection: RelationTo,
		})
		require.NoError(t, err, "to edge for relation/%s", sub)
		edges = append(edges, e)
	}
	c, err := NewClaim(ClaimConfig{
		TypeClass:     NodeRelation,
		TypeSub:       sub,
		EncodingClass: EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(content),
		Contributor:   contributor,
		Edges:         edges,
	})
	require.NoError(t, err, "mkRelation %s", sub)
	return c
}

func mkSymmetricRelation(t *testing.T, contributor Contributor, sub, content string, sources []Claim, members ...Claim) Claim {
	t.Helper()
	edges := make([]Edge, 0, len(sources)+len(members))
	for _, s := range sources {
		edges = append(edges, derivationSourceEdge(t, s))
	}
	for _, m := range members {
		e, err := NewEdge(EdgeConfig{
			Reference:         m.ID(),
			TypeClass:         EdgeRelation,
			TypeSub:           sub,
			RelationDirection: RelationFrom,
		})
		require.NoError(t, err, "edge for symmetric relation/%s", sub)
		edges = append(edges, e)
	}
	c, err := NewClaim(ClaimConfig{
		TypeClass:     NodeRelation,
		TypeSub:       sub,
		EncodingClass: EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(content),
		Contributor:   contributor,
		Edges:         edges,
	})
	require.NoError(t, err, "mkSymmetricRelation %s", sub)
	return c
}

// commit takes a graph (single- or multi-headed) and SetBranches it
// under "main", consolidating first if needed. After commit, the
// archive is Reset — the work has been "persisted" (or no-op'd on mem)
// and the test handle is fresh.
func commit(t *testing.T, ts *testArchive, g Graph, contributor Contributor) {
	t.Helper()
	if !g.IsConsolidated() {
		t.Logf("    consolidating %d open heads", len(g.Heads()))
		head, err := g.Consolidate(contributor)
		require.NoError(t, err, "consolidate before commit")
		_, err = g.AddClaim(head)
		require.NoError(t, err, "add consolidation claim")
	}
	t.Logf("    SetBranch main → %s", g.Heads()[0].String()[:16]+"…")
	require.NoError(t, ts.SetBranch("main", g, contributor), "SetBranch main")
	t.Logf("    [Reset] drop handle, reload from backend")
	ts.Reset()
}

// fetchMain returns the current state of branch "main" as a fresh
// Graph plus the bound head id.
func fetchMain(t *testing.T, ts *testArchive) (Graph, Id) {
	t.Helper()
	require.True(t, ts.HasBranch("main"), "branch main exists after reset")
	b, err := ts.GetBranch("main")
	require.NoError(t, err, "GetBranch main")
	head := b.Latest().Head()
	g, err := ts.GetGraph(head)
	require.NoError(t, err, "GetGraph head")
	t.Logf("    fetched main → head %s (%d claims in closure)",
		head.String()[:16]+"…", countClaims(g))
	return g, head
}

// countClaims reports how many claims the graph holds (handy for
// narrative output; not part of the public API).
func countClaims(g Graph) int {
	cg, ok := g.(*graph)
	if !ok {
		return -1
	}
	return len(cg.claims)
}

// --- Scenario: AliceEmail ---

func runAliceEmail(t *testing.T, ts *testArchive) {
	t.Logf("Scenario: a single email is ingested as a source claim.")
	t.Logf("  Paper §1 — 'A file exists, attributed to Alice by its")
	t.Logf("             headers, that appears to be a copy of an email")
	t.Logf("             to Bob in which Alice claims to like apples.'")

	operator := mkContributor(t, "operator@example.com")
	t.Logf("[Stage 1] bootstrap with operator (the only no-edge claim, §4.3)")
	t.Logf("    operator id: %s", operator.ID().String()[:16]+"…")
	g := NewGraph(operator)
	commit(t, ts, g, operator)

	_, head1 := fetchMain(t, ts)
	require.True(t, head1.Equal(operator.ID()),
		"after bootstrap, head is operator")

	t.Logf("[Stage 2] add Alice's email as a source/email claim")
	g2, _ := fetchMain(t, ts)
	email := mkEmail(t, operator,
		"alice@example.com", "bob@example.com",
		"Bob, just so you know, I really do like apples.\r\n— Alice\r\n")
	t.Logf("    email id: %s", email.ID().String()[:16]+"…")
	_, err := g2.AddClaim(email)
	require.NoError(t, err)
	commit(t, ts, g2, operator)

	t.Logf("[Verify] reload, walk closure, validate")
	g3, _ := fetchMain(t, ts)
	require.True(t, g3.ContainsClaim(email.ID()), "email survived reset")
	require.True(t, g3.ContainsClaim(operator.ID()), "operator survived reset")
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

	operator := mkContributor(t, "operator@example.com")
	agent := mkAgent(t, operator, "extraction-agent-v1")
	t.Logf("[Stage 1] bootstrap operator + agent (agent's contributor is operator)")
	t.Logf("    operator id: %s", operator.ID().String()[:16]+"…")
	t.Logf("    agent    id: %s", agent.ID().String()[:16]+"…")

	g := NewGraph(operator)
	_, err := g.AddClaim(agent)
	require.NoError(t, err)
	commit(t, ts, g, operator)
	g, _ = fetchMain(t, ts)
	require.True(t, g.ContainsClaim(agent.ID()), "agent survived reset 1")

	t.Logf("[Stage 2] add two source/email claims (Alice → Bob, ingested by operator)")
	emailApples := mkEmail(t, operator,
		"alice@example.com", "bob@example.com",
		"Bob, just so you know, I really do like apples.\r\n— Alice\r\n")
	emailFamily := mkEmail(t, operator,
		"alice@example.com", "bob@example.com",
		"Bob, please tell Bob Jr. I miss him.\r\n— Alice\r\n")
	_, err = g.AddClaim(emailApples)
	require.NoError(t, err)
	_, err = g.AddClaim(emailFamily)
	require.NoError(t, err)
	commit(t, ts, g, operator)
	g, _ = fetchMain(t, ts)
	require.True(t, g.ContainsClaim(emailApples.ID()))
	require.True(t, g.ContainsClaim(emailFamily.ID()))

	t.Logf("[Stage 3] agent extracts derivations, entities, and relations")
	t.Logf("    (every derived claim cites its source via a derivation/source edge — §3.5)")
	summary := mkSummary(t, agent, emailApples, "Alice expresses preference for apples.")
	alice := mkEntity(t, agent, "person", "Alice", emailApples)
	apples := mkEntity(t, agent, "object", "apples", emailApples)
	bobSr := mkEntity(t, agent, "person", "Bob", emailApples)
	bobJr := mkEntity(t, agent, "person", "Bob Jr.", emailFamily)
	likes := mkRelation(t, agent, "likes",
		"Alice expresses preference for apples in the email.",
		[]Claim{emailApples}, []Claim{alice}, []Claim{apples})
	knows := mkRelation(t, agent, "knows",
		"Alice addresses Bob directly, implying they are acquainted.",
		[]Claim{emailApples}, []Claim{alice}, []Claim{bobSr})
	ignores := mkRelation(t, agent, "ignores",
		"Bob does not respond to Alice (inferred; low conviction).",
		[]Claim{emailApples}, []Claim{bobSr}, []Claim{alice})
	family := mkSymmetricRelation(t, agent, "family",
		"Bob Sr. and Bob Jr. share a surname; Alice's email implies kinship.",
		[]Claim{emailFamily}, bobSr, bobJr)

	for _, c := range []Claim{summary, alice, apples, bobSr, bobJr, likes, knows, ignores, family} {
		_, err := g.AddClaim(c)
		require.NoError(t, err)
	}

	t.Logf("    bob_sr id: %s", bobSr.ID().String()[:16]+"…")
	t.Logf("    bob_jr id: %s — distinct from bob_sr (different sources)",
		bobJr.ID().String()[:16]+"…")
	require.False(t, bobSr.ID().Equal(bobJr.ID()),
		"two Bob entities from different sources have distinct ids")

	commit(t, ts, g, operator)

	t.Logf("[Verify] final reload: walk closure end-to-end and validate")
	g, _ = fetchMain(t, ts)
	for _, c := range []Claim{summary, alice, apples, bobSr, bobJr, likes, knows, ignores, family} {
		require.True(t, g.ContainsClaim(c.ID()),
			"%s/%s survived final reset", c.Node().TypeClass(), c.Node().TypeSub())
	}
	require.NoError(t, g.Validate(), "full graph validates after final reset")
	t.Logf("    closure intact, integrity verified ✓")
}
