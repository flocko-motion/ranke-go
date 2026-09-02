// package: stack / bookmarks
// type:    logic
// job:     the stack's 𝒰_hist — a delegating BookmarkStore that puts and gets through the same
// tier rules, fan-out and fall-through the claim path uses
// limits:  carries no policy of its own; every decision is stack.go's, called not copied
// (-> stack.go, bookmark_store in the library)
package stack

import (
	"context"
	"errors"

	"github.com/rankegraph/ranke-go"
)

// Bookmarks returns 𝒰_hist routed through the claim path ON PURPOSE: only the key
// derivation differs, so a bookmark wants the same layering. A parallel path is the
// regression this undoes.
func (s *stack) Bookmarks() ranke.BookmarkStore { return stackBookmarks{s} }

type stackBookmarks struct{ s *stack }

// Put writes the claim path's tiers, so only an authoritative failure fails it.
func (b stackBookmarks) Put(ctx context.Context, key ranke.Id, record []byte) error {
	put := func(ctx context.Context, l layer) error {
		if !l.caps.Bookmarks {
			return nil // holds no 𝒰_hist, so it is skipped both ways
		}
		return l.u.Bookmarks().Put(ctx, key, record)
	}
	if err := b.s.writeSync(ctx, put); err != nil {
		return err
	}
	b.s.background(ctx, func(ctx context.Context, l layer) { _ = put(ctx, l) })
	return nil
}

// Get answers from the topmost layer holding the slot, filling those above that
// missed (StorageTierLazy). Inline, the background filler carrying content blobs.
func (b stackBookmarks) Get(ctx context.Context, key ranke.Id) ([]byte, error) {
	var missed []int
	var lastErr error = ranke.ErrNotFound
	for li := range b.s.layers {
		if !b.s.layers[li].caps.Bookmarks {
			continue
		}
		record, err := b.s.layers[li].u.Bookmarks().Get(ctx, key)
		if err == nil {
			b.fill(ctx, missed, key, record)
			return record, nil
		}
		if !errors.Is(err, ranke.ErrNotFound) {
			return nil, err
		}
		missed, lastErr = append(missed, li), err
	}
	return nil, lastErr
}

// fill writes the record into the layers that missed it, best-effort: a fill only
// makes a later read faster.
func (b stackBookmarks) fill(ctx context.Context, layers []int, key ranke.Id, record []byte) {
	for _, li := range layers {
		_ = b.s.layers[li].u.Bookmarks().Put(ctx, key, record)
	}
}
