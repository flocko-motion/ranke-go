// package: ranke / query
// type:    logic
// job:     DefaultQuery — the reference RQL executor a byte-store Universe delegates to: a forward-closure walk (reverse via closure inversion) with filter/order/limit/shape
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

// scopeOrigin is where this walker enumerates a scope from: Select.Head, else the
// scope's own anchor. An index-free walker's own business, not a Select default.
func scopeOrigin(sel Select, scope Scope) (Id, error) {
	if sel.Head != nil {
		return sel.Head, nil
	}
	if scope.Head != nil {
		return scope.Head, nil
	}
	return nil, ErrQueryNoHead
}

// DefaultQuery is the reference implementation of Universe.Query for a backend
// with no native query engine. It reads only through the public Universe API
// (GetClaim + edges), so it works over any byte store. It is deliberately
// simple and not performance-tuned: it walks the closure, materialises every
// candidate, then filters, orders, limits, and shapes — the meaning a capable
// backend's native lowering must reproduce. A byte store delegates here; a
// graph-native backend (neo4j) overrides Universe.Query.
func DefaultQuery(ctx context.Context, u Universe, q Query, scope Scope) (ResultStream, error) {
	start := time.Now()
	ctx, rc, createdReport := beginReport(ctx, q.Execution.Report, start)
	rc.log("native", "select", ReportInfo, "", map[string]any{"branch": q.Select.Branch})
	if q.Limit.Time > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, q.Limit.Time)
		defer cancel()
	}

	needPaths := q.Output.Shape == ShapePath
	sel := q.Select
	switch {
	case len(sel.Path) == 0:
		// A scan: the scope's claim set, reached by closing over an anchor. The
		// anchor belongs to that set, so the sweep starts at zero hops.
		origin, err := scopeOrigin(sel, scope)
		if err != nil {
			return nil, err
		}
		sel.Claim = origin
		sel.Path = []PathStep{{Min: Hops(0)}}
		needPaths = false
	case sel.Claim == nil:
		return nil, ErrQueryUnanchored
	}
	reached, routes, err := queryTraverse(ctx, u, sel, scope.Head, needPaths, rc)
	if err != nil {
		return nil, err
	}
	return finishReached(ctx, u, q, reached, routes, rc, createdReport)
}

// finishReached is the reference post-traversal pipeline (Where, sort, limit,
// shape) over an already-generated set. Finalises the report
// if this call created it.
func finishReached(ctx context.Context, u Universe, q Query, reached []Claim, routes map[string][]Claim, rc *reportCollector, createdReport bool) (ResultStream, error) {
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
	rc.timed("native", "sort", ReportInfo, sortStart, "", map[string]any{"ordered": len(q.Order) > 0})

	truncated := false
	if q.Limit.Results > 0 && len(filtered) > q.Limit.Results {
		filtered = filtered[:q.Limit.Results]
		truncated = true
	}
	if truncated {
		rc.log("native", "limit", ReportInfo, "", map[string]any{"results": q.Limit.Results, "truncated": true})
	}

	needPaths := q.Output.Shape == ShapePath
	results := make([]QueryResult, 0, len(filtered))
	for _, c := range filtered {
		r := QueryResult{Id: c.ID(), Claim: c}
		if needPaths {
			r.Path = routes[c.ID().String()]
		}
		if q.Output.Content != nil {
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

// sortResults orders claims by the order keys (priority order), then the natural
// (created_at, id) order for ties. A claim lacking a key's field sorts last.
func sortResults(claims []Claim, keys []OrderKey) {
	sort.SliceStable(claims, func(i, j int) bool {
		ci, cj := claims[i], claims[j]
		for _, k := range keys {
			vi, oki := queryFieldValue(ci, k.Field)
			vj, okj := queryFieldValue(cj, k.Field)
			if oki != okj {
				return oki // present before absent, regardless of direction
			}
			if !oki {
				continue // both lack this key — try the next
			}
			c := compareValues(vi, vj, k.Compare)
			if c == 0 {
				continue
			}
			if k.Dir == SortDesc {
				return c > 0
			}
			return c < 0
		}
		// Natural order: (created_at, id) — a total order for stable ties.
		ti, tj := ci.Node().CreatedAt(), cj.Node().CreatedAt()
		if !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return ci.ID().String() < cj.ID().String()
	})
}

// compareValues compares two field values under the collation: numeric coerces to
// float (falling back to string when either isn't numeric); lexical and the empty
// default compare as strings, the empty default first trying numeric-aware order.
func compareValues(a, b any, col Collation) int {
	switch col {
	case CompareNumeric:
		if af, aok := asFloat(a); aok {
			if bf, bok := asFloat(b); bok {
				switch {
				case af < bf:
					return -1
				case af > bf:
					return 1
				default:
					return 0
				}
			}
		}
		return strings.Compare(toStringValue(a), toStringValue(b))
	case CompareLexical:
		return strings.Compare(toStringValue(a), toStringValue(b))
	default:
		if c, ok := compareOrdered(a, b); ok {
			return c
		}
		return strings.Compare(toStringValue(a), toStringValue(b))
	}
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
	data, err := io.ReadAll(io.LimitReader(rdr, out.Content.Max+1))
	if err != nil {
		return nil
	}
	if int64(len(data)) <= out.Content.Max {
		return data
	}
	switch out.Content.Overflow {
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
		return data[:out.Content.Max]
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
	// Fast path: the tagger stamps every branch member with the _b_<branch> tag,
	// so a present tag proves membership in O(1). A missing tag is inconclusive
	// (the backend may be untagged), so it falls through to the reference walk,
	// which honours reachability directly. Only for a real branch.
	if branch != "" && branch != BranchUniverse && branch != BranchArchive {
		if c, err := GetClaim(ctx, u, id); err == nil && c.Tag(BranchTagKey(branch)) != "" {
			return c, nil
		}
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
