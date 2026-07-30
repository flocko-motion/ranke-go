// package: ranke / history
// type:    logic
// job:     HistoryItem — one immutable entry in the head-id timeline (head id, height, append time)
// plus its constructor and getters
// limits:  a value type; the timeline it belongs to is History (-> history)
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
	revision  int
	height    int
	timestamp time.Time
}

// NewHistoryItem builds a HistoryItem. It lets adapters outside package
// ranke reconstruct an item from persisted storage (id + its recorded
// height and append time).
func NewHistoryItem(id Id, revision int, height int, timestamp time.Time) HistoryItem {
	return HistoryItem{id: id, revision: revision, height: height, timestamp: timestamp}
}

// GetId returns the head id this entry records.
func (h HistoryItem) GetId() Id { return h.id }

// GetRevision returns the entry's position in the timeline (0 is the oldest).
func (h HistoryItem) GetRevision() int { return h.revision }

// GetHeight returns the entry's height
func (h HistoryItem) GetHeight() int { return h.height }

// GetTimestamp returns the time the head was appended.
func (h HistoryItem) GetTimestamp() time.Time { return h.timestamp }
