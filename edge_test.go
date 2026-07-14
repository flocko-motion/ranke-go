package ranke

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the Edge (§4.2, §4.7): NewEdge validation, the
// content-addressed id, and the §4.4 content surface (inline / external).

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

// --- content surface (§4.4) --------------------------------------------

// TestEdgeInlineContent: an edge carries content under the same rule as a
// node — inline bytes held on the edge, reachable without a Universe.
func TestEdgeInlineContent(t *testing.T) {
	alice := contributor(t)
	source, err := NewClaim(TypeSource("doc"), alice).WithInlineContent([]byte("src")).Sign()
	require.NoError(t, err)

	payload := []byte("edge-borne annotation")
	e, err := NewEdge(EdgeConfig{Reference: source.ID(), Type: TypeDerivation("source"), InlineContent: payload})
	require.NoError(t, err)

	require.False(t, e.IsContentExternal())
	require.Equal(t, uint64(len(payload)), e.GetContentSize())
	got, err := e.GetInlineContent()
	require.NoError(t, err)
	require.Equal(t, payload, got)

	r, err := e.GetContent(context.Background(), nil)
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, payload, b, "inline edge content streams without a Universe")
}

// TestEdgeExternalContent: the edge external path — ContentHash+ContentSize
// on EdgeConfig, streamed back through the Universe (stubUniverse lives in
// node_test.go, same package).
func TestEdgeExternalContent(t *testing.T) {
	alice := contributor(t)
	source, err := NewClaim(TypeSource("doc"), alice).WithInlineContent([]byte("src")).Sign()
	require.NoError(t, err)

	blob := []byte("external edge blob")
	hash, err := hashContent(blob)
	require.NoError(t, err)

	e, err := NewEdge(EdgeConfig{
		Reference:   source.ID(),
		Type:        TypeDerivation("source"),
		ContentHash: hash,
		ContentSize: uint64(len(blob)),
	})
	require.NoError(t, err)

	require.True(t, e.IsContentExternal())
	require.Equal(t, uint64(len(blob)), e.GetContentSize(), "edge records external size")
	_, err = e.GetInlineContent()
	require.ErrorIs(t, err, errContentExternal)

	u := newStubUniverse()
	require.NoError(t, u.PutContents(context.Background(), []ContentBlob{{Hash: hash, Content: blob}}))
	r, err := e.GetContent(context.Background(), u)
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, blob, b, "external edge bytes stream from the Universe")
}
