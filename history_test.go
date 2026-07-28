package ranke

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// hitem builds a HistoryItem whose id is derived from tag (so replaced entries
// are distinguishable) at the given revision.
func hitem(t *testing.T, tag string, revision int) HistoryItem {
	t.Helper()
	id, err := HashContent([]byte(tag))
	require.NoError(t, err)
	return NewHistoryItem(id, revision, revision, time.Unix(int64(revision), 0).UTC())
}

func revsOf(items []HistoryItem) []int {
	out := make([]int, len(items))
	for i, it := range items {
		out[i] = it.GetRevision()
	}
	return out
}

// TestSpliceHistory covers the graft points: a plain forward advance (nothing
// dropped), a re-tag from a middle revision (superseded tail replaced), a full
// retag from 0, and the empty edge cases.
func TestSpliceHistory(t *testing.T) {
	base := []HistoryItem{hitem(t, "a", 0), hitem(t, "b", 1), hitem(t, "c", 2), hitem(t, "d", 3)}

	// Forward advance: tagged begins one past the end, so nothing is dropped.
	fwd := SpliceHistory(base, []HistoryItem{hitem(t, "e", 4)})
	require.Equal(t, []int{0, 1, 2, 3, 4}, revsOf(fwd))

	// Re-tag from revision 2: keep 0,1; replace 2.. with the re-tagged items.
	retag := SpliceHistory(base, []HistoryItem{hitem(t, "C2", 2), hitem(t, "D2", 3), hitem(t, "E2", 4)})
	require.Equal(t, []int{0, 1, 2, 3, 4}, revsOf(retag))
	require.True(t, retag[2].GetId().Equal(hitem(t, "C2", 2).GetId()), "revision 2 replaced by the re-tagged item")
	require.True(t, retag[1].GetId().Equal(hitem(t, "b", 1).GetId()), "revision 1 kept from the base")

	// Full retag from 0: everything replaced.
	full := SpliceHistory(base, []HistoryItem{hitem(t, "x", 0), hitem(t, "y", 1)})
	require.Equal(t, []int{0, 1}, revsOf(full))
	require.True(t, full[0].GetId().Equal(hitem(t, "x", 0).GetId()))

	// Empty tagged leaves the base untouched; empty base yields tagged as-is.
	require.Equal(t, revsOf(base), revsOf(SpliceHistory(base, nil)))
	require.Equal(t, []int{4}, revsOf(SpliceHistory(nil, []HistoryItem{hitem(t, "z", 4)})))
}
