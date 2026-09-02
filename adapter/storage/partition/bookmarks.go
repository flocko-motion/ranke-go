// package: partition / bookmarks
// type:    logic
// job:     the partition's 𝒰_hist — every shard holds the whole bookmark list, the one place this
// adapter broadcasts where it would otherwise route by key
// limits:  carries no policy beyond that choice; the record and its rules are the library's
// (-> partition.go, bookmark_store in the library)
package partition

import (
	"context"
	"errors"

	"github.com/rankegraph/ranke-go"
)

// Bookmarks replicates 𝒰_hist to every shard, DIFFERING FROM CLAIM ROUTING ON
// PURPOSE: the list must stay contiguous, and sharding it would let one lost shard
// hole it, which the O(log n) search reads as the end.
func (p *partition) Bookmarks() ranke.BookmarkStore { return partitionBookmarks{p} }

type partitionBookmarks struct{ p *partition }

// Put broadcasts, the loop Tag uses. One failure fails it: a partial list is the
// hole this prevents.
func (b partitionBookmarks) Put(ctx context.Context, key ranke.Id, record []byte) error {
	for sh := range b.p.shards {
		if err := b.p.shards[sh].Bookmarks().Put(ctx, key, record); err != nil {
			return err
		}
	}
	return nil
}

// Get reads the first shard that answers: every shard holds the same list.
func (b partitionBookmarks) Get(ctx context.Context, key ranke.Id) ([]byte, error) {
	var lastErr error = ranke.ErrNotFound
	for sh := range b.p.shards {
		record, err := b.p.shards[sh].Bookmarks().Get(ctx, key)
		if err == nil {
			return record, nil
		}
		if !errors.Is(err, ranke.ErrNotFound) {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}
