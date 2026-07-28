package matrix_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/tests/generator"
	"github.com/flocko-motion/ranke-go/tests/rql"
)

// PathStep.Min is the hop floor. The toy wires two of its three entities with one
// relation, so the floor decides whether the unwired one comes back.

// TestHopsMinDefaultWalksAnEdge: with Min unset a step walks at least one edge, so
// only the entities the relation reaches come back.
func TestHopsMinDefaultWalksAnEdge(t *testing.T) {
	eachToyBackend(t, generator.ToyUnwiredEntity(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		got := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Claim: m.Relations[0],
				Path: []ranke.PathStep{{Edges: []string{"relation/*"}, Nodes: []string{"entity/person"}}}},
		})
		require.Len(t, got, 2, "the relation wires two of the three entities")
		require.NotContains(t, got, unwired(m).String(), "the unwired entity is not reached by an edge")
	})
}

// TestHopsMinZeroAdmitsTheStart: Hops(0) lets a step match its own start, so a
// walk from an entity returns that entity.
func TestHopsMinZeroAdmitsTheStart(t *testing.T) {
	eachToyBackend(t, generator.ToyUnwiredEntity(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		start := unwired(m)
		got := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Claim: start,
				Path: []ranke.PathStep{{Min: ranke.Hops(0), Edges: []string{"relation/*"},
					Nodes: []string{"entity/person"}}}},
		})
		require.Equal(t, []string{start.String()}, got,
			"Hops(0) admits the start, and no relation edge leaves it")
	})
}

// TestHopsMinExcludesTheStart: the same walk with the default floor returns nothing,
// since the start is the only entity in reach and one hop is required.
func TestHopsMinExcludesTheStart(t *testing.T) {
	eachToyBackend(t, generator.ToyUnwiredEntity(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		got := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Claim: unwired(m),
				Path: []ranke.PathStep{{Edges: []string{"relation/*"}, Nodes: []string{"entity/person"}}}},
		})
		require.Empty(t, got, "one hop is required and no relation edge leaves the start")
	})
}

// TestHopsMinAboveMax: a floor above the ceiling admits nothing.
func TestHopsMinAboveMax(t *testing.T) {
	eachToyBackend(t, generator.ToyUnwiredEntity(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		got := reached(t, u, m.Head, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch, Claim: m.Relations[0],
				Path: []ranke.PathStep{{Min: ranke.Hops(3), Max: 1, Edges: []string{"relation/*"}}}},
		})
		require.Empty(t, got, "no hop count satisfies both bounds")
	})
}

// unwired is the entity the toy's single relation leaves alone: the relation wires
// the first two, so the last one is free.
func unwired(m *generator.Manifest) ranke.Id {
	return m.Entities[len(m.Entities)-1]
}
