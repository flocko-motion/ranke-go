package matrix_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/tests/generator"
	"github.com/flocko-motion/ranke-go/tests/matrix"
	"github.com/flocko-motion/ranke-go/tests/rql"
)

// Exact answers on a toy graph, so a gap every backend shares still fails here.
// The matrix next door compares backends against each other.

// TestToyHeadClosure: Select.Head narrows the scope to that claim's closure. Edges
// run delta→base, so the base's closure holds the base alone and the delta's holds
// both.
func TestToyHeadClosure(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyDiff(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		delta := m.DiffChainHead
		base := diffPredecessor(t, u, delta)

		fromBase := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Head: base},
		})
		require.Contains(t, fromBase, base.String(), "the base is in its own closure")
		require.NotContains(t, fromBase, delta.String(), "the delta is outside the base's closure")

		fromDelta := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Head: delta},
		})
		require.Contains(t, fromDelta, delta.String(), "the delta is in its own closure")
		require.Contains(t, fromDelta, base.String(), "and cites the base via contribution/diff")
	})
}

// TestToyScopeArchive: $archive scopes to the branch-table header's closure, which
// covers the spine as well as the branches' content.
func TestToyScopeArchive(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyDiff(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		archive := reached(t, u, m.Head, ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}})
		require.Contains(t, archive, m.Head.String(), "$archive reaches the archive head itself")
		require.Contains(t, archive, m.DiffChainHead.String(), "and the branches' content")
	})
}

// TestToyAnchoredScanIsTheClaimsClosure: with no path, Select.Claim anchors the
// frontier (`R-QANCHOR`) and the read is that claim's outward closure (`R-QSTEPS`) —
// not the whole scope. Anchored at the diff base, the delta overlaying it is outside.
func TestToyAnchoredScanIsTheClaimsClosure(t *testing.T) {
	eachToyBackend(t, generator.ToyDiff(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		delta := m.DiffChainHead
		base := diffPredecessor(t, u, delta)

		anchored := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Claim: base},
		})
		require.Contains(t, anchored, base.String(), "the anchor is in its own closure")
		require.NotContains(t, anchored, delta.String(), "what overlays the anchor is not")
		require.NotContains(t, anchored, m.Head.String(), "nor is the archive head above it")
	})
}

// TestToyUnanchoredScanIsTheWholeScope is the control: naming no claim leaves the
// frontier the whole closure, so the same scan still returns the scope entire.
func TestToyUnanchoredScanIsTheWholeScope(t *testing.T) {
	eachToyBackend(t, generator.ToyDiff(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		delta := m.DiffChainHead
		base := diffPredecessor(t, u, delta)

		scope := reached(t, u, m.Head, ranke.Query{Select: ranke.Select{Branch: rql.Branch}})
		require.Contains(t, scope, delta.String(), "the branch's own head claim")
		require.Contains(t, scope, base.String(), "and everything it reaches")
	})
}

// TestToyAnchoredScanStillIntersectsHead: a Head narrows a read wherever it starts
// (`R-QHEAD`), so an anchor reaching past it is cut back to the intersection.
func TestToyAnchoredScanStillIntersectsHead(t *testing.T) {
	eachToyBackend(t, generator.ToyDiff(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		delta := m.DiffChainHead
		base := diffPredecessor(t, u, delta)

		both := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Head: base, Claim: delta},
		})
		require.Contains(t, both, base.String(), "what both closures hold")
		require.NotContains(t, both, delta.String(), "the anchor itself lies outside the Head's")
	})
}

// TestToyUniverseNeedsRoot: $universe scopes to a closure, so a read there must pin
// Select.Head.
func TestToyUniverseNeedsRoot(t *testing.T) {
	matrix.Each(t, matrix.FromSpec(generator.ToyDiff(1)), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		arc, err := ranke.NewArchive(context.Background(), u, m.Head)
		require.NoError(t, err)
		_, err = arc.Query(context.Background(), ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchUniverse},
		})
		require.Error(t, err, "$universe without a root must fail, not read everything")
	})
}

// reached runs q and returns the reached ids as a set of strings.
func reached(t *testing.T, u ranke.Universe, archiveHead ranke.Id, q ranke.Query) []string {
	t.Helper()
	answer, err := rql.Run(context.Background(), u, q, archiveHead)
	require.NoError(t, err)
	out := make([]string, 0, len(answer))
	for _, fp := range answer {
		out = append(out, firstField(fp))
	}
	return out
}

// firstField is the id at the head of a fingerprint.
func firstField(fp string) string {
	for i := 0; i < len(fp); i++ {
		if fp[i] == ' ' {
			return fp[:i]
		}
	}
	return fp
}

// diffPredecessor returns the claim the given delta overlays.
func diffPredecessor(t *testing.T, u ranke.Universe, delta ranke.Id) ranke.Id {
	t.Helper()
	c, err := ranke.GetClaim(context.Background(), u, delta)
	require.NoError(t, err)
	for _, e := range c.Edges(ranke.EdgeFilterType{Type: ranke.EdgeTypeDiff}) {
		return e.Reference()
	}
	t.Fatal("the toy's chain head must carry a contribution/diff edge")
	return nil
}
