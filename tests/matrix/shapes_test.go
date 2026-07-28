package matrix_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/tests/generator"
	"github.com/flocko-motion/ranke-go/tests/rql"
)

// One representative per read shape: scan, anchored traversal, unanchored traversal.

// TestShapeScan: a filter over a branch returns the claims matching it.
func TestShapeScan(t *testing.T) {
	eachToyBackend(t, generator.ToyDiff(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		got := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch},
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
		})
		require.ElementsMatch(t, idStrings(m.Sources), got,
			"a scan returns exactly the branch's claims matching the filter")
	})
}

// TestShapeScanRejectsPathOutput: a read without Path steps rejects Shape: path.
// A route only exists where steps state one, so backends would each invent their own.
func TestShapeScanRejectsPathOutput(t *testing.T) {
	eachToyBackend(t, generator.ToyDiff(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		arc, err := ranke.NewArchive(context.Background(), u, m.Head)
		require.NoError(t, err)
		_, err = arc.Query(context.Background(), ranke.Query{
			Select: ranke.Select{Branch: rql.Branch},
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
			Output: ranke.Output{Shape: ranke.ShapePath},
		})
		require.Error(t, err, "a pathless read has no route to return")
	})
}

// TestShapeAnchoredTraversal: from one claim, derivation edges reach the sources
// it cites.
func TestShapeAnchoredTraversal(t *testing.T) {
	eachToyBackend(t, generator.ToyDiff(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		base := diffPredecessor(t, u, m.DiffChainHead)
		got := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Claim: base,
				Path: []ranke.PathStep{{Edges: []string{"derivation/*"}, Nodes: []string{"source/*"}}}},
		})
		require.ElementsMatch(t, idStrings(m.Sources), got,
			"the derivation edges of this claim reach exactly the sources it cites")
	})
}

// TestShapeUnanchoredTraversal: a pattern without a starting point matches wherever
// it occurs in the scope — here the relation reaches both entities it wires.
func TestShapeUnanchoredTraversal(t *testing.T) {
	eachToyBackend(t, generator.ToyRelation(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		got := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch,
				Path: []ranke.PathStep{{Edges: []string{"relation/*"}, Nodes: []string{"entity/person"}}}},
		})
		require.ElementsMatch(t, idStrings(m.Entities), got,
			"the relation's edges reach both entities it wires, from wherever the pattern occurs")
	})
}

// idStrings renders ids for comparison against a reached set.
func idStrings(ids []ranke.Id) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.String())
	}
	return out
}
