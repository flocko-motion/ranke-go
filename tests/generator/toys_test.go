package generator

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
)

// TestToys builds each toy and checks it exhibits the corner it is named for, so
// a toy cannot silently stop producing the thing a test relies on.
func TestToys(t *testing.T) {
	ctx := context.Background()

	t.Run("ToyDiff", func(t *testing.T) {
		u := ranke.NewMemoryUniverse()
		m, err := Generate(ctx, u, ToyDiff(1))
		require.NoError(t, err)
		require.NotNil(t, m.DiffChainHead, "must produce a diff chain")

		// The delta's content is inherited, not its own: materialised it reports
		// the base's size, in delta form it reports none.
		full, err := ranke.GetClaim(ctx, u, m.DiffChainHead)
		require.NoError(t, err)
		require.NotZero(t, full.Node().GetContentSize(), "materialised, the delta inherits content")

		bare, err := ranke.GetClaim(ctx, u, m.DiffChainHead, ranke.WithNotDiffMaterialized())
		require.NoError(t, err)
		require.Zero(t, bare.Node().GetContentSize(), "in delta form it restates none")
	})

	t.Run("ToyRelation", func(t *testing.T) {
		u := ranke.NewMemoryUniverse()
		m, err := Generate(ctx, u, ToyRelation(1))
		require.NoError(t, err)
		require.Len(t, m.Relations, 1, "must produce exactly one relation")
		require.Len(t, m.Entities, 2, "wiring two entities")
	})

	t.Run("ToyHandMadeEntity", func(t *testing.T) {
		u := ranke.NewMemoryUniverse()
		m, err := Generate(ctx, u, ToyHandMadeEntity(1))
		require.NoError(t, err)
		require.Len(t, m.HandMade, 1, "must produce one hand-made entity")

		// The corner is the ABSENCE, so assert it: a contributor edge and nothing else.
		c, err := ranke.GetClaim(ctx, u, m.HandMade[0])
		require.NoError(t, err)
		for _, e := range c.Edges() {
			require.NotEqual(t, ranke.EdgeClassDerivation, e.TypeClass(),
				"a hand-made entity cites no source")
		}
		require.Len(t, c.Edges(ranke.EdgeFilterType{Type: ranke.EdgeTypeContributor}), 1,
			"and is still attributed")
	})

	t.Run("ToyExternalContent", func(t *testing.T) {
		u := ranke.NewMemoryUniverse()
		m, err := Generate(ctx, u, ToyExternalContent(1))
		require.NoError(t, err)
		require.Len(t, m.ExternalBlobs, 1, "must produce one external-content source")
	})

	t.Run("ToyBranches", func(t *testing.T) {
		u := ranke.NewMemoryUniverse()
		m, err := Generate(ctx, u, ToyBranches(1))
		require.NoError(t, err)
		require.Len(t, m.Branches, 2, "must spread over exactly two branches")
		require.Equal(t, "main", m.Branches[0])
		require.GreaterOrEqual(t, m.Revisions, 2, "each branch takes a contribution")
	})

	t.Run("ToyDated", func(t *testing.T) {
		u := ranke.NewMemoryUniverse()
		m, err := Generate(ctx, u, ToyDated(1))
		require.NoError(t, err)
		require.Len(t, m.Sources, 2, "must produce exactly two sources")

		dated, absent := 0, 0
		for _, id := range m.Sources {
			c, err := ranke.GetClaim(ctx, u, id)
			require.NoError(t, err)
			if c.Node().Dated() != "" {
				dated++
			} else {
				absent++
			}
		}
		require.Equal(t, 1, dated, "exactly one source carries dated")
		require.Equal(t, 1, absent, "and one is left without it — a compare: temporal read has something to sort last")
	})
}
