package ranke

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Foundation unit test for HistoryItem — the immutable value type one
// timeline entry (head id + height + append time). The History interface
// itself is adapter-backed and belongs to the integration layer; the value
// type is pure and pinned here.

func TestHistoryItemGetters(t *testing.T) {
	id, err := HashContent([]byte("head"))
	require.NoError(t, err)
	ts := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	item := NewHistoryItem(id, 2, 3, ts)
	require.True(t, item.GetId().Equal(id), "id round-trips")
	require.Equal(t, 2, item.GetRevision())
	require.Equal(t, 3, item.GetHeight())
	require.True(t, item.GetTimestamp().Equal(ts))
}

// TestHistoryItemZeroValue: the zero item (as returned for an empty
// timeline) has no id and height zero.
func TestHistoryItemZeroValue(t *testing.T) {
	var zero HistoryItem
	require.Nil(t, zero.GetId())
	require.Equal(t, 0, zero.GetHeight())
	require.True(t, zero.GetTimestamp().IsZero())
}
