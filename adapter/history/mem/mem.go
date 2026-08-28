// package: mem / coordination
// type:    adapter
// job:     in-memory History — the head-id timeline held in a slice, lost on process exit
// limits:  not durable (-> adapter/history/file); stores ids only (-> ranke)
//
// Package mem is an ephemeral in-memory head-id timeline, for tests and
// short-lived sessions.
package mem

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/rankegraph/ranke-go"
)

var (
	errNilID    = errors.New("adapter/history/mem: nil id")
	errRevRange = errors.New("adapter/history/mem: revision out of range")
	errRange    = errors.New("adapter/history/mem: range out of bounds")
)

// New returns an in-memory History — lost on process exit.
func New() ranke.History {
	return &history{}
}

type history struct {
	mu    sync.Mutex
	items []ranke.HistoryItem
}

func (h *history) Append(_ context.Context, id ranke.Id, height int, revision int) (ranke.HistoryItem, error) {
	if id == nil {
		return ranke.HistoryItem{}, errNilID
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	item := ranke.NewHistoryItem(id, revision, height, time.Now().UTC())
	h.items = append(h.items, item)
	return item, nil
}

func (h *history) Latest(_ context.Context) (ranke.HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.items) == 0 {
		return ranke.HistoryItem{}, nil
	}
	return h.items[len(h.items)-1], nil
}

func (h *history) GetAtRevision(_ context.Context, revision int) (ranke.HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if revision < 0 || revision >= len(h.items) {
		return ranke.HistoryItem{}, fmt.Errorf("%w: revision %d out of range [0,%d)", errRevRange, revision, len(h.items))
	}
	return h.items[revision], nil
}

// GetBulk returns the half-open revision range [fromRevision, toExcludingRevision).
func (h *history) GetBulk(_ context.Context, fromRevision, toExcludingRevision int) ([]ranke.HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if fromRevision < 0 || toExcludingRevision > len(h.items) || fromRevision > toExcludingRevision {
		return nil, fmt.Errorf("%w: range [%d,%d) out of bounds [0,%d)", errRange, fromRevision, toExcludingRevision, len(h.items))
	}
	return append([]ranke.HistoryItem(nil), h.items[fromRevision:toExcludingRevision]...), nil
}

func (h *history) Len(_ context.Context) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.items), nil
}

func (h *history) Close() error { return nil }
