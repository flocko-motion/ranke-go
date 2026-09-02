package ranke

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// sitem builds a SpineItem with an id derived from tag, making replacements distinguishable.
func sitem(t *testing.T, tag string, revision int) SpineItem {
	t.Helper()
	id, err := HashContent([]byte(tag))
	require.NoError(t, err)
	return NewSpineItem(id, revision, revision, time.Unix(int64(revision), 0).UTC())
}

func revsOf(items []SpineItem) []int {
	out := make([]int, len(items))
	for i, it := range items {
		out[i] = it.GetRevision()
	}
	return out
}

// TestSpliceSpine covers forward advance, re-tag from middle, full retag, and empty edge cases.
func TestSpliceSpine(t *testing.T) {
	base := []SpineItem{sitem(t, "a", 0), sitem(t, "b", 1), sitem(t, "c", 2), sitem(t, "d", 3)}

	// Forward advance: tagged begins one past the end, so nothing is dropped.
	fwd := SpliceSpine(base, []SpineItem{sitem(t, "e", 4)})
	require.Equal(t, []int{0, 1, 2, 3, 4}, revsOf(fwd))

	// Re-tag from revision 2: keep 0,1; replace 2.. with the re-tagged items.
	retag := SpliceSpine(base, []SpineItem{sitem(t, "C2", 2), sitem(t, "D2", 3), sitem(t, "E2", 4)})
	require.Equal(t, []int{0, 1, 2, 3, 4}, revsOf(retag))
	require.True(t, retag[2].GetId().Equal(sitem(t, "C2", 2).GetId()), "revision 2 replaced by the re-tagged item")
	require.True(t, retag[1].GetId().Equal(sitem(t, "b", 1).GetId()), "revision 1 kept from the base")

	// Full retag from 0: everything replaced.
	full := SpliceSpine(base, []SpineItem{sitem(t, "x", 0), sitem(t, "y", 1)})
	require.Equal(t, []int{0, 1}, revsOf(full))
	require.True(t, full[0].GetId().Equal(sitem(t, "x", 0).GetId()))

	// Empty tagged leaves the base untouched; empty base yields tagged as-is.
	require.Equal(t, revsOf(base), revsOf(SpliceSpine(base, nil)))
	require.Equal(t, []int{4}, revsOf(SpliceSpine(nil, []SpineItem{sitem(t, "z", 4)})))
}

func TestSpineItemGetters(t *testing.T) {
	id, err := HashContent([]byte("head"))
	require.NoError(t, err)
	ts := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	item := NewSpineItem(id, 2, 3, ts)
	require.True(t, item.GetId().Equal(id), "id round-trips")
	require.Equal(t, 2, item.GetRevision())
	require.Equal(t, 3, item.GetHeight())
	require.True(t, item.GetTimestamp().Equal(ts))
}

// TestSpineItemZeroValue: the zero item (as returned for an empty timeline) has no id and height zero.
func TestSpineItemZeroValue(t *testing.T) {
	var zero SpineItem
	require.Nil(t, zero.GetId())
	require.Equal(t, 0, zero.GetHeight())
	require.True(t, zero.GetTimestamp().IsZero())
}
