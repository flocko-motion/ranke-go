package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the default diff materialisation
// (universe_materialize.go): DefaultMaterialize resolves a claim's
// contribution/diff overlay so GetField/content reflect the chain, while the
// stored delta (WithNotDiffMaterialized) keeps only what it restates.
// Driven over mapUniverse (the toy byte-store).

// TestDefaultMaterializeDiff: a delta that restates only one field inherits
// the predecessor's other fields AND its content once materialised; the raw
// delta carries neither.
func TestDefaultMaterializeDiff(t *testing.T) {
	ctx := context.Background()
	u := newMapUniverse()
	root := contributor(t)
	base, err := NewClaim(TypeSource("note"), root).
		WithInlineContent([]byte("base content")).
		WithField("author", "alice").
		Sign()
	require.NoError(t, err)
	delta, err := NewClaim(TypeSource("note"), root).
		WithDiff(base.ID()).
		WithField("rev", "2").
		Sign()
	require.NoError(t, err)
	u.put(root, base, delta)

	// The stored delta, un-materialised, has only its own field and no content.
	raw, err := u.GetClaims(ctx, []Id{delta.ID()}, WithNotDiffMaterialized())
	require.NoError(t, err)
	_, err = raw[0].Node().GetField("author")
	require.Error(t, err, "the raw delta does not carry the inherited field")

	// Materialise it: inherited field + own field + inherited content.
	out, err := DefaultMaterialize(ctx, u, raw)
	require.NoError(t, err)
	author, err := out[0].Node().GetField("author")
	require.NoError(t, err)
	require.Equal(t, "alice", author, "inherited from the predecessor")
	rev, err := out[0].Node().GetField("rev")
	require.NoError(t, err)
	require.Equal(t, "2", rev, "the delta's own field")
	require.Equal(t, []byte("base content"), mustInline(t, out[0].Node()),
		"content inherited from the predecessor")
}

// TestDefaultMaterializeNonDiffNoOp: a non-diff claim passes through
// untouched — DefaultMaterialize is idempotent.
func TestDefaultMaterializeNonDiffNoOp(t *testing.T) {
	ctx := context.Background()
	u := newMapUniverse()
	root := contributor(t)
	plain := srcClaim(t, root, "plain content")
	u.put(root, plain)

	raw, err := u.GetClaims(ctx, []Id{plain.ID()}, WithNotDiffMaterialized())
	require.NoError(t, err)
	out, err := DefaultMaterialize(ctx, u, raw)
	require.NoError(t, err)
	require.Equal(t, []byte("plain content"), mustInline(t, out[0].Node()),
		"a non-diff claim is unchanged")
}
