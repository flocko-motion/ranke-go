// package: neo4j / persistence-cache
// job:     link each write batch's diff overlays before it is projected, so the cache stores
// materialised claims only
// type:    logic
// limits:  write-path only; reads need no overlay pass because what is stored is already
// materialised (-> neo4j.go)
package neo4j

import (
	"context"

	"github.com/flocko-motion/ranke-go"
)

// materializeForWrite links the contribution/diff overlay of every claim in cs.
// Predecessors first, so each claim's own link is a single read of an
// already-linked predecessor: within the batch because this pass got there first,
// from the graph because what is stored is already materialised.
func (u *neo4jUniverse) materializeForWrite(ctx context.Context, cs []ranke.Claim) error {
	batch := make(map[string]ranke.Claim, len(cs))
	for _, c := range cs {
		if c != nil && c.ID() != nil {
			batch[c.ID().String()] = c
		}
	}
	src := batchFirst{Universe: u, batch: batch}
	done := make(map[string]bool, len(cs))
	for _, c := range cs {
		if err := link(ctx, src, batch, done, c); err != nil {
			return err
		}
	}
	return nil
}

// link resolves c's predecessor chain depth-first, then c — so no claim is ever
// linked twice, and no link walks a chain that is already resolved. Only the batch
// is descended: a predecessor already in the graph is materialised there.
func link(ctx context.Context, src batchFirst, batch map[string]ranke.Claim, done map[string]bool, c ranke.Claim) error {
	if c == nil || c.ID() == nil || done[c.ID().String()] {
		return nil
	}
	done[c.ID().String()] = true
	for _, e := range c.Edges(ranke.EdgeFilterType{Type: ranke.EdgeTypeDiff}) {
		if pred, ok := batch[e.Reference().String()]; ok {
			if err := link(ctx, src, batch, done, pred); err != nil {
				return err
			}
		}
	}
	_, err := ranke.DefaultMaterialize(ctx, src, []ranke.Claim{c})
	return err
}

// batchFirst resolves a predecessor from the pending write batch before the graph —
// the predecessor of a delta being written is typically in the same batch, so it
// cannot be read back yet.
type batchFirst struct {
	ranke.Universe // the cache, for predecessors already stored (already materialised)
	batch          map[string]ranke.Claim
}

// GetClaims answers from the batch where it can, the graph otherwise. It adds no
// overlay pass of its own: callers reach a claim only after its predecessors are
// linked, and the graph stores materialised claims.
func (b batchFirst) GetClaims(ctx context.Context, ids []ranke.Id, opts ...ranke.GetOption) ([]ranke.Claim, error) {
	out := make([]ranke.Claim, len(ids))
	var missIDs []ranke.Id
	var missAt []int
	for i, id := range ids {
		if id == nil {
			return nil, errNilID
		}
		if c, ok := b.batch[id.String()]; ok {
			out[i] = c
			continue
		}
		missIDs = append(missIDs, id)
		missAt = append(missAt, i)
	}
	if len(missIDs) == 0 {
		return out, nil
	}
	got, err := b.Universe.GetClaims(ctx, missIDs, opts...)
	if err != nil {
		return nil, err
	}
	for j, c := range got {
		out[missAt[j]] = c
	}
	return out, nil
}
