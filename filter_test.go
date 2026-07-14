package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for edge filters — the predicates Claim.Edges
// applies (AND across all filters). They run against richClaim, a shared
// fixture with a deliberately diverse edge set. As new filters land (by
// class, by relation direction, by reference, …) add cases here and grow
// richClaim rather than spinning up one-off claims per test.

func mustEdge(t *testing.T, cfg EdgeConfig) Edge {
	t.Helper()
	e, err := NewEdge(cfg)
	require.NoError(t, err)
	return e
}

// richClaim is a relation/likes claim carrying a varied edge set:
//
//	contribution/contributor              (auto) → root
//	derivation/source     role=primary, weight=high → src
//	derivation/summary    role=context               → src
//	relation/likes        RelationFrom               → Alice
//	relation/likes        RelationTo                 → apples
//
// Five edges spanning three classes, two derivation subtypes, two relation
// directions, and three distinct field/value pairs — enough material to
// exercise type, field, and combined (AND) filters, and to grow as more
// filter kinds arrive.
func richClaim(t *testing.T) Claim {
	t.Helper()
	root := contributor(t)
	src := srcClaim(t, root, "the source document")
	alice := entityClaim(t, root, "person", "Alice", src)
	apples := entityClaim(t, root, "object", "apples", src)

	rel, err := NewClaim(TypeRelation("likes"), root).
		WithInlineContent([]byte("Alice likes apples")).
		WithEdges(
			mustEdge(t, EdgeConfig{
				Reference: src.ID(),
				Type:      TypeDerivation("source"),
				Fields:    map[string]string{"role": "primary", "weight": "high"},
			}),
			mustEdge(t, EdgeConfig{
				Reference: src.ID(),
				Type:      "derivation/summary",
				Fields:    map[string]string{"role": "context"},
			}),
			mustEdge(t, EdgeConfig{
				Reference:         alice.ID(),
				Type:              "relation/likes",
				RelationDirection: RelationFrom,
			}),
			mustEdge(t, EdgeConfig{
				Reference:         apples.ID(),
				Type:              "relation/likes",
				RelationDirection: RelationTo,
			}),
		).
		Sign()
	require.NoError(t, err)
	return rel
}

// TestFilterFixtureShape guards the fixture itself: if richClaim changes,
// the edge count is asserted here so downstream expectations stay honest.
func TestFilterFixtureShape(t *testing.T) {
	require.Len(t, richClaim(t).Edges(), 5, "contributor + 2 derivation + 2 relation edges")
}

// TestEdgeFilterType: EdgeFilterType selects by exact "class/sub".
func TestEdgeFilterType(t *testing.T) {
	c := richClaim(t)
	require.Len(t, c.Edges(EdgeFilterType{Type: EdgeTypeContributor}), 1)
	require.Len(t, c.Edges(EdgeFilterType{Type: "derivation/source"}), 1)
	require.Len(t, c.Edges(EdgeFilterType{Type: "derivation/summary"}), 1)
	require.Len(t, c.Edges(EdgeFilterType{Type: "relation/likes"}), 2, "from + to share a type")
	require.Empty(t, c.Edges(EdgeFilterType{Type: "relation/knows"}), "no such edge")
}

// TestEdgeFilterFieldValue: matches an edge whose field equals the value;
// an edge lacking the field never matches.
func TestEdgeFilterFieldValue(t *testing.T) {
	c := richClaim(t)
	require.Len(t, c.Edges(EdgeFilterFieldValue{Field: "role", Value: "primary"}), 1)
	require.Len(t, c.Edges(EdgeFilterFieldValue{Field: "role", Value: "context"}), 1)
	require.Len(t, c.Edges(EdgeFilterFieldValue{Field: "weight", Value: "high"}), 1)
	require.Empty(t, c.Edges(EdgeFilterFieldValue{Field: "role", Value: "secondary"}), "wrong value")
	require.Empty(t, c.Edges(EdgeFilterFieldValue{Field: "absent", Value: "x"}), "field not present")
}

// TestFilterAND: multiple filters must all match (intersection).
func TestFilterAND(t *testing.T) {
	c := richClaim(t)
	require.Len(t, c.Edges(
		EdgeFilterType{Type: "derivation/source"},
		EdgeFilterFieldValue{Field: "role", Value: "primary"},
	), 1, "the one derivation/source edge with role=primary")
	require.Len(t, c.Edges(
		EdgeFilterType{Type: "derivation/source"},
		EdgeFilterFieldValue{Field: "weight", Value: "high"},
	), 1)
	require.Empty(t, c.Edges(
		EdgeFilterType{Type: "relation/likes"},                // relation edges...
		EdgeFilterFieldValue{Field: "role", Value: "primary"}, // ...carry no role field
	), "AND with a non-matching filter yields nothing")
}

// TestFilterNoFilters: Edges() with no filters returns the whole set.
func TestFilterNoFilters(t *testing.T) {
	require.Len(t, richClaim(t).Edges(), 5)
}

// TestFilterNodeSemantics: edge filters match any node (they are edge-only)
// and report themselves as edge filters.
func TestFilterNodeSemantics(t *testing.T) {
	for _, f := range []Filter{
		EdgeFilterType{Type: "x/y"},
		EdgeFilterFieldValue{Field: "a", Value: "b"},
	} {
		require.True(t, f.IsEdgeFilter())
		require.True(t, f.MatchNode(nil), "edge filters are transparent to nodes")
	}
}
