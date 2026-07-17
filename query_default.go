// package: ranke / query
// type:    logic
// job:     DefaultQuery — the reference RQL executor a byte-store Universe delegates to: a correct forward-closure walk with filter/order/limit/shape
// limits:  performance-ignorant; a graph-native backend overrides with a native lowering (-> adapter/storage/neo4j)
package ranke

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

// DefaultQuery is the reference implementation of Universe.Query for a backend
// with no native query engine. It reads only through the public Universe API
// (GetClaim + edges), so it works over any byte store. It is deliberately
// simple and not performance-tuned: it walks the closure, materialises every
// candidate, then filters, orders, limits, and shapes — the meaning a capable
// backend's native lowering must reproduce. A byte store delegates here; a
// graph-native backend (neo4j) overrides Universe.Query.
func DefaultQuery(ctx context.Context, u Universe, q Query, scope Scope) (ResultStream, error) {
	start := time.Now()
	if q.Select.Branch == "" {
		return nil, errQueryNoScope // scope is mandatory — use BranchUniverse for unconfined reads.
	}
	// Claim is the required walk root (the Archive resolves Branch → head into it).
	if q.Select.Claim == nil {
		return nil, errQueryNoRoot
	}
	ctx, rc, createdReport := beginReport(ctx, q.Execution.Report, start)
	rc.log("native", "select", ReportInfo, "", map[string]any{"branch": q.Select.Branch, "root": q.Select.Claim.String()})
	if q.Limit.Time > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Limit.Time)
		defer cancel()
	}

	needPaths := q.Output.Detail == DetailPath
	reached, routes, err := queryTraverse(ctx, u, q.Select, scope.Head, needPaths, rc)
	if err != nil {
		return nil, err
	}

	// Filter by the Where tree (scope confinement happened during the walk).
	filterStart := reportStart(rc)
	var filtered []Claim
	for _, c := range reached {
		if !evalWhere(q.Where, c) {
			continue
		}
		filtered = append(filtered, c)
	}
	rc.timed("native", "filter", ReportInfo, filterStart, "", map[string]any{"in": len(reached), "kept": len(filtered)})

	sortStart := reportStart(rc)
	sortResults(filtered, q.Order)
	rc.timed("native", "sort", ReportInfo, sortStart, "", map[string]any{"ordered": q.Order != nil})

	truncated := false
	if q.Limit.Results > 0 && len(filtered) > q.Limit.Results {
		filtered = filtered[:q.Limit.Results]
		truncated = true
	}
	if truncated {
		rc.log("native", "limit", ReportInfo, "", map[string]any{"results": q.Limit.Results, "truncated": true})
	}

	results := make([]QueryResult, 0, len(filtered))
	for _, c := range filtered {
		r := QueryResult{Claim: c}
		if needPaths {
			r.Path = routes[c.ID().String()]
		}
		if q.Output.Content > 0 {
			r.Content = queryContent(ctx, u, c, q.Output)
		}
		results = append(results, r)
	}

	rc.log("native", "results", ReportInfo, "", map[string]any{"results": len(results)})
	var report *QueryReport
	if createdReport {
		report = rc.finalize(len(results), truncated)
	}
	return &sliceStream{results: results, report: report}, nil
}

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

// queryWalkStep expands the frontier along one PathStep: BFS following edges
// whose type matches step.Edges, up to step.Depth hops (0 = unbounded),
// collecting every visited claim whose node type matches step.Nodes (the
// starting frontier counts as hop 0, per the paper's *0..depth semantics).
// Direction selects which edges: DirProvenance (default) follows outgoing edges
// forward; DirUses follows incoming edges (from the reverse index); DirConnections
// does both. Reverse steps require incoming to be built (queryTraverse).
func queryWalkStep(ctx context.Context, u Universe, frontier []Claim, step PathStep, incoming map[string][]incomingEdge, routes map[string][]Claim, needPaths bool, rc *reportCollector) ([]Claim, error) {
	type item struct {
		c   Claim
		hop int
	}
	seen := map[string]bool{}
	var queue []item
	for _, f := range frontier {
		if k := f.ID().String(); !seen[k] {
			seen[k] = true
			queue = append(queue, item{f, 0})
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
	enqueue := func(child, parent Claim, hop int) {
		k := child.ID().String()
		if seen[k] {
			return
		}
		seen[k] = true
		if needPaths {
			pr := routes[parent.ID().String()]
			route := make([]Claim, len(pr), len(pr)+1)
			copy(route, pr)
			routes[k] = append(route, child)
		}
		queue = append(queue, item{child, hop})
	}

	forward := step.Dir == "" || step.Dir == DirProvenance || step.Dir == DirConnections
	reverse := step.Dir == DirUses || step.Dir == DirConnections

	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		collect(cur.c)
		if step.Depth > 0 && cur.hop >= step.Depth {
			continue
		}
		if forward {
			for _, e := range cur.c.Edges() {
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
				enqueue(child, cur.c, cur.hop+1)
			}
		}
		if reverse {
			// Incoming edges come from the reverse index, already within the
			// scope closure — no fetch, and no way to leave the branch.
			for _, ie := range incoming[cur.c.ID().String()] {
				if !matchTypeList(step.Edges, ie.edgeType) {
					continue
				}
				rc.log("native", "reverse", ReportTrace, ie.from.ID().String(), nil)
				enqueue(ie.from, cur.c, cur.hop+1)
			}
		}
	}
	return out, nil
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

// --- Where evaluation ------------------------------------------------------

// evalWhere evaluates the boolean tree against one claim. A nil / empty node
// passes; a leaf tests the resolved field value.
func evalWhere(w *Where, c Claim) bool {
	if w == nil {
		return true
	}
	switch {
	case len(w.And) > 0:
		for i := range w.And {
			if !evalWhere(&w.And[i], c) {
				return false
			}
		}
		return true
	case len(w.Or) > 0:
		for i := range w.Or {
			if evalWhere(&w.Or[i], c) {
				return true
			}
		}
		return false
	case w.Not != nil:
		return !evalWhere(w.Not, c)
	case w.Test != nil:
		v, ok := queryFieldValue(c, w.Field)
		if !ok {
			return false // a claim lacking the field never matches
		}
		return evalComparison(*w.Test, v)
	default:
		return true
	}
}

// queryFieldValue resolves a claim field by name — the seam between RQL and the
// node model. Reserved fields have dedicated accessors (this is where height
// becomes queryable); everything else is an open node field.
func queryFieldValue(c Claim, field string) (any, bool) {
	n := c.Node()
	switch field {
	case "id":
		return c.ID().String(), true
	case "type":
		return n.Type(), true
	case "encoding":
		return n.Encoding(), true
	case "created_at":
		return n.CreatedAt(), true
	case "content_size":
		return n.GetContentSize(), true
	case "height":
		return n.Height(), true
	default:
		v, err := n.GetField(field)
		if err != nil {
			return nil, false
		}
		return v, true
	}
}

// evalComparison applies the one operator set on cmp to the actual field value.
func evalComparison(cmp Comparison, actual any) bool {
	switch {
	case cmp.Eq != nil:
		return valuesEqual(actual, cmp.Eq)
	case cmp.Ne != nil:
		return !valuesEqual(actual, cmp.Ne)
	case cmp.Lt != nil:
		c, ok := compareOrdered(actual, cmp.Lt)
		return ok && c < 0
	case cmp.Le != nil:
		c, ok := compareOrdered(actual, cmp.Le)
		return ok && c <= 0
	case cmp.Gt != nil:
		c, ok := compareOrdered(actual, cmp.Gt)
		return ok && c > 0
	case cmp.Ge != nil:
		c, ok := compareOrdered(actual, cmp.Ge)
		return ok && c >= 0
	case cmp.In != nil:
		for _, w := range cmp.In {
			if valuesEqual(actual, w) {
				return true
			}
		}
		return false
	case cmp.Glob != "":
		ok, _ := path.Match(cmp.Glob, toStringValue(actual))
		return ok
	}
	return false
}

// valuesEqual compares two values with light coercion: numbers by magnitude,
// times as instants, everything else by string form (covers strings, bools).
func valuesEqual(a, b any) bool {
	if af, aok := asFloat(a); aok {
		if bf, bok := asFloat(b); bok {
			return af == bf
		}
	}
	if at, aok := asTime(a); aok {
		if bt, bok := asTime(b); bok {
			return at.Equal(bt)
		}
	}
	return toStringValue(a) == toStringValue(b)
}

// compareOrdered returns -1/0/1 and whether the two values are order-comparable
// (both numeric, both times/RFC3339, or both strings).
func compareOrdered(a, b any) (int, bool) {
	if af, aok := asFloat(a); aok {
		if bf, bok := asFloat(b); bok {
			switch {
			case af < bf:
				return -1, true
			case af > bf:
				return 1, true
			default:
				return 0, true
			}
		}
	}
	if at, aok := asTime(a); aok {
		if bt, bok := asTime(b); bok {
			switch {
			case at.Before(bt):
				return -1, true
			case at.After(bt):
				return 1, true
			default:
				return 0, true
			}
		}
	}
	as, aok := a.(string)
	bs, bok := b.(string)
	if aok && bok {
		return strings.Compare(as, bs), true
	}
	return 0, false
}

func asFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	default:
		return 0, false
	}
}

func asTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		if p, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return p, true
		}
		if p, err := time.Parse(time.RFC3339, t); err == nil {
			return p, true
		}
	}
	return time.Time{}, false
}

func toStringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// --- ordering, content, stream ---------------------------------------------

// sortResults orders claims by the query's Order, or by (created_at, id) when
// none is given. Claims lacking the ordered field sort last.
func sortResults(claims []Claim, order *Order) {
	sort.SliceStable(claims, func(i, j int) bool {
		ci, cj := claims[i], claims[j]
		if order == nil {
			ti, tj := ci.Node().CreatedAt(), cj.Node().CreatedAt()
			if !ti.Equal(tj) {
				return ti.Before(tj)
			}
			return ci.ID().String() < cj.ID().String()
		}
		vi, oki := queryFieldValue(ci, order.Field)
		vj, okj := queryFieldValue(cj, order.Field)
		if oki != okj {
			return oki // present before absent, regardless of direction
		}
		if !oki {
			return ci.ID().String() < cj.ID().String()
		}
		c, ok := compareOrdered(vi, vj)
		if !ok {
			c = strings.Compare(toStringValue(vi), toStringValue(vj))
		}
		if order.Desc {
			return c > 0
		}
		return c < 0
	})
}

// queryContent loads up to Output.Content bytes of a claim's content, applying
// the overflow policy when the content is larger. Reads through u so external
// content is fetched transparently; a read error yields no content.
func queryContent(ctx context.Context, u Universe, c Claim, out Output) []byte {
	n := c.Node()
	inline, _ := n.GetInlineContent() // nil for external or no content
	if inline == nil && n.GetContentHash() == nil {
		return nil // no content
	}
	rdr, err := c.GetContent(ctx, u) // claim in hand, no scope check
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(rdr, out.Content+1))
	if err != nil {
		return nil
	}
	if int64(len(data)) <= out.Content {
		return data
	}
	switch out.Overflow {
	case OverflowOmit:
		return nil
	case OverflowReference:
		// External content carries its address; inline content has none in the
		// id (§Content), so its reference is H(content), computed on demand.
		hash := n.GetContentHash()
		if hash == nil {
			hash, err = HashContent(inline)
			if err != nil {
				return nil
			}
		}
		return []byte(hash.String())
	default: // OverflowCutoff
		return data[:out.Content]
	}
}

// sliceStream is the reference ResultStream: a fully materialised slice handed
// out one item at a time. Performance-ignorant by design.
type sliceStream struct {
	results []QueryResult
	i       int
	report  *QueryReport
}

func (s *sliceStream) Next() bool {
	if s.i < len(s.results) {
		s.i++
		return true
	}
	return false
}
func (s *sliceStream) Result() QueryResult  { return s.results[s.i-1] }
func (s *sliceStream) Report() *QueryReport { return s.report }
func (s *sliceStream) Err() error           { return nil }
func (s *sliceStream) Close() error         { return nil }

// GetFromClosure returns the claim at id when it is reachable within scope
// branch from any of heads, else ErrNotFound. It replaces the old
// Universe.GetFromClosure method as a package helper: a reference-edge walk from
// the heads that stops at id, so a head is reachable even if its own closure
// dangles — a gap is a real error only when the walk must cross it to reach id.
// The branch scope (BranchUniverse/BranchArchive/a name) is the hint a
// Tags-capable backend would use to accelerate this; the reference walk honours
// reachability directly and ignores it. heads are the scope roots (usually one;
// a multi-headed graph passes several, unioned by the shared visited set).
func GetFromClosure(ctx context.Context, u Universe, branch string, heads []Id, id Id) (Claim, error) {
	if id == nil {
		return nil, errNilID
	}
	seen := map[string]struct{}{}
	queue := make([]Id, 0, len(heads))
	for _, h := range heads {
		if h != nil {
			queue = append(queue, h)
		}
	}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		cur := queue[0]
		queue = queue[1:]
		k := cur.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		if cur.Equal(id) {
			return GetClaim(ctx, u, id) // reached — materialise and return, no expansion
		}
		c, err := GetClaim(ctx, u, cur, WithNotDiffMaterialized())
		if err != nil {
			return nil, err // a gap on the path to id is a real error
		}
		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
		}
	}
	return nil, ErrNotFound
}

// InClosure reports whether id is reachable within scope branch from any of
// heads — GetFromClosure without materialising the claim.
func InClosure(ctx context.Context, u Universe, branch string, heads []Id, id Id) (bool, error) {
	_, err := GetFromClosure(ctx, u, branch, heads, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
