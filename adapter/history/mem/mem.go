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
	"fmt"
	"sync"
	"time"

	"github.com/flocko-motion/ranke-go"
)

// New returns an in-memory History — lost on process exit.
func New() ranke.History {
	return &history{}
}

type history struct {
	mu    sync.Mutex
	items []ranke.HistoryItem
}

func (h *history) Append(_ context.Context, id ranke.Id) (ranke.HistoryItem, error) {
	if id == nil {
		return ranke.HistoryItem{}, fmt.Errorf("adapter/history/mem: nil id")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	item := ranke.NewHistoryItem(id, len(h.items), time.Now().UTC())
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

func (h *history) Get(_ context.Context, i int) (ranke.HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i < 0 || i >= len(h.items) {
		return ranke.HistoryItem{}, fmt.Errorf("adapter/history/mem: index %d out of range [0,%d)", i, len(h.items))
	}
	return h.items[i], nil
}

// GetBulk returns the half-open range [from, to).
func (h *history) GetBulk(_ context.Context, from, to int) ([]ranke.HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if from < 0 || to > len(h.items) || from > to {
		return nil, fmt.Errorf("adapter/history/mem: range [%d,%d) out of bounds [0,%d)", from, to, len(h.items))
	}
	return append([]ranke.HistoryItem(nil), h.items[from:to]...), nil
}

func (h *history) Len(_ context.Context) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.items), nil
}

func (h *history) Close() error { return nil }
