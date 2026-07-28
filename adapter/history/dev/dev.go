// package: adapter/history/dev / testkit
// type:    adapter
// job:     an in-memory History for tests and development — the head-id timeline k₀…kₙ held in a slice, stamped from an injected deterministic Clock
// limits:  not durable and not concurrent (single-threaded, no locks); stores ids only, like adapter/history/mem but with a controllable clock so a generated archive's history is reproducible
package dev

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flocko-motion/ranke-go"
)

var (
	errNilID    = errors.New("adapter/history/dev: nil id")
	errRevRange = errors.New("adapter/history/dev: revision out of range")
	errRange    = errors.New("adapter/history/dev: range out of bounds")
)

// Clock is the deterministic time source each timeline entry is stamped with.
// Kept a local interface so this adapter depends only on ranke —
// generator.Clock satisfies it. Tick returns the current time and advances.
type Clock interface {
	Tick() time.Time
}

// History is a minimal in-memory head-id timeline. It is the deterministic
// sibling of adapter/history/mem: instead of time.Now it stamps each entry
// from an injected Clock, so append order and timestamps are reproducible.
// Single-threaded by design (no locks).
type History struct {
	clock Clock
	items []ranke.HistoryItem
}

var _ ranke.History = (*History)(nil)

// New returns an empty timeline stamped from clock.
func New(clock Clock) *History {
	return &History{clock: clock}
}

// Append records id as the new head at the given height and revision, stamping
// the append time from the clock (which it advances by one tick).
func (h *History) Append(_ context.Context, id ranke.Id, height int, revision int) (ranke.HistoryItem, error) {
	if id == nil {
		return ranke.HistoryItem{}, errNilID
	}
	item := ranke.NewHistoryItem(id, revision, height, h.clock.Tick())
	h.items = append(h.items, item)
	return item, nil
}

// Latest returns kₙ, or the zero item when the timeline is empty.
func (h *History) Latest(_ context.Context) (ranke.HistoryItem, error) {
	if len(h.items) == 0 {
		return ranke.HistoryItem{}, nil
	}
	return h.items[len(h.items)-1], nil
}

// GetAtRevision returns kᵢ; an out-of-range revision is an error.
func (h *History) GetAtRevision(_ context.Context, revision int) (ranke.HistoryItem, error) {
	if revision < 0 || revision >= len(h.items) {
		return ranke.HistoryItem{}, fmt.Errorf("%w: revision %d out of range [0,%d)", errRevRange, revision, len(h.items))
	}
	return h.items[revision], nil
}

// GetBulk returns the half-open revision range [fromRevision, toExcludingRevision).
func (h *History) GetBulk(_ context.Context, fromRevision, toExcludingRevision int) ([]ranke.HistoryItem, error) {
	if fromRevision < 0 || toExcludingRevision > len(h.items) || fromRevision > toExcludingRevision {
		return nil, fmt.Errorf("%w: range [%d,%d) out of bounds [0,%d)", errRange, fromRevision, toExcludingRevision, len(h.items))
	}
	return append([]ranke.HistoryItem(nil), h.items[fromRevision:toExcludingRevision]...), nil
}

// Len returns the number of entries (n+1).
func (h *History) Len(_ context.Context) (int, error) { return len(h.items), nil }

// Close releases the store; the in-memory timeline holds nothing to release.
func (h *History) Close() error { return nil }
