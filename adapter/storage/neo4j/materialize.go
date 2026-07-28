// package: neo4j / persistence-cache
// job:     resolve a write batch's diff overlays before it is projected, so the cache stores materialised claims only
// type:    logic
// limits:  write-path only; reads need no overlay pass because what is stored is already materialised (-> neo4j.go)
package neo4j

import (
	"context"

	"github.com/flocko-motion/ranke-go"
)

// materializeForWrite resolves the contribution/diff overlay of every claim in cs.
// A delta's predecessor is usually in the same batch (references precede
// referrers) and not yet stored, so resolution looks in the batch first and only
// then in the graph.
func (u *neo4jUniverse) materializeForWrite(ctx context.Context, cs []ranke.Claim) error {
	batch := make(map[string]ranke.Claim, len(cs))
	for _, c := range cs {
		if c != nil && c.ID() != nil {
			batch[c.ID().String()] = c
		}
	}
	_, err := ranke.DefaultMaterialize(ctx, batchFirst{Universe: u, batch: batch}, cs)
	return err
}

// batchFirst resolves claims from a pending write batch before falling back to the
// graph — the predecessor of a delta being written is typically in the same batch,
// so it cannot be read back yet.
type batchFirst struct {
	ranke.Universe // the cache, for predecessors already stored
	batch          map[string]ranke.Claim
}

// GetClaims answers from the batch where it can, the graph otherwise, and
// materialises what it returns so a chain deeper than one link resolves.
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
	if len(missIDs) > 0 {
		got, err := b.Universe.GetClaims(ctx, missIDs, opts...)
		if err != nil {
			return nil, err
		}
		for j, c := range got {
			out[missAt[j]] = c
		}
	}
	return ranke.DefaultMaterialize(ctx, b, out, opts...)
}
