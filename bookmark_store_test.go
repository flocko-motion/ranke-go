package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The 𝒰_hist reference implementation: what it stores it hands back verbatim, and it
// holds its own copy, since a bookmark's slot is not derived from its bytes and so
// nothing else would catch a caller mutating the buffer it wrote.

func TestMemoryBookmarksRoundTrip(t *testing.T) {
	ctx := context.Background()
	hist := NewMemoryBookmarks()
	slot, err := IdSeq(3, []byte("store-seed"))
	require.NoError(t, err)

	_, err = hist.Get(ctx, slot)
	require.ErrorIs(t, err, ErrNotFound, "an empty slot is absence, not an error of its own")

	record := []byte("a bookmark record")
	require.NoError(t, hist.Put(ctx, slot, record))
	got, err := hist.Get(ctx, slot)
	require.NoError(t, err)
	require.Equal(t, record, got)

	// 𝒰_hist is mutable, unlike 𝒰: a slot takes the newer record.
	require.NoError(t, hist.Put(ctx, slot, []byte("a later record")))
	got, err = hist.Get(ctx, slot)
	require.NoError(t, err)
	require.Equal(t, []byte("a later record"), got)
}

func TestMemoryBookmarksCopiesWhatItHolds(t *testing.T) {
	ctx := context.Background()
	hist := NewMemoryBookmarks()
	slot, err := IdSeq(0, []byte("copy-seed"))
	require.NoError(t, err)

	record := []byte("original")
	require.NoError(t, hist.Put(ctx, slot, record))
	record[0] = 'X'

	got, err := hist.Get(ctx, slot)
	require.NoError(t, err)
	require.Equal(t, []byte("original"), got, "the store keeps its own copy of what it was given")

	got[0] = 'Y'
	again, err := hist.Get(ctx, slot)
	require.NoError(t, err)
	require.Equal(t, []byte("original"), again, "and hands out another")
}

func TestMemoryBookmarksRefusesANilSlot(t *testing.T) {
	ctx := context.Background()
	hist := NewMemoryBookmarks()

	_, err := hist.Get(ctx, nil)
	require.ErrorIs(t, err, errBookmarkNilSlot)
	require.ErrorIs(t, hist.Put(ctx, nil, []byte("x")), errBookmarkNilSlot)
}
