package matrix_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/generator"
	"github.com/flocko-motion/ranke-go/tests/backends"
	"github.com/flocko-motion/ranke-go/tests/rql"
)

// The matrix proves backends AGREE; these prove they are RIGHT. On a toy graph the
// expected answer is small enough to state, so a gap every backend shares — and
// therefore agrees on — still fails here.

// TestToyWalkStart pins what Select.Claim means: the walk's starting point. Rooted
// at the diff chain's base, the walk reaches what the base cites and NOT the delta
// that overlays it — edges run delta→base, so a delta is never in its base's
// closure. A lowering that scans branch membership and ignores the root returns the
// delta too, and is wrong however much it agrees with another such lowering.
func TestToyWalkStart(t *testing.T) {
	eachToyBackend(t, generator.ToyDiff(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		delta := m.DiffChainHead
		base := diffPredecessor(t, u, delta)

		fromBase := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Claim: base},
		})
		require.Contains(t, fromBase, base.String(), "the root itself is reached")
		require.NotContains(t, fromBase, delta.String(),
			"the delta overlays the base, so it is NOT in the base's closure — a walk from the base must not reach it")

		fromDelta := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Claim: delta},
		})
		require.Contains(t, fromDelta, delta.String(), "the root itself is reached")
		require.Contains(t, fromDelta, base.String(),
			"the delta cites its base via contribution/diff, so a walk from the delta reaches it")
	})
}

// TestToyScopeArchive pins $archive: the branch-table header's closure, so it
// reaches the spine itself — claims no branch-confined read returns.
func TestToyScopeArchive(t *testing.T) {
	eachToyBackend(t, generator.ToyDiff(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		archive := reached(t, u, m.Head, ranke.Query{Select: ranke.Select{Branch: ranke.BranchArchive}})
		require.Contains(t, archive, m.Head.String(), "$archive reaches the archive head itself")
		require.Contains(t, archive, m.DiffChainHead.String(), "and the branches' content")
	})
}

// TestToyUniverseNeedsRoot pins that $universe has no natural head, so a read must
// pin one — an unrooted universe read is an error, not the whole store.
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
