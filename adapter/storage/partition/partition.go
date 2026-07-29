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

var (
	errNoShards = errors.New("partition: at least one shard required")
	errNilShard = errors.New("partition: nil shard Universe")
	errNilClaim = errors.New("partition: nil claim or id")
	errNilHash  = errors.New("partition: nil content hash")
)

type partition struct {
	shards []ranke.Universe
}

// NewPartition routes keys across shards by hash(id) mod len(shards). At least
// one shard is required; the shard set is fixed for the partition's lifetime.
func NewPartition(shards ...ranke.Universe) (ranke.Universe, error) {
	if len(shards) == 0 {
		return nil, errNoShards
	}
	for _, s := range shards {
		if s == nil {
			return nil, errNilShard
		}
	}
	if len(shards) == 1 {
		// One shard needs no routing.
		return shards[0], nil
	}
	return &partition{shards: shards}, nil
}

// shardOf picks the shard for a key: the id is already a uniformly distributed
// hash, so shard = its string as base-256 mod N, by Horner (paper §Composing Universes).
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
			return errNilClaim
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
			return errNilHash
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

func (p *partition) GetClaims(ctx context.Context, ids []ranke.Id, opts ...ranke.GetOption) ([]ranke.Claim, error) {
	out := make([]ranke.Claim, len(ids))
	groups := p.groupIds(ids)
	for s, idx := range groups {
		if len(idx) == 0 {
			continue
		}
		if ranke.ReportEnabled(ctx, ranke.ReportDebug) {
			ranke.ReportEvent(ctx, "partition", "route", ranke.ReportDebug, "",
				map[string]any{"shard": s, "ids": len(idx)})
		}
		// Raw delta from the shard; materialised below so a diff's
		// predecessor resolves across shards.
		got, err := p.shards[s].GetClaims(ctx, at(ids, idx), ranke.WithNotDiffMaterialized())
		if err != nil {
			return nil, err
		}
		for k, c := range got {
			out[idx[k]] = c
		}
	}
	// Materialise diff overlays at the partition level, honouring read opts.
	return ranke.DefaultMaterialize(ctx, p, out, opts...)
}

func (p *partition) GetContents(ctx context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	out := make([][]byte, len(refs))
	groups := make([][]int, len(p.shards))
	for i, r := range refs {
		if r.Hash == nil {
			return nil, errNilHash
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
		return nil, errNilHash
	}
	return p.shards[p.shardOf(hash)].StreamContent(ctx, hash, size)
}

func (p *partition) CopyClaims(ctx context.Context, src ranke.Universe, ids []ranke.Id, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyClaims(ctx, p, src, ids, opts...)
}

func (p *partition) CopyContents(ctx context.Context, src ranke.Universe, refs []ranke.ContentRef, opts ...ranke.CopyOption) error {
	return ranke.DefaultCopyContents(ctx, p, src, refs, opts...)
}

// GetClaimHeights resolves heights through the partition's own routed GetClaims.
func (p *partition) GetClaimHeights(ctx context.Context, ids []ranke.Id) ([]uint64, error) {
	return ranke.DefaultGetClaimHeights(ctx, p, ids)
}

// Query resolves an RQL read through the partition's own routed read path.
func (p *partition) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	return ranke.DefaultQuery(ctx, p, q, scope)
}

// GetClaimsRaw routes each id to its shard and gathers the stored CBOR.
func (p *partition) GetClaimsRaw(ctx context.Context, ids []ranke.Id) ([][]byte, error) {
	out := make([][]byte, len(ids))
	groups := p.groupIds(ids)
	for s, idx := range groups {
		if len(idx) == 0 {
			continue
		}
		got, err := p.shards[s].GetClaimsRaw(ctx, at(ids, idx))
		if err != nil {
			return nil, err
		}
		for k, b := range got {
			out[idx[k]] = b
		}
	}
	return out, nil
}

// GetClaimTags routes each id to its shard (a claim's tags live wherever the
// claim does) and gathers them positionally.
func (p *partition) GetClaimTags(ctx context.Context, claims []ranke.Id) ([]map[string]string, error) {
	out := make([]map[string]string, len(claims))
	groups := p.groupIds(claims)
	for s, idx := range groups {
		if len(idx) == 0 {
			continue
		}
		got, err := p.shards[s].GetClaimTags(ctx, at(claims, idx))
		if err != nil {
			return nil, err
		}
		for k, t := range got {
			out[idx[k]] = t
		}
	}
	return out, nil
}

// Tag broadcasts to every shard: a closure is spread across all of them.
func (p *partition) Tag(ctx context.Context, head ranke.Id) error {
	for sh := range p.shards {
		if err := p.shards[sh].Tag(ctx, head); err != nil {
			return err
		}
	}
	return nil
}

// SetClaimsTags groups the per-claim tag maps by shard; clearTags applies to each.
func (p *partition) SetClaimsTags(ctx context.Context, clearTags []string, tags map[string]map[string]string) error {
	groups := make([]map[string]map[string]string, len(p.shards))
	for s, kv := range tags {
		id, err := ranke.ParseId(s)
		if err != nil {
			return err
		}
		sh := p.shardOf(id)
		if groups[sh] == nil {
			groups[sh] = map[string]map[string]string{}
		}
		groups[sh][s] = kv
	}
	for sh, g := range groups {
		if len(g) == 0 {
			continue
		}
		if err := p.shards[sh].SetClaimsTags(ctx, clearTags, g); err != nil {
			return err
		}
	}
	return nil
}

func (p *partition) Capabilities() ranke.Capabilities {
	c := ranke.Capabilities{Overwrite: true, Delete: true, Enumerate: true, Persistent: true, ReverseWalk: true, RawClaims: true, ExternalContent: true, Tags: true}
	for i, s := range p.shards {
		sc := s.Capabilities()
		c.Overwrite = c.Overwrite && sc.Overwrite
		c.Delete = c.Delete && sc.Delete
		c.Enumerate = c.Enumerate && sc.Enumerate
		c.Persistent = c.Persistent && sc.Persistent
		c.ReverseWalk = c.ReverseWalk && sc.ReverseWalk
		c.RawClaims = c.RawClaims && sc.RawClaims
		c.ExternalContent = c.ExternalContent && sc.ExternalContent
		c.Tags = c.Tags && sc.Tags
		if i == 0 {
			// Shards share one tier; adopt the first shard's.
			c.Tier = sc.Tier
		}
	}
	return c
}

// Sync reports id as synced immediately.
func (p *partition) Sync(_ context.Context, _ ranke.Universe, id ranke.Id) <-chan ranke.SyncResult {
	// TODO: each shard holds an incomplete graph, so the partition must orchestrate.
	return ranke.SyncedNow(id)
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
