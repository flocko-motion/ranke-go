package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the Universe package helpers (universe.go): the
// copy-option resolution and the single-item wrappers over the bulk
// interface. The Universe CONTRACT (durability, reload, closure across a
// real store) is verified blackbox in adaptertest/tests, matrixed over
// adapters; here we pin the pure package-level code against the complete
// toy mapUniverse (archive_test.go).

func TestNewCopyConfigDefaults(t *testing.T) {
	cfg := NewCopyConfig()
	require.False(t, cfg.Closure, "closure off by default")
	require.False(t, cfg.Content, "content off by default")
	require.Nil(t, cfg.Progress)
}

func TestNewCopyConfigOptions(t *testing.T) {
	var called bool
	cfg := NewCopyConfig(WithClosure(), WithContent(), WithProgress(func(CopyProgress) { called = true }))
	require.True(t, cfg.Closure, "WithClosure sets closure")
	require.True(t, cfg.Content, "WithContent sets content")
	require.NotNil(t, cfg.Progress, "WithProgress registers a callback")

	cfg.Progress(CopyProgress{})
	require.True(t, called, "the registered callback is the one supplied")
}

// TestUniverseClaimWrappers: the single-item PutClaim/HasClaim/GetClaim
// helpers delegate to the bulk interface — a stored claim is present and
// reads back, an absent id is absent and errors ErrNotFound.
func TestUniverseClaimWrappers(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "hello")

	require.NoError(t, PutClaim(ctx, u, root))
	require.NoError(t, PutClaim(ctx, u, em))

	has, err := HasClaim(ctx, u, em.ID())
	require.NoError(t, err)
	require.True(t, has)

	got, err := GetClaim(ctx, u, em.ID())
	require.NoError(t, err)
	require.True(t, got.ID().Equal(em.ID()))

	absent, err := HashContent([]byte("absent"))
	require.NoError(t, err)
	has, err = HasClaim(ctx, u, absent)
	require.NoError(t, err)
	require.False(t, has)
	_, err = GetClaim(ctx, u, absent)
	require.ErrorIs(t, err, ErrNotFound)
}

// TestUniverseContentWrappers: the single-item PutContent/HasContent/
// GetContent helpers delegate to the bulk interface.
func TestUniverseContentWrappers(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	blob := []byte("some content bytes")
	hash, err := HashContent(blob)
	require.NoError(t, err)

	require.NoError(t, PutContent(ctx, u, hash, blob))

	has, err := HasContent(ctx, u, hash)
	require.NoError(t, err)
	require.True(t, has)

	gotBs, err := u.GetContents(ctx, []ContentRef{{Hash: hash, ContentSize: uint64(len(blob))}})
	require.NoError(t, err)
	require.Equal(t, blob, gotBs[0])

	absent, err := HashContent([]byte("absent"))
	require.NoError(t, err)
	has, err = HasContent(ctx, u, absent)
	require.NoError(t, err)
	require.False(t, has)
	_, err = u.GetContents(ctx, []ContentRef{{Hash: absent, ContentSize: 0}})
	require.ErrorIs(t, err, ErrNotFound)
}
