// package: partition / persistence
// type:    adapter
// job:     shard one Universe across N backends — each key routed to a shard by hash(id) mod N (paper §Composing Universes)
// limits:  no storage of its own (-> the shard Universes); N is fixed (consistent-hashing / resharding not modelled)
//
// Package partition spreads a single Universe across N shard Universes by
// content-addressed id: each claim id and content hash is routed to one shard
// by a stable hash mod N, so the shards together hold the whole keyspace and
// each holds a disjoint slice. A claim and the content it references are routed
// independently (by their own ids), which is fine — both are content-addressed.
// Composes with stack: a partition can sit beneath an eager cache, or a shard
// can itself be a stack.
package partition

import (
	"context"
	"errors"
	"io"

	"github.com/flocko-motion/ranke-go"
)

type partition struct {
	shards []ranke.Universe
}

// NewPartition routes keys across shards by hash(id) mod len(shards). At least
// one shard is required; the shard set is fixed for the partition's lifetime.
func NewPartition(shards ...ranke.Universe) (ranke.Universe, error) {
	if len(shards) == 0 {
		return nil, errors.New("partition: at least one shard required")
	}
	for _, s := range shards {
		if s == nil {
			return nil, errors.New("partition: nil shard Universe")
		}
	}
	if len(shards) == 1 {
		// Nothing to shard across — the partition would just forward every call
		// to the one shard, so return it directly.
		return shards[0], nil
	}
	return &partition{shards: shards}, nil
}

// shardOf picks the shard for a key: the id is already a content-addressed hash
// (uniformly distributed), so shard i = X mod N taken straight over the id —
// no re-hashing (paper §Composing Universes). X is the id's stable string form
// read as a base-256 integer, reduced mod N by Horner's method.
func (p *partition) shardOf(id ranke.Id) int {
	n := uint64(len(p.shards))
	var x uint64
	s := id.String()
	for i := 0; i < len(s); i++ {
		x = (x*256 + uint64(s[i])) % n
	}
	return int(x)
}

func (p *partition) PutClaims(ctx context.Context, cs []ranke.Claim) error {
	groups := make([][]ranke.Claim, len(p.shards))
	for _, c := range cs {
		if c == nil || c.ID() == nil {
			return errors.New("partition.PutClaims: nil claim or id")
		}
		s := p.shardOf(c.ID())
		groups[s] = append(groups[s], c)
	}
	for s, g := range groups {
		if len(g) == 0 {
			continue
		}
		if err := p.shards[s].PutClaims(ctx, g); err != nil {
			return err
		}
	}
	return nil
}

func (p *partition) PutContents(ctx context.Context, blobs []ranke.ContentBlob) error {
	groups := make([][]ranke.ContentBlob, len(p.shards))
	for _, b := range blobs {
		if b.Hash == nil {
			return errors.New("partition.PutContents: nil hash")
		}
		s := p.shardOf(b.Hash)
		groups[s] = append(groups[s], b)
	}
	for s, g := range groups {
		if len(g) == 0 {
			continue
		}
		if err := p.shards[s].PutContents(ctx, g); err != nil {
			return err
		}
	}
	return nil
}

func (p *partition) GetClaims(ctx context.Context, ids []ranke.Id) ([]ranke.Claim, error) {
	out := make([]ranke.Claim, len(ids))
	groups := p.groupIds(ids)
	for s, idx := range groups {
		if len(idx) == 0 {
			continue
		}
		got, err := p.shards[s].GetClaims(ctx, at(ids, idx))
		if err != nil {
			return nil, err
		}
		for k, c := range got {
			out[idx[k]] = c
		}
	}
	return out, nil
}

func (p *partition) GetContents(ctx context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	out := make([][]byte, len(refs))
	groups := make([][]int, len(p.shards))
	for i, r := range refs {
		if r.Hash == nil {
			return nil, errors.New("partition.GetContents: nil hash")
		}
		s := p.shardOf(r.Hash)
		groups[s] = append(groups[s], i)
	}
	for s, idx := range groups {
		if len(idx) == 0 {
			continue
		}
		sub := make([]ranke.ContentRef, len(idx))
		for k, i := range idx {
			sub[k] = refs[i]
		}
		got, err := p.shards[s].GetContents(ctx, sub)
		if err != nil {
			return nil, err
		}
		for k, b := range got {
			out[idx[k]] = b
		}
	}
	return out, nil
}

func (p *partition) HasClaims(ctx context.Context, ids []ranke.Id) ([]bool, error) {
	out := make([]bool, len(ids))
	groups := p.groupIds(ids)
	for s, idx := range groups {
		if len(idx) == 0 {
			continue
		}
		got, err := p.shards[s].HasClaims(ctx, at(ids, idx))
		if err != nil {
			return nil, err
		}
		for k, ok := range got {
			out[idx[k]] = ok
		}
	}
	return out, nil
}

func (p *partition) HasContents(ctx context.Context, hashes []ranke.Id) ([]bool, error) {
	out := make([]bool, len(hashes))
	groups := p.groupIds(hashes)
	for s, idx := range groups {
		if len(idx) == 0 {
			continue
		}
		got, err := p.shards[s].HasContents(ctx, at(hashes, idx))
		if err != nil {
			return nil, err
		}
		for k, ok := range got {
			out[idx[k]] = ok
		}
	}
	return out, nil
}

func (p *partition) StreamContent(ctx context.Context, hash ranke.Id, size uint64) (io.ReadCloser, error) {
	if hash == nil {
		return nil, errors.New("partition.StreamContent: nil hash")
	}
	return p.shards[p.shardOf(hash)].StreamContent(ctx, hash, size)
}

func (p *partition) CopyClaims(ctx context.Context, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyClaims(ctx, p, src, ids, opts...)
}

func (p *partition) CopyContents(ctx context.Context, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyContents(ctx, p, src, refs, opts...)
}

// Capabilities reports an ability only if every shard has it: a key may land on
// any shard, so the partition can promise a capability only when all shards can
// (Enumerate and a GQL query likewise span every shard).
// InClosure reports whether id is in head's closure.
//
// TODO: implement — a closure can span shards, so this cannot route by a
// single id; it needs a traversal that resolves each referenced claim
// through the partition. Likely delegates per-claim as the walk proceeds.
func (p *partition) InClosure(ctx context.Context, head, id ranke.Id) (bool, error) {
	panic("TODO: partition.InClosure not implemented")
}

// GetFromClosure returns the claim at id if it is in head's closure.
//
// TODO: implement — closure-membership check, then route the id to its
// shard via the normal partition read.
func (p *partition) GetFromClosure(ctx context.Context, head, id ranke.Id) (ranke.Claim, error) {
	panic("TODO: partition.GetFromClosure not implemented")
}

func (p *partition) Capabilities() ranke.Capabilities {
	c := ranke.Capabilities{Overwrite: true, Delete: true, Enumerate: true, Persistent: true, GQL: true}
	for _, s := range p.shards {
		sc := s.Capabilities()
		c.Overwrite = c.Overwrite && sc.Overwrite
		c.Delete = c.Delete && sc.Delete
		c.Enumerate = c.Enumerate && sc.Enumerate
		c.Persistent = c.Persistent && sc.Persistent
		c.GQL = c.GQL && sc.GQL
	}
	return c
}

// Close closes every shard (best-effort: all attempted, errors joined).
func (p *partition) Close() error {
	var errs []error
	for _, s := range p.shards {
		if err := s.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// groupIds buckets each id's position by its shard.
func (p *partition) groupIds(ids []ranke.Id) [][]int {
	groups := make([][]int, len(p.shards))
	for i, id := range ids {
		if id == nil {
			continue
		}
		s := p.shardOf(id)
		groups[s] = append(groups[s], i)
	}
	return groups
}

func at(ids []ranke.Id, idx []int) []ranke.Id {
	out := make([]ranke.Id, len(idx))
	for i, j := range idx {
		out[i] = ids[j]
	}
	return out
}
