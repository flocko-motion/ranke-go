// package: neo4j / query
// type:    adapter
// job:     native RQL execution — lower the whole query to one Cypher statement, run it, reconstruct + stream (neo4j has an engine, never uses DefaultQuery)
// limits:  field read → scan; path read → walk from Select.Claim, or from anywhere in the closure when it names none; branch confinement is the _b_<branch> tag (<= Height, point-in-time), not a walk
package neo4j

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flocko-motion/ranke-go"
	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Query lowers the whole RQL read to one Cypher statement, runs it, then
// reconstructs and streams the matched claims (routes too, for the path shape).
func (u *neo4jUniverse) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	start := time.Now()
	needPaths := q.Output.Shape == ranke.ShapePath
	rep := newReport(q.Execution.Report, start)

	cypher, params := lowerCypher(q, scope, needPaths)
	rep.log("cypher", "lower", ranke.ReportDebug, 0, cypher)

	execStart := time.Now()
	res, err := u.query(ctx, cypher, params)
	if err != nil {
		return nil, fmt.Errorf("%w: execute: %w", errQuery, err)
	}
	rep.log("cypher", "execute", ranke.ReportInfo, time.Since(execStart), "")

	recStart := time.Now()
	var results []ranke.QueryResult
	switch {
	case needPaths:
		ps, rerr := u.reachedPaths(res.Records)
		if rerr != nil {
			return nil, rerr
		}
		if q.Output.Detail == ranke.DetailID {
			for i := range ps {
				ps[i].Claim = nil // the route carries the identities
			}
		}
		results = ps
	case q.Output.Detail == ranke.DetailID:
		// ids only — no reconstruction at all.
		for _, r := range res.Records {
			id, perr := ranke.ParseId(asString(valOf(r, "id")))
			if perr != nil {
				return nil, perr
			}
			results = append(results, ranke.QueryResult{Id: id})
		}
	default: // graph or claims: build from this query's own node records.
		claims, rerr := u.claimsFromRecords(res.Records)
		if rerr != nil {
			return nil, rerr
		}
		for _, c := range claims {
			r := ranke.QueryResult{Id: c.ID(), Claim: c}
			if q.Output.Content != nil {
				r.Content = ranke.ShapeContent(ctx, u, c, q.Output)
			}
			results = append(results, r)
		}
	}
	rep.log("neo4j", "reconstruct", ranke.ReportInfo, time.Since(recStart), strconv.Itoa(len(results))+" results")
	return &cypherStream{results: results, report: rep.finalize(len(results))}, nil
}

// reachedPaths assembles one result per record from its route's node data: a claim
// per route element, the last being the endpoint.
func (u *neo4jUniverse) reachedPaths(records []*neo4jdriver.Record) ([]ranke.QueryResult, error) {
	out := make([]ranke.QueryResult, 0, len(records))
	for _, r := range records {
		hops, ok := valOf(r, "route").([]any)
		if !ok {
			return nil, fmt.Errorf("%w: route is not a list", errQuery)
		}
		route := make([]ranke.Claim, 0, len(hops))
		for _, h := range hops {
			m, _ := h.(map[string]any)
			c, err := claimFromData(m)
			if err != nil {
				return nil, err
			}
			route = append(route, c)
		}
		if len(route) == 0 {
			continue
		}
		end := route[len(route)-1]
		out = append(out, ranke.QueryResult{Id: end.ID(), Claim: end, Path: route})
	}
	return out, nil
}

// nodeData is the Cypher map projection of a node's full data (props, labels, out-edges).
func nodeData(v string) string {
	return "{node: properties(" + v + "), labels: labels(" + v + "), " +
		"edges: [(" + v + ")-[r]->(t) | {props: properties(r), rtype: type(r), ref: t.id}]}"
}

// claimFromData rebuilds a claim from a nodeData map.
func claimFromData(m map[string]any) (ranke.Claim, error) {
	props, _ := m["node"].(map[string]any)
	labels, _ := m["labels"].([]any)
	edges, _ := m["edges"].([]any)
	id, err := ranke.ParseId(asString(props["id"]))
	if err != nil {
		return nil, err
	}
	parts, err := partsFromNode(id, props, labels, edges)
	if err != nil {
		return nil, err
	}
	return ranke.AssembleClaim(parts)
}

// tagBounded reports whether the _b_<branch> tag bounds this scope, which holds
// for a real branch name.
func tagBounded(scope ranke.Scope) bool {
	return scope.Branch != "" && scope.Branch != ranke.BranchUniverse && scope.Branch != ranke.BranchArchive
}

// closureAnchor is the claim whose reach bounds a read: Select.Head, else
// $archive's branch-table head. A tag-bounded scope needs none.
func closureAnchor(q ranke.Query, scope ranke.Scope) ranke.Id {
	if q.Select.Head != nil {
		return q.Select.Head
	}
	if !tagBounded(scope) {
		return scope.Head
	}
	return nil
}

// startClause binds n0, where a traversal's first segment starts: the claim
// Select.Claim names, else any claim the closure holds.
func startClause(q ranke.Query, scope ranke.Scope, params map[string]any) string {
	if q.Select.Claim != nil {
		params["root"] = q.Select.Claim.String()
		return "MATCH (n0 {id: $root})"
	}
	if anchor := closureAnchor(q, scope); anchor != nil {
		params["head"] = anchor.String()
		return "MATCH (h {id: $head})-[*0..]->(n0)"
	}
	return "MATCH (n0)"
}

// lowerCypher routes a query to its Cypher: a Path-less read is the scope's claim
// set (a scan), anything else follows the Path's steps.
func lowerCypher(q ranke.Query, scope ranke.Scope, needPaths bool) (string, map[string]any) {
	if len(q.Select.Path) == 0 {
		return scanCypher(q, scope) // no traversal: the scope's claim set
	}
	return traversalCypher(q, scope, needPaths)
}

// scanCypher lowers a Path-less read: the scope's claim set. Branch membership is
// the _b_<branch> tag (<= Height gives point-in-time); Head narrows to its reach.
func scanCypher(q ranke.Query, scope ranke.Scope) (string, map[string]any) {
	anchor := closureAnchor(q, scope)
	params := map[string]any{}
	match := "MATCH (n)"
	if anchor != nil {
		params["head"] = anchor.String()
		match = "MATCH (h {id: $head})-[*0..]->(n)\nWITH DISTINCT n"
	}
	var conds []string
	if tagBounded(scope) {
		params["bkey"] = ranke.BranchTagKey(scope.Branch)
		params["height"] = scope.Height
		conds = append(conds, "n[$bkey] <= $height")
	}
	conds = append(conds, "size(labels(n)) > 0")
	ctr := 0
	if wc := whereClause(q.Where, "n", params, &ctr); wc != "true" {
		conds = append(conds, wc)
	}
	return match + "\nWHERE " + strings.Join(conds, "\n  AND ") +
		"\nRETURN " + returnCols(q.Output.Detail, "n") +
		orderLimitClause(q.Order, q.Limit.Results, "n"), params
}

// returnCols is the RETURN projection for an endpoint node: the id for DetailID,
// else its full data (node/labels/edges) so the claim rebuilds from this query.
func returnCols(detail ranke.Detail, v string) string {
	if detail == ranke.DetailID {
		return v + ".id AS id"
	}
	return "properties(" + v + ") AS node, labels(" + v + ") AS labels, " +
		"[(" + v + ")-[r]->(t) | {props: properties(r), rtype: type(r), ref: t.id}] AS edges"
}

// claimsFromRecords rebuilds claims in-process from a query's node records
// (props, labels, edges).
func (u *neo4jUniverse) claimsFromRecords(records []*neo4jdriver.Record) ([]ranke.Claim, error) {
	out := make([]ranke.Claim, 0, len(records))
	for _, r := range records {
		props, _ := valOf(r, "node").(map[string]any)
		labels, _ := valOf(r, "labels").([]any)
		edges, _ := valOf(r, "edges").([]any)
		id, err := ranke.ParseId(asString(props["id"]))
		if err != nil {
			return nil, err
		}
		parts, err := partsFromNode(id, props, labels, edges)
		if err != nil {
			return nil, err
		}
		c, err := ranke.AssembleClaim(parts)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// traversalCypher lowers the Select's Path (direction, depth, edge/node filters
// per step). A reachability read (no path shape) is a per-step frontier pipeline
// (frontierCypher); a path shape reconstructs one continuous route per endpoint
// (pathCypher). Both treat a 0-length segment as including its start, so the start
// can pass through — matching DefaultQuery's walk.
func traversalCypher(q ranke.Query, scope ranke.Scope, needPaths bool) (string, map[string]any) {
	steps := q.Select.Path
	if len(steps) == 0 {
		steps = []ranke.PathStep{{}} // implicit: all edges, provenance, unbounded → full closure
	}
	params := map[string]any{}
	if needPaths {
		return pathCypher(q, scope, steps, params)
	}
	return frontierCypher(q, scope, steps, params)
}

// frontierCypher lowers a reachability read as a per-step frontier pipeline: each
// PathStep is its own MATCH … WITH DISTINCT stage, so relationship-uniqueness
// resets at every step boundary (a step may re-walk an edge an earlier step
// used). This mirrors the reference's independent per-step expansion — e.g. a
// forward step reaching a source does not block a later reverse step from walking
// that same edge back to the deriver.
func frontierCypher(q ranke.Query, scope ranke.Scope, steps []ranke.PathStep, params map[string]any) (string, map[string]any) {
	conf := tagBounded(scope)
	if conf {
		params["bkey"] = ranke.BranchTagKey(scope.Branch)
		params["height"] = scope.Height
	}
	final := "n" + strconv.Itoa(len(steps))
	var b strings.Builder
	b.WriteString(startClause(q, scope, params) + "\nWITH DISTINCT n0")
	for i, step := range steps {
		rv, nv := "r"+strconv.Itoa(i), "n"+strconv.Itoa(i+1)
		seg := segmentPattern("n"+strconv.Itoa(i), rv, nv, step)
		conds := segmentFilters(step, rv, nv, i, params)
		if conf { // confine every node on the segment (bounds reverse steps to members)
			pv := "p" + strconv.Itoa(i)
			seg = pv + " = " + seg
			conds = append(conds, "all(x IN nodes("+pv+") WHERE x[$bkey] <= $height)")
		}
		if i == len(steps)-1 { // endpoint: valid node + the where tree
			conds = append(conds, "size(labels("+nv+")) > 0")
			ctr := 0
			if wc := whereClause(q.Where, nv, params, &ctr); wc != "true" {
				conds = append(conds, wc)
			}
		}
		b.WriteString("\nMATCH " + seg)
		if len(conds) > 0 {
			b.WriteString("\nWHERE " + strings.Join(conds, "\n  AND "))
		}
		b.WriteString("\nWITH DISTINCT " + nv)
	}
	return b.String() +
		"\nRETURN " + returnCols(q.Output.Detail, final) +
		orderLimitClause(q.Order, q.Limit.Results, final), params
}

// pathCypher lowers a path-shape read as one continuous path, so a route can be
// reconstructed per endpoint. Relationship-uniqueness spans the whole path here:
// a route that re-crosses an edge is not a route. (Reachability uses the frontier
// lowering instead — that is where a step may reuse an earlier step's edge.)
func pathCypher(q ranke.Query, scope ranke.Scope, steps []ranke.PathStep, params map[string]any) (string, map[string]any) {
	var pat, where strings.Builder
	pat.WriteString("(n0)")
	addWhere := func(cond string) {
		if where.Len() > 0 {
			where.WriteString("\n  AND ")
		}
		where.WriteString(cond)
	}
	for i, step := range steps {
		rv, nv := "r"+strconv.Itoa(i), "n"+strconv.Itoa(i+1)
		// The chain omits the repeated head node between segments (a single pattern).
		pat.WriteString(segmentPattern("", rv, nv, step))
		for _, c := range segmentFilters(step, rv, nv, i, params) {
			addWhere(c)
		}
	}
	final := "n" + strconv.Itoa(len(steps))
	addWhere("size(labels(" + final + ")) > 0")
	ctr := 0
	if wc := whereClause(q.Where, final, params, &ctr); wc != "true" {
		addWhere(wc)
	}
	if tagBounded(scope) {
		params["bkey"] = ranke.BranchTagKey(scope.Branch)
		params["height"] = scope.Height
		addWhere("all(x IN nodes(path) WHERE x[$bkey] <= $height)")
	}
	// A single-segment path uses allShortestPaths (O(nodes), not per-route). It
	// refuses a pattern whose ends it cannot prove distinct, so a step that walks
	// at least one edge says so: the structural graph is acyclic, making the
	// inequality free.
	prelude := startClause(q, scope, params) + "\nWITH DISTINCT n0\n"
	match := prelude + "MATCH path=" + pat.String()
	if len(steps) == 1 {
		match = prelude + "MATCH path = allShortestPaths(" + pat.String() + ")"
		if steps[0].MinHops() > 0 {
			addWhere("n0 <> " + final)
		}
	}
	return match + "\nWHERE " + where.String() +
		"\nWITH " + final + ", path ORDER BY length(path), [x IN nodes(path) | x.created_at + x.id]" +
		"\nWITH " + final + ", head(collect([x IN nodes(path) | " + nodeData("x") + "])) AS route" +
		orderLimitClause(q.Order, q.Limit.Results, final) +
		"\nRETURN route", params
}

// segmentPattern renders one PathStep as a variable-length Cypher segment in its
// direction. prev is the anchor node name, or "" to omit it (chaining inside a
// single continuous pattern, where the previous segment already wrote the node).
func segmentPattern(prev, rv, nv string, step ranke.PathStep) string {
	bound := ""
	if step.Max > 0 {
		bound = strconv.Itoa(step.Max)
	}
	hops := "*" + strconv.Itoa(step.MinHops()) + ".." + bound
	head := "(" + prev + ")"
	if prev == "" {
		head = ""
	}
	switch step.Dir {
	case ranke.DirUses:
		return head + "<-[" + rv + hops + "]-(" + nv + ")"
	case ranke.DirConnections:
		return head + "-[" + rv + hops + "]-(" + nv + ")"
	default: // provenance (outgoing)
		return head + "-[" + rv + hops + "]->(" + nv + ")"
	}
}

// segmentFilters returns the edge-type (over rv) and endpoint node-type (over nv)
// conditions for one step; an empty list means "any". Regex params are recorded
// under per-step keys in params.
func segmentFilters(step ranke.PathStep, rv, nv string, i int, params map[string]any) []string {
	var conds []string
	if pos, neg := splitPatterns(step.Edges); len(pos) > 0 || len(neg) > 0 {
		if len(pos) > 0 {
			k := "ep" + strconv.Itoa(i)
			params[k] = pos
			conds = append(conds, "all(x IN "+rv+" WHERE any(re IN $"+k+" WHERE type(x) =~ re))")
		}
		if len(neg) > 0 {
			k := "en" + strconv.Itoa(i)
			params[k] = neg
			conds = append(conds, "all(x IN "+rv+" WHERE none(re IN $"+k+" WHERE type(x) =~ re))")
		}
	}
	if pos, neg := splitPatterns(step.Nodes); len(pos) > 0 || len(neg) > 0 {
		if len(pos) > 0 {
			k := "np" + strconv.Itoa(i)
			params[k] = pos
			conds = append(conds, "any(l IN labels("+nv+") WHERE any(re IN $"+k+" WHERE l =~ re))")
		}
		if len(neg) > 0 {
			k := "nn" + strconv.Itoa(i)
			params[k] = neg
			conds = append(conds, "none(l IN labels("+nv+") WHERE any(re IN $"+k+" WHERE l =~ re))")
		}
	}
	return conds
}

// splitPatterns turns a type-pattern list into positive and negative regex
// lists (a leading "-" excludes); globs become regex for Cypher's =~ full match.
func splitPatterns(patterns []string) (pos, neg []string) {
	for _, p := range patterns {
		if strings.HasPrefix(p, "-") {
			neg = append(neg, globToRegex(strings.TrimPrefix(p, "-")))
		} else {
			pos = append(pos, globToRegex(p))
		}
	}
	return pos, neg
}

func globToRegex(glob string) string {
	var b strings.Builder
	for _, r := range glob {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '.', '+', '(', ')', '[', ']', '{', '}', '^', '$', '|', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// intField is the set of fields neo4j stores as a native integer, so ordered
// comparisons and ORDER BY use them without coercion. The rest are strings; type
// is a label, handled specially.
var intField = map[string]bool{"height": true, "content_size": true}

// whereClause lowers a Where tree to a Cypher boolean over node. A nil tree is
// "true". p names the bound-parameter sink and ctr the next free index.
func whereClause(w *ranke.Where, node string, p map[string]any, ctr *int) string {
	if w == nil {
		return "true"
	}
	switch {
	case len(w.And) > 0:
		return "(" + joinClauses(w.And, " AND ", node, p, ctr) + ")"
	case len(w.Or) > 0:
		return "(" + joinClauses(w.Or, " OR ", node, p, ctr) + ")"
	case w.Not != nil:
		return "(NOT " + whereClause(w.Not, node, p, ctr) + ")"
	case w.Test != nil:
		return cmpClause(w.Field, *w.Test, node, p, ctr)
	default:
		return "true"
	}
}

func joinClauses(ws []ranke.Where, sep, node string, p map[string]any, ctr *int) string {
	parts := make([]string, len(ws))
	for i := range ws {
		parts[i] = whereClause(&ws[i], node, p, ctr)
	}
	return strings.Join(parts, sep)
}

// bind stores v under a fresh $wN parameter and returns its reference.
func bind(v any, p map[string]any, ctr *int) string {
	k := "w" + strconv.Itoa(*ctr)
	*ctr++
	p[k] = v
	return "$" + k
}

// cmpClause lowers one field comparison to Cypher, matching the reference's
// evalComparison: a claim lacking the field never matches (Cypher null propagates
// to false in WHERE), type is a label, int fields compare natively, other fields
// coerce to float when the operand is numeric (else string), mirroring
// compareOrdered's float-then-string fallback.
func cmpClause(field string, cmp ranke.Comparison, node string, p map[string]any, ctr *int) string {
	if field == "type" {
		return typeClause(cmp, node, p, ctr)
	}
	acc := node + "." + field // property accessor
	num := intField[field]
	ord := func(op string, v any) string {
		if num || isNumeric(v) {
			return "toFloat(" + acc + ") " + op + " " + bind(toFloatVal(v), p, ctr)
		}
		return acc + " " + op + " " + bind(v, p, ctr)
	}
	switch {
	case cmp.Eq != nil:
		return acc + " = " + bind(cmp.Eq, p, ctr)
	case cmp.Ne != nil:
		return acc + " <> " + bind(cmp.Ne, p, ctr)
	case cmp.Lt != nil:
		return ord("<", cmp.Lt)
	case cmp.Le != nil:
		return ord("<=", cmp.Le)
	case cmp.Gt != nil:
		return ord(">", cmp.Gt)
	case cmp.Ge != nil:
		return ord(">=", cmp.Ge)
	case cmp.In != nil:
		return acc + " IN " + bind(cmp.In, p, ctr)
	case cmp.Glob != "":
		return acc + " =~ " + bind(globToRegex(cmp.Glob), p, ctr)
	default:
		return "true"
	}
}

// typeClause lowers a comparison on the type field, which neo4j stores as a node
// label rather than a property.
func typeClause(cmp ranke.Comparison, node string, p map[string]any, ctr *int) string {
	labels := "labels(" + node + ")"
	switch {
	case cmp.Eq != nil:
		return bind(cmp.Eq, p, ctr) + " IN " + labels
	case cmp.Ne != nil:
		return "NOT " + bind(cmp.Ne, p, ctr) + " IN " + labels
	case cmp.In != nil:
		return "any(x IN " + bind(cmp.In, p, ctr) + " WHERE x IN " + labels + ")"
	case cmp.Glob != "":
		return "any(l IN " + labels + " WHERE l =~ " + bind(globToRegex(cmp.Glob), p, ctr) + ")"
	case cmp.Lt != nil:
		return "any(l IN " + labels + " WHERE l < " + bind(cmp.Lt, p, ctr) + ")"
	case cmp.Le != nil:
		return "any(l IN " + labels + " WHERE l <= " + bind(cmp.Le, p, ctr) + ")"
	case cmp.Gt != nil:
		return "any(l IN " + labels + " WHERE l > " + bind(cmp.Gt, p, ctr) + ")"
	case cmp.Ge != nil:
		return "any(l IN " + labels + " WHERE l >= " + bind(cmp.Ge, p, ctr) + ")"
	default:
		return "true"
	}
}

// orderLimitClause renders ORDER BY + LIMIT over node, matching the reference:
// (created_at, id) by default; otherwise the named field then id (a total order —
// the reference tiebreaks by id too), with missing values last in both directions.
func orderLimitClause(keys []ranke.OrderKey, limit int, node string) string {
	var b strings.Builder
	b.WriteString("\nORDER BY ")
	for _, k := range keys {
		b.WriteString(orderTerm(k, node) + ", ")
	}
	// Natural order (created_at, id) — the tiebreak, always last.
	b.WriteString(node + ".created_at, " + node + ".id")
	if limit > 0 {
		b.WriteString("\nLIMIT " + strconv.Itoa(limit))
	}
	return b.String()
}

// orderTerm renders one sort key as Cypher: missing values last, numeric keys
// coerced with toFloat, type sorted on its label.
func orderTerm(k ranke.OrderKey, node string) string {
	dir := ""
	if k.Dir == ranke.SortDesc {
		dir = " DESC"
	}
	if k.Field == "type" {
		lbl := "head(labels(" + node + "))"
		return lbl + " IS NULL, " + lbl + dir
	}
	acc := node + "." + k.Field
	key := acc
	if k.Compare == ranke.CompareNumeric || intField[k.Field] {
		key = "toFloat(" + acc + ")"
	}
	return acc + " IS NULL, " + key + dir
}

// isNumeric reports whether v is a Go numeric type (the operand kind that makes a
// comparison numeric, mirroring compareOrdered's asFloat path).
func isNumeric(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		return true
	default:
		return false
	}
}

// toFloatVal coerces a numeric any to float64 for a bound parameter.
func toFloatVal(v any) float64 {
	switch n := v.(type) {
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	case float64:
		return n
	case float32:
		return float64(n)
	default:
		return 0
	}
}

// --- result stream, report, content shaping (neo4j-native, no reference reuse) ---

// cypherStream is neo4j's ResultStream over an already-resolved slice — the query
// (filter/order/limit) ran in Cypher, so there is nothing left to do but hand
// rows out in order.
type cypherStream struct {
	results []ranke.QueryResult
	report  *ranke.QueryReport
	i       int
}

func (s *cypherStream) Next() bool {
	if s.i < len(s.results) {
		s.i++
		return true
	}
	return false
}
func (s *cypherStream) Result() ranke.QueryResult  { return s.results[s.i-1] }
func (s *cypherStream) Report() *ranke.QueryReport { return s.report }
func (s *cypherStream) Err() error                 { return nil }
func (s *cypherStream) Close() error               { return nil }

// reportBuilder collects execution events when a report is requested, gated by
// the level threshold. A zero level (no report) makes log/finalize no-ops.
type reportBuilder struct {
	level   ranke.ReportLevel
	started time.Time
	events  []ranke.QueryEvent
}

func newReport(level ranke.ReportLevel, started time.Time) *reportBuilder {
	return &reportBuilder{level: level, started: started}
}

func (r *reportBuilder) on() bool { return reportRank(r.level) > 0 }

func (r *reportBuilder) log(engine, op string, level ranke.ReportLevel, dur time.Duration, detail string) {
	if !r.on() || reportRank(level) > reportRank(r.level) {
		return
	}
	r.events = append(r.events, ranke.QueryEvent{
		At: time.Since(r.started), Engine: engine, Op: op, Level: level, Duration: dur, Detail: detail,
	})
}

func (r *reportBuilder) finalize(results int) *ranke.QueryReport {
	if !r.on() {
		return nil
	}
	return &ranke.QueryReport{StartedAt: r.started, Elapsed: time.Since(r.started), Results: results, Events: r.events}
}

func reportRank(l ranke.ReportLevel) int {
	switch l {
	case ranke.ReportError:
		return 1
	case ranke.ReportWarn:
		return 2
	case ranke.ReportInfo:
		return 3
	case ranke.ReportDebug:
		return 4
	case ranke.ReportTrace:
		return 5
	default:
		return 0
	}
}

