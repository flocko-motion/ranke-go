package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Correctness tests for the RQL reference executor (DefaultQuery), covering the
// gaps beyond query_test.go: boolean Where trees, every comparison operator,
// path node/edge exclusion, overflow policies, and ordering. Assertions key on
// deterministic properties only (set membership, height, type, content) — never
// on ids or created_at, since the fixture's keys are random and its timestamps
// are wall-clock.

// queryOpsFixture builds a small diverse graph in a memory Universe, rooted at
// a hub that references every other claim, so a full-closure query reaches all:
//
//	root(0, contribution/contributor)
//	s1(1, source/note "alpha")   s2(1, source/note "beta")
//	eAlice(2, entity/person)     eApples(2, entity/object)  — both derived from s1
//	hub(3, derivation/summary)   — references s1, s2, eAlice, eApples
func queryOpsFixture(t *testing.T) (Universe, map[string]Claim) {
	t.Helper()
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	s1 := srcClaim(t, root, "alpha")
	s2 := srcClaim(t, root, "beta")
	eAlice := entityClaim(t, root, "person", "Alice", s1)
	eApples := entityClaim(t, root, "object", "apples", s1)
	hub, err := NewClaim(TypeDerivation("summary"), root).
		WithInlineContent([]byte("hub")).
		WithEncoding(EncodingPlain).
		WithEdges(
			mustDerivEdge(t, s1), mustDerivEdge(t, s2),
			mustDerivEdge(t, eAlice), mustDerivEdge(t, eApples),
		).
		WithHeight(HeightOf(root, s1, s2, eAlice, eApples)).
		Sign()
	require.NoError(t, err)

	all := map[string]Claim{"root": root, "s1": s1, "s2": s2, "eAlice": eAlice, "eApples": eApples, "hub": hub}
	for _, c := range []Claim{root, s1, s2, eAlice, eApples, hub} {
		require.NoError(t, PutClaim(ctx, u, c))
	}
	return u, all
}

// testScope resolves q's scope the way an Archive would (test helper): a branch
// query confines to its root's closure, $universe is unconfined.
func testScope(q Query) Scope {
	s := Scope{Branch: q.Select.Branch}
	if q.Select.Branch != BranchUniverse {
		s.Head = q.Select.Claim
	}
	return s
}

// queryIDs runs q and returns the reached id set.
func queryIDs(t *testing.T, u Universe, q Query) map[string]bool {
	t.Helper()
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	return idSet(drain(t, rs))
}

// idsOf is the expected id set for a list of claims.
func idsOf(cs ...Claim) map[string]bool {
	m := map[string]bool{}
	for _, c := range cs {
		m[c.ID().String()] = true
	}
	return m
}

// fromHub is a full-closure query rooted at the hub, filtered by where.
func fromHub(a map[string]Claim, where *Where) Query {
	return Query{Select: Select{Branch: BranchUniverse, Claim: a["hub"].ID()}, Where: where}
}

// TestQueryWhereAnd: And narrows — height ≥ 2 AND type entity/person → eAlice
// alone (eApples is entity/object, hub is height 3 but not an entity/person).
func TestQueryWhereAnd(t *testing.T) {
	u, a := queryOpsFixture(t)
	q := fromHub(a, &Where{And: []Where{
		{Field: "height", Test: &Comparison{Ge: 2}},
		{Field: "type", Test: &Comparison{Glob: "entity/person"}},
	}})
	require.Equal(t, idsOf(a["eAlice"]), queryIDs(t, u, q))
}

// TestQueryWhereOr: Or unions — two source/note claims plus the one person.
func TestQueryWhereOr(t *testing.T) {
	u, a := queryOpsFixture(t)
	q := fromHub(a, &Where{Or: []Where{
		{Field: "type", Test: &Comparison{Eq: "source/note"}},
		{Field: "type", Test: &Comparison{Eq: "entity/person"}},
	}})
	require.Equal(t, idsOf(a["s1"], a["s2"], a["eAlice"]), queryIDs(t, u, q))
}

// TestQueryWhereNot: Not inverts — everything that is not an entity/*.
func TestQueryWhereNot(t *testing.T) {
	u, a := queryOpsFixture(t)
	q := fromHub(a, &Where{Not: &Where{Field: "type", Test: &Comparison{Glob: "entity/*"}}})
	require.Equal(t, idsOf(a["root"], a["s1"], a["s2"], a["hub"]), queryIDs(t, u, q))
}

// TestQueryCompareNe: ne excludes the matching value, keeps the rest.
func TestQueryCompareNe(t *testing.T) {
	u, a := queryOpsFixture(t)
	q := fromHub(a, &Where{Field: "type", Test: &Comparison{Ne: "source/note"}})
	require.Equal(t, idsOf(a["root"], a["eAlice"], a["eApples"], a["hub"]), queryIDs(t, u, q))
}

// TestQueryCompareOrdered exercises lt/le/gt/ge over the numeric height field.
func TestQueryCompareOrdered(t *testing.T) {
	u, a := queryOpsFixture(t)
	require.Equal(t, idsOf(a["root"]), queryIDs(t, u, fromHub(a, &Where{Field: "height", Test: &Comparison{Lt: 1}})), "lt 1")
	require.Equal(t, idsOf(a["root"], a["s1"], a["s2"]), queryIDs(t, u, fromHub(a, &Where{Field: "height", Test: &Comparison{Le: 1}})), "le 1")
	require.Equal(t, idsOf(a["hub"]), queryIDs(t, u, fromHub(a, &Where{Field: "height", Test: &Comparison{Gt: 2}})), "gt 2")
	require.Equal(t, idsOf(a["eAlice"], a["eApples"], a["hub"]), queryIDs(t, u, fromHub(a, &Where{Field: "height", Test: &Comparison{Ge: 2}})), "ge 2")
}

// TestQueryCompareIn: in matches membership in the given set.
func TestQueryCompareIn(t *testing.T) {
	u, a := queryOpsFixture(t)
	q := fromHub(a, &Where{Field: "type", Test: &Comparison{In: []any{"source/note", "derivation/summary"}}})
	require.Equal(t, idsOf(a["s1"], a["s2"], a["hub"]), queryIDs(t, u, q))
}

// TestQueryFieldAbsent: a claim lacking the tested field never matches.
func TestQueryFieldAbsent(t *testing.T) {
	u, a := queryOpsFixture(t)
	q := fromHub(a, &Where{Field: "no_such_field", Test: &Comparison{Eq: "x"}})
	require.Empty(t, queryIDs(t, u, q))
}

// TestQueryPathNodeExclude: a node pattern with a "-" exclusion keeps
// entity/person but drops entity/object across the whole closure.
func TestQueryPathNodeExclude(t *testing.T) {
	u, a := queryOpsFixture(t)
	q := Query{Select: Select{
		Branch: BranchUniverse,
		Claim:  a["hub"].ID(),
		Path:   []PathStep{{Nodes: []string{"entity/*", "-entity/object"}}},
	}}
	require.Equal(t, idsOf(a["eAlice"]), queryIDs(t, u, q))
}

// TestQueryPathEdgeExclude: excluding contribution/* edges makes the root
// (reached only via contributor edges) unreachable.
func TestQueryPathEdgeExclude(t *testing.T) {
	u, a := queryOpsFixture(t)
	q := Query{Select: Select{
		Branch: BranchUniverse,
		Claim:  a["hub"].ID(),
		Path:   []PathStep{{Edges: []string{"-contribution/*"}}},
	}}
	got := queryIDs(t, u, q)
	require.False(t, got[a["root"].ID().String()], "root is off-limits without contributor edges")
	require.Equal(t, idsOf(a["hub"], a["s1"], a["s2"], a["eAlice"], a["eApples"]), got)
}

// TestQueryOverflowOmit: content past the cap is dropped entirely.
func TestQueryOverflowOmit(t *testing.T) {
	u, a := queryOpsFixture(t)
	q := Query{
		Select: Select{Branch: BranchUniverse, Claim: a["s1"].ID()},
		Where:  &Where{Field: "type", Test: &Comparison{Glob: "source/*"}},
		Output: Output{Content: &Content{Max: 3, Overflow: OverflowOmit}},
	}
	got := drain(t, mustQuery(t, u, q))
	require.Len(t, got, 1)
	require.Nil(t, got[0].Content, "over-cap content omitted")
}

// TestQueryOverflowReference: content past the cap is replaced by its hash.
func TestQueryOverflowReference(t *testing.T) {
	u, a := queryOpsFixture(t)
	q := Query{
		Select: Select{Branch: BranchUniverse, Claim: a["s1"].ID()},
		Where:  &Where{Field: "type", Test: &Comparison{Glob: "source/*"}},
		Output: Output{Content: &Content{Max: 3, Overflow: OverflowReference}},
	}
	got := drain(t, mustQuery(t, u, q))
	require.Len(t, got, 1)
	// s1's content is inline, so it has no stored content_hash; the reference is
	// H(content) computed on demand.
	inline, err := a["s1"].Node().GetInlineContent()
	require.NoError(t, err)
	wantHash, err := HashContent(inline)
	require.NoError(t, err)
	require.Equal(t, wantHash.String(), string(got[0].Content), "over-cap inline content replaced by its H(content) reference")
}

// TestQueryOrderHeightAscending: ascending order by height puts the height-0
// root first and the height-3 hub last, with heights non-decreasing throughout.
func TestQueryOrderHeightAscending(t *testing.T) {
	u, a := queryOpsFixture(t)
	q := Query{Select: Select{Branch: BranchUniverse, Claim: a["hub"].ID()}, Order: []OrderKey{{Field: "height"}}}
	got := drain(t, mustQuery(t, u, q))
	require.Len(t, got, 6)
	require.True(t, got[0].Claim.ID().Equal(a["root"].ID()), "height-0 root first")
	require.True(t, got[len(got)-1].Claim.ID().Equal(a["hub"].ID()), "height-3 hub last")
	for i := 1; i < len(got); i++ {
		require.LessOrEqual(t, got[i-1].Claim.Node().Height(), got[i].Claim.Node().Height(), "heights non-decreasing")
	}
}

// TestQueryClosureExcludesUnrelated expresses closure membership as an RQL
// query (the replacement for the removed InClosure): claims reachable from the
// head are returned, an unrelated claim is not.
func TestQueryClosureExcludesUnrelated(t *testing.T) {
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "seed")
	ent := entityClaim(t, root, "person", "Alice", em) // → em (derivation), root (contributor)
	other := srcClaim(t, root, "unrelated")
	putClaims(t, u, root, em, ent, other)

	got := idSet(drain(t, mustQuery(t, u, Query{Select: Select{Branch: BranchUniverse, Claim: ent.ID()}})))
	require.True(t, got[ent.ID().String()] && got[em.ID().String()] && got[root.ID().String()],
		"ent, em, root are reachable from the head")
	require.False(t, got[other.ID().String()], "an unrelated claim is not in the closure")
}

// TestQueryClosureGapIsError: a dangling reference (a claim the walk needs but
// the store lacks) aborts the query — the ADT guarantee that a gap is a real
// error, not a silent omission (previously covered against DefaultInClosure).
func TestQueryClosureGapIsError(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "seed")
	ent := entityClaim(t, root, "person", "Alice", em)
	putClaims(t, u, root, ent) // em deliberately NOT stored — a gap under ent

	_, err := u.Query(ctx, Query{Select: Select{Branch: BranchUniverse, Claim: ent.ID()}}, Scope{Branch: BranchUniverse})
	require.Error(t, err, "a missing referenced claim aborts the closure walk")
}

// mustQuery runs q (no scope) and fails on error.
func mustQuery(t *testing.T, u Universe, q Query) ResultStream {
	t.Helper()
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	return rs
}
