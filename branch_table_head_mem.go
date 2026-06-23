// package: ranke / branch_table_head
// type:    io
// job:     in-memory BranchTableHead holding B_h in a mutex-guarded field; lost on process exit
// limits:  not durable (-> branch_table_head_fs); does not interpret the Id (-> branch_table)
package ranke

import (
	"context"
	"sync"
)

// NewMemBranchTableHead keeps B_h in memory — lost on process exit.
func NewMemBranchTableHead() BranchTableHead { return &memBranchTableHead{} }

type memBranchTableHead struct {
	mu sync.RWMutex
	id Id
}

func (b *memBranchTableHead) Load(_ context.Context) (Id, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.id, nil
}

func (b *memBranchTableHead) Save(_ context.Context, id Id) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.id = id
	return nil
}

func (b *memBranchTableHead) Close() error { return nil }
