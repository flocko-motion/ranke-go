// package: ranke / universe_closure
// type:    logic
// job:     DefaultInClosure / DefaultGetFromClosure — the fallback edge-walk a byte-store Universe uses to answer closure membership and lookup
// limits:  called only by Universe implementations (a graph-native backend overrides with a query); walks by following edge references, loading claims in delta form
package ranke

import "context"

// DefaultInClosure reports whether id is in head's closure — head and every
// claim reachable from it by following edge references — by walking the
// graph. It is the fallback a byte-store Universe uses for InClosure; a
// graph-native backend answers with a single query instead. Claims are
// loaded in delta form (WithNotDiffMaterialized) since only their edge
// references are needed, and the delta edges (including a contribution/diff
// edge) reach the full closure.
func DefaultInClosure(ctx context.Context, u Universe, head, id Id) (bool, error) {
	if head == nil || id == nil {
		return false, errNilID
	}
	seen := map[string]struct{}{}
	queue := []Id{head}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		cur := queue[0]
		queue = queue[1:]
		k := cur.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		if cur.Equal(id) {
			return true, nil
		}
		c, err := GetClaim(ctx, u, cur, WithNotDiffMaterialized())
		if err != nil {
			return false, err // a gap in the closure is a real error here
		}
		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
		}
	}
	return false, nil
}

// DefaultGetFromClosure returns the claim at id when it is in head's closure
// (materialised, like any read), else ErrNotFound. The fallback a byte-store
// Universe uses for GetFromClosure.
func DefaultGetFromClosure(ctx context.Context, u Universe, head, id Id) (Claim, error) {
	in, err := DefaultInClosure(ctx, u, head, id)
	if err != nil {
		return nil, err
	}
	if !in {
		return nil, ErrNotFound
	}
	return GetClaim(ctx, u, id)
}
