// package: neo4j / query
// type:    adapter
// job:     native RQL execution — lower the whole query to one Cypher statement, run it, reconstruct + stream (neo4j has an engine, never uses DefaultQuery)
// limits:  field read → scan; path read → walk anchored at the root claim; branch confinement is the _b_<branch> tag (<= Height, point-in-time), not a walk
package neo4j

import (
	"context"
	"fmt"
	"io"
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
	if q.Select.Branch == "" {
		return nil, fmt.Errorf("%w: query has no scope", errQuery)
	}
	needPaths := q.Output.Shape == ranke.ShapePath
	if (needPaths || len(q.Select.Path) > 0) && q.Select.Claim == nil {
		return nil, fmt.Errorf("%w: a path query needs a root claim", errQuery)
	}
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
	case q.Output.Detail == ranke.DetailID:
		// ids only — no reconstruction at all.
		for _, r := range res.Records {
			id, perr := ranke.ParseId(asString(valOf(r, "id")))
			if perr != nil {
				return nil, perr
			}
			results = append(results, ranke.QueryResult{Id: id})
		}
	case needPaths:
		ps, rerr := u.reachedPaths(res.Records)
		if rerr != nil {
			return nil, rerr
		}
		results = ps
	default: // graph or claims: build from this query's own node records.
		claims, rerr := u.claimsFromRecords(res.Records)
		if rerr != nil {
			return nil, rerr
		}
		for _, c := range claims {
			r := ranke.QueryResult{Id: c.ID(), Claim: c}
			if q.Output.Content != nil {
				r.Content = u.shapeContent(ctx, c, q.Output.Content)
			}
			results = append(results, r)
		}
	}
	rep.log("neo4j", "reconstruct", ranke.ReportInfo, time.Since(recStart), strconv.Itoa(len(results))+" results")
	return &cypherStream{results: results, report: rep.finalize(len(results))}, nil
}

// reachedWithRoutes turns (route, id) records into endpoint claims plus each
// one's route (first row per endpoint wins), materialised in one GetClaims batch.
// reachedPaths builds one result per endpoint from its inline route node data:
// each route element rebuilds a claim, the last is the endpoint.
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

// confined reports whether scope is a real branch (has a _b_ tag); $universe/
// $archive are unrestricted (neo4j is already that snapshot).
func confined(scope ranke.Scope) bool {
	return scope.Branch != "" && scope.Branch != ranke.BranchUniverse && scope.Branch != ranke.BranchArchive
}

// lowerCypher routes a query to its Cypher. A no-path read confined to a branch
// is the branch's members — a tag scan (the closure equals the _b_<branch> set).
// Everything else walks from the root claim: an unconfined ($universe/$archive)
// no-path read is the root's reachable closure (not the whole cache), and a path
// read follows its steps.
func lowerCypher(q ranke.Query, scope ranke.Scope, needPaths bool) (string, map[string]any) {
	if !needPaths && len(q.Select.Path) == 0 && confined(scope) {
		return scanCypher(q, scope)
	}
	return traversalCypher(q, scope, needPaths)
}

// scanCypher answers a no-path read confined to a branch: the branch's members
// are exactly the _b_<branch> tag (<= Height, point-in-time), so it is a node
// scan on that tag — no traversal — plus Where/Order/Limit. Callers only reach
// it for a real branch (lowerCypher); unconfined reads walk from the root claim.
func scanCypher(q ranke.Query, scope ranke.Scope) (string, map[string]any) {
	params := map[string]any{"bkey": ranke.BranchTagKey(scope.Branch), "height": scope.Height}
	where := "n[$bkey] <= $height AND size(labels(n)) > 0"
	ctr := 0
	if wc := whereClause(q.Where, "n", params, &ctr); wc != "true" {
		where += "\n  AND " + wc
	}
	return "MATCH (n)\nWHERE " + where + "\nRETURN " + returnCols(q.Output.Detail, "n") +
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

// traversalCypher builds the reachability query for the Select: chain one
// variable-length segment per PathStep (direction, depth, edge/node type
// filters), and return the final endpoint's claim ids. Matches DefaultQuery's
// walk (a 0-length segment includes its start, so the root can pass through).
// When needPaths (path shape), it returns each matched route (nodes(path), root
// →endpoint) ordered shortest-first, so the caller keeps the reference's
// BFS-first route per endpoint.
func traversalCypher(q ranke.Query, scope ranke.Scope, needPaths bool) (string, map[string]any) {
	steps := q.Select.Path
	if len(steps) == 0 {
		steps = []ranke.PathStep{{}} // implicit: all edges, provenance, unbounded → full closure
	}
	params := map[string]any{"root": q.Select.Claim.String()}

	// Bind the path only for routes (path shape) or per-hop reverse confinement;
	// otherwise plain reachability (no path=) is O(nodes), not O(paths).
	usePath := needPaths || (confined(scope) && hasReverseStep(steps))

	var pat, where strings.Builder
	pat.WriteString("(n0 {id: $root})") // the node/rel chain; the MATCH form is chosen below
	addWhere := func(cond string) {
		if where.Len() > 0 {
			where.WriteString("\n  AND ")
		}
		where.WriteString(cond)
	}
	for i, step := range steps {
		rv, nv := "r"+strconv.Itoa(i), "n"+strconv.Itoa(i+1)
		bound := ""
		if step.Depth > 0 {
			bound = strconv.Itoa(step.Depth)
		}
		switch step.Dir {
		case ranke.DirUses:
			pat.WriteString("<-[" + rv + "*0.." + bound + "]-(" + nv + ")")
		case ranke.DirConnections:
			pat.WriteString("-[" + rv + "*0.." + bound + "]-(" + nv + ")")
		default: // provenance (outgoing)
			pat.WriteString("-[" + rv + "*0.." + bound + "]->(" + nv + ")")
		}
		// Edge-type filter over this segment's relationships (empty = all edges).
		if pos, neg := splitPatterns(step.Edges); len(pos) > 0 || len(neg) > 0 {
			if len(pos) > 0 {
				k := "ep" + strconv.Itoa(i)
				params[k] = pos
				addWhere("all(x IN " + rv + " WHERE any(re IN $" + k + " WHERE type(x) =~ re))")
			}
			if len(neg) > 0 {
				k := "en" + strconv.Itoa(i)
				params[k] = neg
				addWhere("all(x IN " + rv + " WHERE none(re IN $" + k + " WHERE type(x) =~ re))")
			}
		}
		// Node-type filter on this segment's endpoint (empty = any type).
		if pos, neg := splitPatterns(step.Nodes); len(pos) > 0 || len(neg) > 0 {
			if len(pos) > 0 {
				k := "np" + strconv.Itoa(i)
				params[k] = pos
				addWhere("any(l IN labels(" + nv + ") WHERE any(re IN $" + k + " WHERE l =~ re))")
			}
			if len(neg) > 0 {
				k := "nn" + strconv.Itoa(i)
				params[k] = neg
				addWhere("none(l IN labels(" + nv + ") WHERE any(re IN $" + k + " WHERE l =~ re))")
			}
		}
	}
	final := "n" + strconv.Itoa(len(steps))
	addWhere("size(labels(" + final + ")) > 0")

	// Where tree, lowered onto the endpoint node.
	ctr := 0
	if wc := whereClause(q.Where, final, params, &ctr); wc != "true" {
		addWhere(wc)
	}

	// Branch confinement: with a path binding confine every hop (bounds reverse
	// steps); otherwise a forward walk stays in-closure, so confining the end is enough.
	if confined(scope) {
		params["bkey"] = ranke.BranchTagKey(scope.Branch)
		params["height"] = scope.Height
		if usePath {
			addWhere("all(x IN nodes(path) WHERE x[$bkey] <= $height)")
		} else {
			addWhere(final + "[$bkey] <= $height")
		}
	}
	// Choose the MATCH form. A single-segment path shape uses allShortestPaths so
	// neo4j visits each node once (O(nodes)) instead of enumerating every route;
	// a reachability read binds no path (O(nodes)); anything else binds the path.
	var match string
	switch {
	case needPaths && len(steps) == 1:
		match = "MATCH path = allShortestPaths(" + pat.String() + ")"
	case usePath:
		match = "MATCH path=" + pat.String()
	default:
		match = "MATCH " + pat.String()
	}
	body := match + "\nWHERE " + where.String()
	if needPaths {
		// Canonical route per endpoint (shortest, then (created_at,id) total order),
		// kept via head(collect(...)); each route node carries its full data so the
		// route claims rebuild from this query; then order+limit endpoints.
		return body +
			"\nWITH " + final + ", path ORDER BY length(path), [x IN nodes(path) | x.created_at + x.id]" +
			"\nWITH " + final + ", head(collect([x IN nodes(path) | " + nodeData("x") + "])) AS route" +
			orderLimitClause(q.Order, q.Limit.Results, final) +
			"\nRETURN route", params
	}
	return body + "\nWITH DISTINCT " + final +
		"\nRETURN " + returnCols(q.Output.Detail, final) +
		orderLimitClause(q.Order, q.Limit.Results, final), params
}

// hasReverseStep reports whether any step walks edges backward.
func hasReverseStep(steps []ranke.PathStep) bool {
	for _, s := range steps {
		if s.Dir == ranke.DirUses || s.Dir == ranke.DirConnections {
			return true
		}
	}
	return false
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

// shapeContent inlines up to Output.Content bytes of a claim's content (cutoff
// overflow; other policies are a later concern). Content neo4j does not hold is
// read through the claim, which reaches the byte layer.
func (u *neo4jUniverse) shapeContent(ctx context.Context, c ranke.Claim, cnt *ranke.Content) []byte {
	rdr, err := c.GetContent(ctx, u)
	if err != nil {
		return nil
	}
	data, err := io.ReadAll(io.LimitReader(rdr, cnt.Max+1))
	if err != nil {
		return nil
	}
	if int64(len(data)) <= cnt.Max {
		return data
	}
	return data[:cnt.Max]
}
