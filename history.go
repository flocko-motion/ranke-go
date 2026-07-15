// package: ranke / history
// type:    logic
// job:     the History contract — persists the archive head-id timeline k₀…kₙ (append + read-back)
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
