// package: mem / coordination
// type:    adapter
// job:     in-memory BranchTableHead — B_h held in a mutex-guarded field, lost on process exit
// limits:  not durable (-> adapter/sequencer/file); stores only the Id, no interpretation (-> ranke)
//
// Package mem is an ephemeral in-memory sequencer: it keeps B_h in memory,
// for tests and short-lived sessions.
package mem

import (
	"context"
	"sync"

	"github.com/flocko-motion/ranke-go"
)

// New returns an in-memory BranchTableHead — lost on process exit.
func New() ranke.BranchTableHead { return &head{} }

type head struct {
	mu sync.RWMutex
	id ranke.Id
}

func (h *head) Load(context.Context) (ranke.Id, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.id, nil
}

func (h *head) Save(_ context.Context, id ranke.Id) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.id = id
	return nil
}

func (h *head) Close() error { return nil }
