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

	t.Run("ToyExternalContent", func(t *testing.T) {
		u := ranke.NewMemoryUniverse()
		m, err := Generate(ctx, u, ToyExternalContent(1))
		require.NoError(t, err)
		require.Len(t, m.ExternalBlobs, 1, "must produce one external-content source")
	})
}
