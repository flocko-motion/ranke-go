// package: ranke / sequencer
// type:    logic
// job:     the Sequencer contract — the sole writer of a Ranke-Archive: hands out read snapshots
// and advances the head by merging contributions
// limits:  interfaces only; the naive concrete implementation is a write-path mechanism
// (-> adapter/sequencer_mechanism)
package ranke

import "context"

// Sequencer is the single writer of a Ranke-Archive (RankeDB paper
// §Sequencer): it hands out immutable read snapshots and advances the
// head, k → k', by merging contributions. The concrete implementation
// (naive here, concurrent in a server) lives in an adapter.
type Sequencer interface {
	// GetArchive returns the current immutable snapshot RA_k.
	GetArchive(ctx context.Context) (Archive, error)
	// GetContributor returns the contributor the Sequencer attests branch
	// advances with.
	GetContributor() Contributor
	// NewContribution opens a contribution against the current archive.
	NewContribution(ctx context.Context) (Contribution, error)
	// Merge commits a persisted contribution, advancing the head (step 6),
	// and returns a Receipt.
	Merge(ctx context.Context, c MergableContribution) (Receipt, error)
}

// Receipt is the outcome of a committed merge — the new head the archive
// advanced to.
type Receipt interface {
	Head() Id
}
