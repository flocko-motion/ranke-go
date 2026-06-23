package sequencer

import (
	"context"
	"sync"

	"github.com/flocko-motion/ranke-go"
)

// Mem keeps B_h in memory — lost on process exit. For tests and
// short-lived sessions.
func Mem() ranke.BranchTableHead { return &memHead{} }

type memHead struct {
	mu sync.RWMutex
	id ranke.Id
}

func (h *memHead) Load(context.Context) (ranke.Id, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.id, nil
}

func (h *memHead) Save(_ context.Context, id ranke.Id) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.id = id
	return nil
}

func (h *memHead) Close() error { return nil }
