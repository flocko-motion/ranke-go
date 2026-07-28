// package: ranke / history
// type:    logic
// job:     the History contract — persists the archive head-id timeline k₀…kₙ (append + read-back) — plus SpliceHistory, the graft a tagging pass uses to update it
// limits:  interface only; concrete timeline stores live in adapters (-> adapter/history)
package ranke

import (
	"context"
)

// History persists the head-id timeline k₀…kₙ. Append records a new head,
// assigning its height and stamping the time; the rest read the timeline
// back. Oldest first: At(0) is k₀, Latest is kₙ, List runs k₀…kₙ.
type History interface {
	// Append records id as the new head, returning the stamped item.
	Append(ctx context.Context, id Id, height int, revision int) (HistoryItem, error)
	// Latest returns kₙ, or the zero item when the timeline is empty.
	Latest(ctx context.Context) (HistoryItem, error)
	// At returns kᵢ; an out-of-range i is an error.
	GetAtRevision(ctx context.Context, revision int) (HistoryItem, error)
	GetBulk(ctx context.Context, fromRevision, toExcludingRevision int) ([]HistoryItem, error)
	// Len returns the number of entries (n+1).
	Len(ctx context.Context) (int, error)
	Close() error
}

// SpliceHistory grafts tagged onto existing at the revision tagged begins at.
// TagArchive returns only the revisions it just (re)tagged — its first item's
// GetRevision is the splice point — so this keeps existing's entries below that
// revision and replaces everything from there with tagged. That makes it
// correct both for a plain forward advance (tagged starts one past the end, so
// nothing is dropped) and for a re-tag from an earlier revision (the superseded
// tail is discarded). An empty tagged leaves existing unchanged.
//
// Entries are matched by GetRevision, not slice position, so a non-contiguous
// or unordered existing timeline still splices at the right boundary.
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
