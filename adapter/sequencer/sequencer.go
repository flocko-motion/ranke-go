// package: sequencer / coordination
// type:    adapter
// job:     generic BranchTableHead from injected load/save closures — back B_h with any existing store
// limits:  concrete backends live in sub-packages (-> adapter/sequencer/mem, adapter/sequencer/file)
//
// Package sequencer holds the generic BranchTableHead constructor. B_h is
// the one mutable handle in an otherwise immutable, content-addressed
// system — the pointer to the latest branch-table revision, the sequencing
// point of a distributed deployment. It is 256 bits: there is no storage
// *technology* here, only a choice of *where*. New injects that choice as a
// load/save pair; the mem and file conveniences live in sub-packages.
//
// This is a deliberately separate seam from the Universe (adapter/storage):
// 𝒰 wants capacity (S3, fs, …); B_h wants a durable, ideally
// compare-and-swappable cell (a DB row, a KV entry, etcd, a file).
package sequencer

import (
	"context"

	"github.com/flocko-motion/ranke-go"
)

// New builds a BranchTableHead from a load/save pair, so any existing store
// — a users-database row, a KV entry, a config value — backs B_h without a
// dedicated adapter. load returns a nil Id when no head is set yet; save
// persists (id == nil means clear). Close is a no-op.
func New(
	load func(context.Context) (ranke.Id, error),
	save func(context.Context, ranke.Id) error,
) ranke.BranchTableHead {
	return &injectedHead{load: load, save: save}
}

type injectedHead struct {
	load func(context.Context) (ranke.Id, error)
	save func(context.Context, ranke.Id) error
}

func (h *injectedHead) Load(ctx context.Context) (ranke.Id, error)  { return h.load(ctx) }
func (h *injectedHead) Save(ctx context.Context, id ranke.Id) error { return h.save(ctx, id) }
func (h *injectedHead) Close() error                                { return nil }
