// package: ranke / history_item
// type:    data
// job:     HistoryItem — one entry of the head timeline TagArchive derives from an archive's
// branch-table spine (head id, height, creation time) — plus the splice that grafts a re-tag onto
// an earlier run
// limits:  a value type over 𝒰, unrelated to the bookmark list that locates a moving head
// (-> bookmarks); the walk that builds it is universe_default_tagger.go's
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

// SpliceHistory grafts tagged onto existing at tagged's first revision, dropping
// existing's entries from there on — correct for a forward advance and for a
// re-tag from an earlier revision alike. An empty tagged leaves existing as is.
func SpliceHistory(existing, tagged []HistoryItem) []HistoryItem {
	if len(tagged) == 0 {
		return existing
	}
	at := tagged[0].GetRevision()
	out := make([]HistoryItem, 0, at+len(tagged))
	for _, it := range existing {
		if it.GetRevision() >= at {
			continue // superseded by the tagged revisions
		}
		out = append(out, it)
	}
	return append(out, tagged...)
}
