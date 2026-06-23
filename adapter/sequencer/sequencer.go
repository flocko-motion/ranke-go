// package: sequencer / coordination
// type:    adapter
// job:     persists B_h — the single mutable Id naming the current branch-table revision
// limits:  storing 256 bits is trivial; pick a place. Not a content store (-> adapter/storage)
//
// Package sequencer holds BranchTableHead implementations. B_h is the one
// mutable handle in an otherwise immutable, content-addressed system — the
// pointer to the latest branch-table revision, and thus the sequencing
// point of a distributed deployment. It is 256 bits: there is no storage
// *technology* here, only a choice of *where*. So this package stays tiny
// — Mem (mem.go) and File (file.go) conveniences plus the generic
// New below, which injects any place you like.
//
// This is a deliberately separate seam from the Universe (adapter/storage):
// 𝒰 wants capacity (S3, fs, …); B_h wants a durable, ideally
// compare-and-swappable cell (a DB row, a KV entry, etcd, a file).
package sequencer

import (
	"context"

	"github.com/flocko-motion/ranke-go"
)

// New builds a BranchTableHead from a load/save pair, so any existing
// store — a users-database row, a KV entry, a config value — backs B_h
// without a dedicated adapter. load returns a nil Id when no head is set
// yet; save persists (id == nil means clear). Close is a no-op.
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
