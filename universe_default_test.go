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
	u := NewMemoryUniverse()
	root := contributor(t)
	base, err := NewClaim(TypeSource("note"), root).
		WithInlineContent([]byte("base content")).
		WithEncoding(EncodingPlain).
		WithField("author", "alice").
		WithHeight(HeightOf(root)).
		Sign()
	require.NoError(t, err)
	delta, err := NewClaim(TypeSource("note"), root).
		WithDiff(base.ID()).
		WithField("rev", "2").
		WithHeight(HeightOf(root, base)).
		Sign()
	require.NoError(t, err)
	putClaims(t, u, root, base, delta)

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
	u := NewMemoryUniverse()
	root := contributor(t)
	plain := srcClaim(t, root, "plain content")
	putClaims(t, u, root, plain)

	raw, err := u.GetClaims(ctx, []Id{plain.ID()}, WithNotDiffMaterialized())
	require.NoError(t, err)
	out, err := DefaultMaterialize(ctx, u, raw)
	require.NoError(t, err)
	require.Equal(t, []byte("plain content"), mustInline(t, out[0].Node()),
		"a non-diff claim is unchanged")
}

// --- GetClaimHeights (§4.1) --------------------------------------------

// TestGetClaimHeights: GetClaimHeights returns committed heights positionally —
// 0 for an initial node, 1 + max(refs) for a referencing one — and the
// single-item GetClaimHeight wrapper agrees.
func TestGetClaimHeights(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	a := srcClaim(t, root, "a") // height 1
	require.NoError(t, PutClaim(ctx, u, root))
	require.NoError(t, PutClaim(ctx, u, a))

	hs, err := u.GetClaimHeights(ctx, []Id{root.ID(), a.ID()})
	require.NoError(t, err)
	require.Equal(t, []uint64{0, 1}, hs, "initial node 0, source 1 — positionally")

	h, err := GetClaimHeight(ctx, u, a.ID())
	require.NoError(t, err)
	require.Equal(t, uint64(1), h, "single-item wrapper agrees")
}

// TestHeightCacheServesAfterNote: a HeightCache answers a Noted id without
// touching the Universe, and heights being immutable it never overwrites.
func TestHeightCacheServesAfterNote(t *testing.T) {
	ctx := context.Background()
	root := contributor(t)
	a := srcClaim(t, root, "a") // height 1

	cache := NewHeightCache()
	cache.NoteClaims(root, a)
	// An empty Universe: a hit must come from the cache, not a load.
	hs, err := cache.GetClaimHeights(ctx, NewMemoryUniverse(), []Id{a.ID(), root.ID()})
	require.NoError(t, err)
	require.Equal(t, []uint64{1, 0}, hs, "served from the cache, positionally")

	got, ok := cache.Get(a.ID())
	require.True(t, ok)
	require.Equal(t, uint64(1), got)
}
