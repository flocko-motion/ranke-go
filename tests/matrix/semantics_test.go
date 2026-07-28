package matrix_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/tests/backends"
	"github.com/flocko-motion/ranke-go/tests/generator"
	"github.com/flocko-motion/ranke-go/tests/rql"
)

// Exact answers on a toy graph, so a gap every backend shares still fails here.
// The matrix next door compares backends against each other.

// TestToyHeadClosure: Select.Head narrows the scope to that claim's closure. Edges
// run delta→base, so the base's closure holds the base alone and the delta's holds
// both.
func TestToyHeadClosure(t *testing.T) {
	eachToyBackend(t, generator.ToyDiff(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
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
	eachToyBackend(t, generator.ToyDiff(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		archive := reached(t, u, m.Head, ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}})
		require.Contains(t, archive, m.Head.String(), "$archive reaches the archive head itself")
		require.Contains(t, archive, m.DiffChainHead.String(), "and the branches' content")
	})
}

// TestToyUniverseNeedsRoot: $universe scopes to a closure, so a read there must pin
// Select.Head.
func TestToyUniverseNeedsRoot(t *testing.T) {
	eachToyBackend(t, generator.ToyDiff(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
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

// eachToyBackend builds spec into every available backend and runs check.
func eachToyBackend(t *testing.T, spec generator.Spec, check func(*testing.T, ranke.Universe, *generator.Manifest)) {
	t.Helper()
	for _, row := range backends.All() {
		t.Run(row.Name, func(t *testing.T) {
			u, cleanup, err := row.Open()
			if errors.Is(err, backends.ErrUnavailable) {
				t.Skipf("unavailable here: %v", err)
			}
			require.NoError(t, err)
			defer cleanup()

			m, err := generator.Generate(context.Background(), u, spec)
			require.NoError(t, err)
			check(t, u, m)
		})
	}
}
