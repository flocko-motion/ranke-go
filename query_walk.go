// package: ranke / query walk
// type:    logic
// job:     the reference RQL traversal — walk a Select's Path from its root (forward, and reverse via a built closure-inversion index), returning the reached set and each claim's canonical route
// limits:  reachability only; filter/order/limit/shape live in query_default.go. Route ties break on the (created_at, id) total order so a path shape is byte-identical to a Cypher lowering
package ranke

import (
	"context"
	"path"
	"strings"
)

// queryTraverse walks the Select from its root claim, one PathStep at a time,
// and returns the reached claim set (deduped) plus, when needPaths is set, the
// root→claim route for each. An empty Path is one implicit unbounded
// provenance step — the full closure.
func queryTraverse(ctx context.Context, u Universe, sel Select, head Id, needPaths bool, rc *reportCollector) ([]Claim, map[string][]Claim, error) {
	rootStart := reportStart(rc)
	root, err := GetClaim(ctx, u, sel.Claim)
	if err != nil {
		return nil, nil, wrapDetail(errQuery, "root "+sel.Claim.String(), err)
	}
	rc.timed("native", "load-root", ReportInfo, rootStart, sel.Claim.String(), nil)

	steps := sel.Path
	if len(steps) == 0 {
		steps = []PathStep{{}} // all edges, provenance, unbounded → full closure
	}

	// A reverse step needs incoming edges: invert the scope closure (from head,
	// or the walk root when unconfined). Spanning only closure(head) confines it.
	var incoming map[string][]incomingEdge
	if stepsNeedReverse(steps) {
		anchor := root
		if head != nil && !head.Equal(sel.Claim) {
			anchor, err = GetClaim(ctx, u, head)
			if err != nil {
				return nil, nil, wrapDetail(errQuery, "scope head "+head.String(), err)
			}
		}
		idxStart := reportStart(rc)
		incoming, err = buildIncoming(ctx, u, anchor, rc)
		if err != nil {
			return nil, nil, err
		}
		rc.timed("native", "reverse-index", ReportInfo, idxStart, "", map[string]any{"targets": len(incoming)})
	}

	frontier := []Claim{root}
	routes := map[string][]Claim{root.ID().String(): {root}}
	var reached []Claim
	for i, step := range steps {
		stepStart := reportStart(rc)
		reached, err = queryWalkStep(ctx, u, frontier, step, incoming, routes, needPaths, rc)
		if err != nil {
			return nil, nil, err
		}
		rc.timed("native", "step", ReportInfo, stepStart, "", map[string]any{"index": i, "edges": step.Edges, "dir": string(step.Dir), "depth": step.Depth, "reached": len(reached)})
		frontier = reached
	}
	return reached, routes, nil
}

// stepsNeedReverse reports whether any step walks edges backward (uses or
// connections), so the traversal must build a reverse index first.
func stepsNeedReverse(steps []PathStep) bool {
	for _, s := range steps {
		if s.Dir == DirUses || s.Dir == DirConnections {
			return true
		}
	}
	return false
}

// incomingEdge is one reverse-adjacency entry: a claim that references the
// target, and the type of the edge it did so with.
type incomingEdge struct {
	from     Claim
	edgeType string
}

// buildIncoming materialises the forward closure from root and inverts its edges
// into a target-id → referrers map — the reference's reverse index. Expensive (a
// full closure sweep) but simple and correct; every referrer it records is
// within the closure, so it doubles as the scope-membership set.
func buildIncoming(ctx context.Context, u Universe, root Claim, rc *reportCollector) (map[string][]incomingEdge, error) {
	incoming := map[string][]incomingEdge{}
	seen := map[string]bool{root.ID().String(): true}
	queue := []Claim{root}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		for _, e := range cur.Edges() {
			ref := e.Reference()
			k := ref.String()
			incoming[k] = append(incoming[k], incomingEdge{from: cur, edgeType: e.Type()})
			if seen[k] {
				continue
			}
			seen[k] = true
			child, err := GetClaim(ctx, u, ref)
			if err != nil {
				return nil, wrapDetail(errQuery, "reverse-index "+k, err)
			}
			rc.log("native", "fetch", ReportTrace, k, nil)
			queue = append(queue, child)
		}
	}
	return incoming, nil
}

// queryWalkStep expands the frontier along one PathStep: level-synchronized BFS,
// up to step.Depth hops (0 = unbounded), collecting every visited claim whose
// node type matches step.Nodes (the frontier counts as hop 0, per the paper's
// *0..depth semantics). Direction selects which edges: DirProvenance (default)
// follows outgoing edges forward; DirUses follows incoming edges (from the
// reverse index); DirConnections does both. Reverse steps require incoming to be
// built (queryTraverse). A node reached by several equal-length routes keeps the
// canonical one — minimal under (length, then (created_at, id) per node), the
// same total order as the default sort — so a path shape is deterministic and a
// Cypher lowering can reproduce it byte-for-byte with a matching ORDER BY.
func queryWalkStep(ctx context.Context, u Universe, frontier []Claim, step PathStep, incoming map[string][]incomingEdge, routes map[string][]Claim, needPaths bool, rc *reportCollector) ([]Claim, error) {
	seen := map[string]bool{}
	var level []Claim // the current BFS level; the frontier is hop 0
	for _, f := range frontier {
		if k := f.ID().String(); !seen[k] {
			seen[k] = true
			level = append(level, f)
		}
	}

	var out []Claim
	outSeen := map[string]bool{}
	collect := func(c Claim) {
		k := c.ID().String()
		if !outSeen[k] && matchTypeList(step.Nodes, c.Node().Type()) {
			outSeen[k] = true
			out = append(out, c)
		}
	}
	for _, c := range level {
		collect(c)
	}

	forward := step.Dir == "" || step.Dir == DirProvenance || step.Dir == DirConnections
	reverse := step.Dir == DirUses || step.Dir == DirConnections

	for hop := 0; len(level) > 0; hop++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if step.Depth > 0 && hop >= step.Depth {
			break
		}
		// Discover the next level from every node in this one, keeping the
		// canonical route per child before finalising it — so a second parent
		// reaching the same child at this level can still win the tie. Forward
		// follows outgoing edges; reverse follows the incoming index.
		type cand struct {
			claim Claim
			route []Claim // nil unless needPaths
		}
		best := map[string]cand{} // child id → best candidate so far
		var order []string        // children first seen this level, for stable finalisation
		consider := func(cur, child Claim) {
			k := child.ID().String()
			if seen[k] {
				return // finalised at a shorter level — that shorter route wins
			}
			var route []Claim
			if needPaths {
				parent := routes[cur.ID().String()]
				route = make([]Claim, len(parent), len(parent)+1)
				copy(route, parent)
				route = append(route, child)
			}
			prev, ok := best[k]
			if !ok {
				best[k] = cand{child, route}
				order = append(order, k)
			} else if needPaths && lessRoute(route, prev.route) {
				best[k] = cand{child, route}
			}
		}
		for _, cur := range level {
			if forward {
				for _, e := range cur.Edges() {
					if !matchTypeList(step.Edges, e.Type()) {
						continue
					}
					ref := e.Reference()
					if seen[ref.String()] {
						continue
					}
					child, err := GetClaim(ctx, u, ref)
					if err != nil {
						return nil, wrapDetail(errQuery, "traverse "+ref.String(), err)
					}
					rc.log("native", "fetch", ReportTrace, ref.String(), nil)
					consider(cur, child)
				}
			}
			if reverse {
				// Incoming edges come from the reverse index, already within the
				// scope closure — no fetch, and no way to leave the branch.
				for _, ie := range incoming[cur.ID().String()] {
					if !matchTypeList(step.Edges, ie.edgeType) {
						continue
					}
					rc.log("native", "reverse", ReportTrace, ie.from.ID().String(), nil)
					consider(cur, ie.from)
				}
			}
		}
		var next []Claim
		for _, k := range order {
			seen[k] = true
			c := best[k]
			if needPaths {
				routes[k] = c.route
			}
			next = append(next, c.claim)
			collect(c.claim)
		}
		level = next
	}
	return out, nil
}

// lessRoute reports whether route a sorts before b under the query total order:
// shorter first, then node-by-node on (created_at, id) — the same key the
// default sort and the Cypher path ORDER BY use, so all three agree.
func lessRoute(a, b []Claim) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	for i := range a {
		ta, tb := a[i].Node().CreatedAt(), b[i].Node().CreatedAt()
		if !ta.Equal(tb) {
			return ta.Before(tb)
		}
		if ida, idb := a[i].ID().String(), b[i].ID().String(); ida != idb {
			return ida < idb
		}
	}
	return false
}

// matchTypeList reports whether typ satisfies a type-pattern list: a leading
// "-" excludes, patterns are path.Match globs over "class/sub". No patterns, or
// only negatives, means "match unless excluded".
func matchTypeList(patterns []string, typ string) bool {
	if len(patterns) == 0 {
		return true
	}
	hasPositive, matchedPositive := false, false
	for _, p := range patterns {
		neg := strings.HasPrefix(p, "-")
		pat := strings.TrimPrefix(p, "-")
		ok, _ := path.Match(pat, typ)
		if neg {
			if ok {
				return false
			}
			continue
		}
		hasPositive = true
		if ok {
			matchedPositive = true
		}
	}
	return !hasPositive || matchedPositive
}
