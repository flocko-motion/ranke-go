package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Tests for the RQL reference executor (DefaultQuery over a Universe): forward
// and reverse traversal, filtering, ordering, limiting, and output shaping.

// queryFixture builds root(0) ← a:source(1) ← b:entity(2) in a memory
// Universe and returns it with the three claims.
func queryFixture(t *testing.T) (Universe, Contributor, Claim, Claim) {
	t.Helper()
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	a := srcClaim(t, root, "aardvark")
	b := entityClaim(t, root, "person", "b", a)
	for _, c := range []Claim{root, a, b} {
		require.NoError(t, PutClaim(ctx, u, c))
	}
	return u, root, a, b
}

func drain(t *testing.T, rs ResultStream) []QueryResult {
	t.Helper()
	var out []QueryResult
	for rs.Next() {
		out = append(out, rs.Result())
	}
	require.NoError(t, rs.Err())
	require.NoError(t, rs.Close())
	return out
}

func idSet(rs []QueryResult) map[string]bool {
	m := map[string]bool{}
	for _, r := range rs {
		m[r.Claim.ID().String()] = true
	}
	return m
}

// TestQueryFullClosure: an empty Path walks the full forward closure from the
// root — here b reaches a (derivation) and root (contributor), and a reaches root.
func TestQueryFullClosure(t *testing.T) {
	u, root, a, b := queryFixture(t)
	rs, err := u.Query(context.Background(), Query{Select: Select{Branch: BranchUniverse, Head: b.ID()}}, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	got := idSet(drain(t, rs))
	require.Equal(t, map[string]bool{
		b.ID().String(): true, a.ID().String(): true, root.ID().String(): true,
	}, got, "full closure reaches b, a, root")
}

// TestQueryPathTypedDepth: a derivation/* step of depth 1 from b, landing on
// source/* nodes, reaches only a — root (a contributor edge) is off-path.
func TestQueryPathTypedDepth(t *testing.T) {
	u, _, a, b := queryFixture(t)
	q := Query{Select: Select{
		Branch: BranchUniverse,
		Claim:  b.ID(),
		Path:   []PathStep{{Edges: []string{"derivation/*"}, Max: 1, Nodes: []string{"source/*"}}},
	}}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	got := idSet(drain(t, rs))
	require.Equal(t, map[string]bool{a.ID().String(): true}, got)
}

// TestQueryWhereType: a Where on type filters the closure to source claims.
func TestQueryWhereType(t *testing.T) {
	u, _, a, b := queryFixture(t)
	q := Query{
		Select: Select{Branch: BranchUniverse, Head: b.ID()},
		Where:  &Where{Field: "type", Test: &Comparison{Glob: "source/*"}},
	}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	got := idSet(drain(t, rs))
	require.Equal(t, map[string]bool{a.ID().String(): true}, got)
}

// TestQueryWhereHeight: height is a first-class queryable field — ge 1 drops
// the height-0 root, keeping a(1) and b(2).
func TestQueryWhereHeight(t *testing.T) {
	u, root, a, b := queryFixture(t)
	q := Query{
		Select: Select{Branch: BranchUniverse, Head: b.ID()},
		Where:  &Where{Field: "height", Test: &Comparison{Ge: 1}},
	}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	got := idSet(drain(t, rs))
	require.Equal(t, map[string]bool{a.ID().String(): true, b.ID().String(): true}, got)
	require.False(t, got[root.ID().String()], "height-0 root excluded")
}

// TestQueryOrderLimit: order by height desc and cap to one → the deepest claim.
func TestQueryOrderLimit(t *testing.T) {
	u, _, _, b := queryFixture(t)
	q := Query{
		Select: Select{Branch: BranchUniverse, Head: b.ID()},
		Order:  []OrderKey{{Field: "height", Compare: CompareNumeric, Dir: SortDesc}},
		Limit:  Limit{Results: 1},
	}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	got := drain(t, rs)
	require.Len(t, got, 1)
	require.True(t, got[0].Claim.ID().Equal(b.ID()), "highest-height claim first")
}

// TestQueryOutputContent: content is inlined up to the cap, truncated per the
// overflow policy.
func TestQueryOutputContent(t *testing.T) {
	u, _, a, _ := queryFixture(t)
	q := Query{
		Select: Select{Branch: BranchUniverse, Head: a.ID()},
		Where:  &Where{Field: "id", Test: &Comparison{Eq: a.ID().String()}},
		Output: Output{Content: &Content{Max: 4, Overflow: OverflowCutoff}},
	}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	got := drain(t, rs)
	require.Len(t, got, 1)
	require.Equal(t, []byte("aard"), got[0].Content, "content cut off at the cap")
}

// TestQueryOutputPath: ShapePath returns the route root→claim.
func TestQueryOutputPath(t *testing.T) {
	u, _, a, b := queryFixture(t)
	q := Query{
		Select: Select{Branch: BranchUniverse, Claim: b.ID(), Path: []PathStep{{Edges: []string{"derivation/*"}, Max: 1, Nodes: []string{"source/*"}}}},
		Output: Output{Shape: ShapePath},
	}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err)
	got := drain(t, rs)
	require.Len(t, got, 1)
	require.Len(t, got[0].Path, 2, "route is b → a")
	require.True(t, got[0].Path[0].ID().Equal(b.ID()))
	require.True(t, got[0].Path[1].ID().Equal(a.ID()))
}

// TestQueryReverseSupported: a byte store serves a reverse step via closure
// inversion (not a refusal) — DirConnections from the head reaches its whole
// closure, both directions, confined to it.
func TestQueryReverseSupported(t *testing.T) {
	u, root, a, b := queryFixture(t)
	q := Query{Select: Select{Branch: BranchUniverse, Claim: b.ID(),
		Path: []PathStep{{Min: Hops(0), Dir: DirConnections}}}}
	rs, err := u.Query(context.Background(), q, testScope(q))
	require.NoError(t, err, "reverse walk is served via closure inversion, not refused")
	require.Equal(t, idsOf(a, b, root), idSet(drain(t, rs)))
}

// TestQueryNoScopeAnchor: this walker enumerates a scope by closing over an
// anchor, so a branch read reaching it without one — Select.Head unset and the
// Scope unresolved — cannot be served. Branch resolution is upstream.
func TestQueryNoScopeAnchor(t *testing.T) {
	u := NewMemoryUniverse()
	_, err := u.Query(context.Background(), Query{Select: Select{Branch: "main"}}, Scope{})
	require.ErrorIs(t, err, ErrQueryNoHead)
}

// TestQueryReport: Execution.Report appends a report after the stream drains.
func TestQueryReport(t *testing.T) {
	u, _, _, b := queryFixture(t)
	rs, err := u.Query(context.Background(), Query{
		Select:    Select{Branch: BranchUniverse, Head: b.ID()},
		Execution: Execution{Report: ReportInfo},
	}, Scope{Branch: BranchUniverse})
	require.NoError(t, err)
	got := drain(t, rs)
	rep := rs.Report()
	require.NotNil(t, rep)
	require.Equal(t, len(got), rep.Results)
	require.NotEmpty(t, rep.Events, "the report carries an execution log")
	// The native engine logged at least one event, and the log ends with results.
	var sawNative bool
	for _, e := range rep.Events {
		if e.Engine == "native" {
			sawNative = true
		}
	}
	require.True(t, sawNative, "native engine events are recorded")
}
