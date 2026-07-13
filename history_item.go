package ranke

import (
	"time"
)

// HistoryItem is one entry in the head-id timeline: the head id, its
// height (position in the sequence, i=0 the oldest), and the time it was
// appended. Fields are unexported; construct one with NewHistoryItem and
// read via the getters.
type HistoryItem struct {
	id        Id
	height    int
	timestamp time.Time
}

// NewHistoryItem builds a HistoryItem. It lets adapters outside package
// ranke reconstruct an item from persisted storage (id + its recorded
// height and append time).
func NewHistoryItem(id Id, height int, timestamp time.Time) HistoryItem {
	return HistoryItem{id: id, height: height, timestamp: timestamp}
}

func (h HistoryItem) GetId() Id { return h.id }

func (h HistoryItem) GetHeight() int { return h.height }

func (h HistoryItem) GetTimestamp() time.Time { return h.timestamp }
