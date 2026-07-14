package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for NewEdge validation (§4.2, §4.7) and edge id
// determinism. The happy inline/external paths are covered in
// content_test.go; this file pins the rejection matrix and the content-
// addressed id.

func anyRef(t *testing.T) Id {
	t.Helper()
	h, err := HashContent([]byte("some reference target"))
	require.NoError(t, err)
	return h
}

// TestNewEdgeRequiresReference: an edge must point somewhere.
func TestNewEdgeRequiresReference(t *testing.T) {
	_, err := NewEdge(EdgeConfig{Type: TypeDerivation("source")})
	require.Error(t, err)
}

// TestNewEdgeRequiresType: an edge must carry a type.
func TestNewEdgeRequiresType(t *testing.T) {
	_, err := NewEdge(EdgeConfig{Reference: anyRef(t)})
	require.Error(t, err)
}

// TestNewEdgeRejectsUnknownClass: the class half is closed vocabulary.
func TestNewEdgeRejectsUnknownClass(t *testing.T) {
	_, err := NewEdge(EdgeConfig{Reference: anyRef(t), Type: "bogus/x"})
	require.Error(t, err)
}

// TestNewEdgeContentXOR: inline and external content are mutually exclusive.
func TestNewEdgeContentXOR(t *testing.T) {
	hash, err := HashContent([]byte("bytes"))
	require.NoError(t, err)
	_, err = NewEdge(EdgeConfig{
		Reference:     anyRef(t),
		Type:          TypeDerivation("source"),
		InlineContent: []byte("bytes"),
		ContentHash:   hash,
	})
	require.ErrorIs(t, err, errEdgeContentXOR)
}

// TestNewEdgeRelationDirectionRequired: relation/* edges must carry a
// direction (§4.7).
func TestNewEdgeRelationDirectionRequired(t *testing.T) {
	_, err := NewEdge(EdgeConfig{Reference: anyRef(t), Type: "relation/likes"})
	require.Error(t, err, "a relation edge without a direction is rejected")
}

// TestNewEdgeRelationDirectionOnNonRelation: a direction on a
// non-relation edge is rejected — direction is meaningful only for
// relation/* edges.
func TestNewEdgeRelationDirectionOnNonRelation(t *testing.T) {
	_, err := NewEdge(EdgeConfig{
		Reference:         anyRef(t),
		Type:              TypeDerivation("source"),
		RelationDirection: RelationFrom,
	})
	require.Error(t, err)
}

// TestNewEdgeRelationValid: a relation edge with a direction is accepted.
func TestNewEdgeRelationValid(t *testing.T) {
	e, err := NewEdge(EdgeConfig{
		Reference:         anyRef(t),
		Type:              "relation/likes",
		RelationDirection: RelationFrom,
	})
	require.NoError(t, err)
	require.Equal(t, RelationFrom, e.RelationDirection())
}

// TestEdgeIdDeterministic: an edge's id is content-addressed — identical
// config yields an equal id; any change yields a different one.
func TestEdgeIdDeterministic(t *testing.T) {
	ref := anyRef(t)
	mk := func(sub string) Edge {
		e, err := NewEdge(EdgeConfig{Reference: ref, Type: "derivation/" + sub})
		require.NoError(t, err)
		return e
	}
	a, b := mk("source"), mk("source")
	require.True(t, a.ID().Equal(b.ID()), "identical edges share an id")

	c := mk("summary")
	require.False(t, a.ID().Equal(c.ID()), "a different type yields a different id")
}
