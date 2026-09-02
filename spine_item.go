// package: ranke / spine_item
// type:    data
// job:     one revision of the branch-table spine for timeline tracking
// limits:  a value type over U, unrelated to the bookmark list that locates a moving head
// (-> bookmarks); the walk that builds it is universe_default_tagger.go's
package ranke

import (
	"time"
)

// SpineItem is one revision of the branch-table spine: head id, height, and creation time.
type SpineItem struct {
	id        Id
	revision  int
	height    int
	timestamp time.Time
}

// NewSpineItem builds a SpineItem. It lets adapters outside package ranke reconstruct
// an item from persisted storage.
func NewSpineItem(id Id, revision int, height int, timestamp time.Time) SpineItem {
	return SpineItem{id: id, revision: revision, height: height, timestamp: timestamp}
}

// GetId returns the head id this entry records.
func (h SpineItem) GetId() Id { return h.id }

// GetRevision returns the entry's position in the timeline (0 is the oldest).
func (h SpineItem) GetRevision() int { return h.revision }

// GetHeight returns the entry's height.
func (h SpineItem) GetHeight() int { return h.height }

// GetTimestamp returns the time the head was appended.
func (h SpineItem) GetTimestamp() time.Time { return h.timestamp }

// SpliceSpine grafts tagged onto existing at tagged's first revision, dropping existing's
// entries from there on. An empty tagged leaves existing as is.
func SpliceSpine(existing, tagged []SpineItem) []SpineItem {
	if len(tagged) == 0 {
		return existing
	}
	at := tagged[0].GetRevision()
	out := make([]SpineItem, 0, at+len(tagged))
	for _, it := range existing {
		if it.GetRevision() >= at {
			continue // superseded by the tagged revisions
		}
		out = append(out, it)
	}
	return append(out, tagged...)
}
