package neo4j

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	"github.com/flocko-motion/ranke-go/adapter/storage/stack"
	"github.com/flocko-motion/ranke-go/generator"
)

// TestStackRoutesByForm asserts a stack routes a claim read by the FORM asked for.
// This cache holds materialised claims and no canonical CBOR (RawClaims false), so
// it can serve the default read but has no delta to serve; a delta read belongs to
// the byte layer beneath. Capabilities exist so the stack can make that call.
func TestStackRoutesByForm(t *testing.T) {
	cache, _ := connectTestNeo4j(t)
	byteLayer := mem.New()
	st, err := stack.NewStack(cache, byteLayer)
	require.NoError(t, err)

	m, err := generator.Generate(context.Background(), st, generator.ToyDiff(1))
	require.NoError(t, err)
	delta := m.DiffChainHead

	t.Run("materialized/from-cache", func(t *testing.T) {
		require.Equal(t, 0, servedBy(t, st, delta),
			"the default (materialised) read is what the cache is for")
	})

	t.Run("delta/from-byte-layer", func(t *testing.T) {
		require.Equal(t, 1, servedBy(t, st, delta, ranke.WithNotDiffMaterialized()),
			"the cache keeps no delta, so this must descend to the byte layer")
	})
}

// TestDeniesDeltaForm asserts the cache refuses a delta read outright rather than
// answering it with materialised data. Unsupported, not a miss: the stack routes by
// capability, so an ask that reaches here is a caller's bug and must be loud.
func TestDeniesDeltaForm(t *testing.T) {
	cache, _ := connectTestNeo4j(t)
	st, err := stack.NewStack(cache, mem.New())
	require.NoError(t, err)
	m, err := generator.Generate(context.Background(), st, generator.ToyDiff(1))
	require.NoError(t, err)

	_, err = cache.GetClaims(context.Background(), []ranke.Id{m.DiffChainHead}, ranke.WithNotDiffMaterialized())
	require.ErrorIs(t, err, ranke.ErrUnsupported,
		"a materialised-only layer must reject a delta read, not answer it")
}

// servedBy reads one claim through the stack and returns the index of the layer
// that answered, read off the stack's own routing events.
func servedBy(t *testing.T, u ranke.Universe, id ranke.Id, opts ...ranke.GetOption) int {
	t.Helper()
	ctx, events := ranke.WithReport(context.Background(), ranke.ReportDebug)
	_, err := u.GetClaims(ctx, []ranke.Id{id}, opts...)
	require.NoError(t, err)

	for _, e := range events() {
		if e.Engine == "stack" && e.Op == "route" {
			layer, ok := e.Attrs["layer"].(int)
			require.True(t, ok, "route event must name the layer it hit")
			return layer
		}
	}
	t.Fatal("the stack logged no routing event")
	return -1
}
