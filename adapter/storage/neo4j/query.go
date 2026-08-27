// package: neo4j / query
// type:    adapter
// job:     native RQL execution — lower the whole query to one Cypher statement, run it,
// reconstruct + stream (neo4j has an engine, never uses DefaultQuery)
// limits:  every read starts from Select.Claim, or from anywhere in the closure when it names
// none — a scan without a path, a walk with one; branch confinement is the
// _b_<branch> tag (<= Height, point-in-time), not a walk; a serialised read is refused
package neo4j

import (
	"context"
	"errors"
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
	// A serialised result is the stored record, which this layer keeps none of. The
	// capability is what decides, not the request: a stack asks its layers natively
	// and encodes above them, so an ask that reaches here is a caller's bug.
	if enc := q.Output.Encoding; enc != "" && enc != ranke.ResultNative && !u.Capabilities().RawClaims {
		return nil, errByteForm
	}
	start := time.Now()
	needPaths := q.Output.Shape == ranke.ShapePath
	rep := newReport(q.Execution.Report, start)

	cypher, params := lowerCypher(q, scope, needPaths)
	rep.log("cypher", "lower", ranke.ReportDebug, 0, cypher)

	execStart := time.Now()
	res, err := u.query(ctx, cypher, params, txTimeout(q.Limit.Time)...)
	if err != nil {
		// The server terminating the transaction is limit.time being spent, which
		// bounds the answer rather than failing it (`R-QLIMIT`). It leaves no partial
		// rows, so the bounded answer here is the empty one, reported as cut short.
		if boundedByTime(err, q.Limit.Time, ctx.Err()) {
			rep.log("cypher", "limit", ranke.ReportInfo, time.Since(execStart), q.Limit.Time.String())
			// No rows: the report is the whole sequence, or none of it (`R-QREPORT`).
			return &cypherStream{results: ranke.AppendReport(nil, rep.finalize(0, true))}, nil
		}
		return nil, fmt.Errorf("%w: execute: %w", errQuery, err)
	}
	rep.log("cypher", "execute", ranke.ReportInfo, time.Since(execStart), "")

	recStart := time.Now()
	// Native results only: this layer answers which claims, never their
	// serialisation (-> stack.Query, ranke.EncodeResults).
	ids := idsOnly(q.Output)
	var results []ranke.QueryResult
	switch {
	case needPaths:
		ps, rerr := u.reachedPaths(res.Records)
		if rerr != nil {
			return nil, rerr
		}
		for i := range ps {
			ps[i].Kind = ranke.KindPathNative
			if ids {
				ps[i].ClaimNative, ps[i].PathNative = nil, nil // the ids carry the identities
				ps[i].Kind = ranke.KindPathId
			}
		}
		results = ps
	case ids:
		// ids only — no reconstruction at all.
		for _, r := range res.Records {
			id, perr := ranke.ParseId(asString(valOf(r, "id")))
			if perr != nil {
				return nil, perr
			}
			results = append(results, ranke.QueryResult{Kind: ranke.KindClaimId, ClaimId: id})
		}
	default: // graph or claims: build from this query's own node records.
		claims, rerr := u.claimsFromRecords(res.Records)
		if rerr != nil {
			return nil, rerr
		}
		for _, c := range claims {
			results = append(results, ranke.QueryResult{
				Kind: ranke.KindClaimNative, ClaimId: c.ID(), ClaimNative: c,
			})
		}
	}
	// The statement asked for one row past the cap, so an extra row here is the
	// evidence that more existed — the same thing the reference learns by holding
	// the whole set and cutting it (`R-QLIMIT`).
	truncated := false
	if n := q.Limit.Results; n > 0 && len(results) > n {
		results, truncated = results[:n], true
		rep.log("neo4j", "limit", ranke.ReportInfo, 0, strconv.Itoa(n)+" results, truncated")
	}
	rep.log("neo4j", "reconstruct", ranke.ReportInfo, time.Since(recStart), strconv.Itoa(len(results))+" results")
	// The report ends the sequence (`R-QSTREAM`), finalised over what it counts.
	return &cypherStream{results: ranke.AppendReport(results, rep.finalize(len(results), truncated))}, nil
}

// reachedPaths assembles one result per record: a claim per route element, last is
// the endpoint. Both the id and the native view of the route are filled; Kind decides.
func (u *neo4jUniverse) reachedPaths(records []*neo4jdriver.Record) ([]ranke.QueryResult, error) {
	out := make([]ranke.QueryResult, 0, len(records))
	for _, r := range records {
		hops, ok := valOf(r, "route").([]any)
		if !ok {
			return nil, fmt.Errorf("%w: route is not a list", errQuery)
		}
		route := make([]ranke.Claim, 0, len(hops))
		ids := make([]ranke.Id, 0, len(hops))
		for _, h := range hops {
			m, _ := h.(map[string]any)
			c, err := claimFromData(m)
			if err != nil {
				return nil, err
			}
			route = append(route, c)
			ids = append(ids, c.ID())
		}
		if len(route) == 0 {
			continue
		}
		end := route[len(route)-1]
		out = append(out, ranke.QueryResult{
			ClaimId: end.ID(), ClaimNative: end, PathId: ids, PathNative: route,
		})
	}
	return out, nil
}

// idsOnly reports whether a read returns identities alone, which only DetailID
// asks for. A serialised read is refused at the door (-> Query), not answered
// with ids.
func idsOnly(out ranke.Output) bool {
	return out.Detail == ranke.DetailID
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

// frontierRoot binds the claim a read starts from — the anchor `R-QANCHOR` names, else
// the closure anchor — returning its parameter name, "" when neither bounds the read.
func frontierRoot(q ranke.Query, scope ranke.Scope, params map[string]any) string {
	if q.Select.Claim != nil {
		params["root"] = q.Select.Claim.String()
		return "$root"
	}
	if anchor := closureAnchor(q, scope); anchor != nil {
		params["head"] = anchor.String()
		return "$head"
	}
	return ""
}

// startClause binds n0, where a traversal's first segment starts: the claim
// Select.Claim names, else any claim the closure holds.
func startClause(q ranke.Query, scope ranke.Scope, params map[string]any) string {
	root := frontierRoot(q, scope, params)
	switch {
	case root == "":
		return "MATCH (n0)"
	case q.Select.Claim != nil:
		return "MATCH (n0 {id: " + root + "})" // the anchor itself is the frontier
	}
	return "MATCH (h {id: " + root + "})-[*0..]->(n0)"
}

// lowerCypher routes a query to its Cypher: a Path-less read is the frontier's
// closure (a scan), anything else follows the Path's steps.
func lowerCypher(q ranke.Query, scope ranke.Scope, needPaths bool) (string, map[string]any) {
	if len(q.Select.Path) == 0 {
		return scanCypher(q, scope) // no traversal: the frontier's outward closure
	}
	return traversalCypher(q, scope, needPaths)
}

// scanCypher lowers a Path-less read: the frontier's outward closure (`R-QSTEPS`),
// frontierRoot fixing the start so a scan and a walk begin alike (ranke.frontier).
func scanCypher(q ranke.Query, scope ranke.Scope) (string, map[string]any) {
	params := map[string]any{}
	match := "MATCH (n)"
	if root := frontierRoot(q, scope, params); root != "" {
		match = "MATCH (h {id: " + root + "})-[*0..]->(n)\nWITH DISTINCT n"
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
		"\nRETURN " + returnCols(q.Output, "n") +
		orderLimitClause(q.Order, q.Limit.Results, "n"), params
}

// returnCols is the RETURN projection for an endpoint node: the id for an
// ids-only read, else its full data (node/labels/edges) so the claim rebuilds
// from this query.
func returnCols(out ranke.Output, v string) string {
	if idsOnly(out) {
		return v + ".id AS id"
	}
	return "properties(" + v + ") AS node, labels(" + v + ") AS labels, " +
		"[(" + v + ")-[r]->(t) | {props: properties(r), rtype: type(r), ref: t.id}] AS edges"
}

// claimsFromRecords rebuilds claims from a query's node records (props, labels, edges).
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

// traversalCypher lowers the Select's Path — frontierCypher for reachability,
// pathCypher for a path shape. A 0-length segment includes its start (DefaultQuery).
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

// frontierCypher lowers a reachability read as a per-step frontier pipeline, one
// MATCH … WITH DISTINCT per step, so relationship-uniqueness resets at each
// boundary: a step may re-walk an edge an earlier step used, as the reference does.
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
		"\nRETURN " + returnCols(q.Output, final) +
		orderLimitClause(q.Order, q.Limit.Results, final), params
}

// pathCypher lowers a path-shape read as a per-step pipeline, the route carried forward
// as a list, so relationship-uniqueness resets at each boundary (`R-QCFRONTIER`). Each
// step keeps the one route per endpoint `R-QSHAPE` names, which is exact per step: a
// segment depends on the node it starts from, not on how that node was reached.
func pathCypher(q ranke.Query, scope ranke.Scope, steps []ranke.PathStep, params map[string]any) (string, map[string]any) {
	conf := tagBounded(scope)
	if conf {
		params["bkey"] = ranke.BranchTagKey(scope.Branch)
		params["height"] = scope.Height
	}
	final := "n" + strconv.Itoa(len(steps))
	var b strings.Builder
	b.WriteString(startClause(q, scope, params) + "\nWITH DISTINCT n0, [n0] AS route")
	for i, step := range steps {
		rv, nv, pv := "r"+strconv.Itoa(i), "n"+strconv.Itoa(i+1), "p"+strconv.Itoa(i)
		seg := pv + " = " + segmentPattern("n"+strconv.Itoa(i), rv, nv, step)
		conds := segmentFilters(step, rv, nv, i, params)
		if conf { // confine every node on the segment (bounds reverse steps to members)
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
		// The segment repeats the node the route already ends at, so it joins from 1.
		b.WriteString("\nWITH " + nv + ", route + nodes(" + pv + ")[1..] AS route" +
			"\n  ORDER BY size(route), [x IN route | x.created_at + x.id]" +
			"\nWITH " + nv + ", head(collect(route)) AS route")
	}
	return b.String() +
		orderLimitClause(q.Order, q.Limit.Results, final) +
		"\nRETURN [x IN route | " + nodeData("x") + "] AS route", params
}

// segmentPattern renders one PathStep as a variable-length Cypher segment in its
// direction. prev is the anchor node name, "" when a chained segment already wrote it.
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

// segmentFilters returns one step's edge-type (over rv) and node-type (over nv)
// conditions, binding their regexes under per-step keys in params. Empty means "any".
func segmentFilters(step ranke.PathStep, rv, nv string, i int, params map[string]any) []string {
	var conds []string
	if pos, neg := splitPatterns(step.Edges); pos != "" || neg != "" {
		if pos != "" {
			k := "ep" + strconv.Itoa(i)
			params[k] = pos
			conds = append(conds, "all(x IN "+rv+" WHERE type(x) =~ $"+k+")")
		}
		if neg != "" {
			k := "en" + strconv.Itoa(i)
			params[k] = neg
			conds = append(conds, "all(x IN "+rv+" WHERE NOT type(x) =~ $"+k+")")
		}
	}
	if pos, neg := splitPatterns(step.Nodes); pos != "" || neg != "" {
		if pos != "" {
			k := "np" + strconv.Itoa(i)
			params[k] = pos
			conds = append(conds, "any(l IN labels("+nv+") WHERE l =~ $"+k+")")
		}
		if neg != "" {
			k := "nn" + strconv.Itoa(i)
			params[k] = neg
			conds = append(conds, "none(l IN labels("+nv+") WHERE l =~ $"+k+")")
		}
	}
	return conds
}

// splitPatterns turns a type-pattern list into one positive and one negative regex
// (a leading "-" excludes); globs become regex for Cypher's =~ full match. Flat
// alternation, since neo4j mis-rewrites a nested list predicate that it inlines
// into a shortest-path expansion.
func splitPatterns(patterns []string) (pos, neg string) {
	var p, n []string
	for _, pat := range patterns {
		if after, found := strings.CutPrefix(pat, "-"); found {
			n = append(n, globToRegex(after))
		} else {
			p = append(p, globToRegex(pat))
		}
	}
	return alternation(p), alternation(n)
}

// alternation joins regexes into one, grouping each branch to contain its alternatives.
func alternation(res []string) string {
	if len(res) == 0 {
		return ""
	}
	if len(res) == 1 {
		return res[0]
	}
	return "(?:" + strings.Join(res, ")|(?:") + ")"
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

// intField are the fields neo4j stores as native integers, so comparisons and
// ORDER BY use them without coercion. Other fields are strings; type is a label.
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

// cmpClause lowers one field comparison to Cypher as evalComparison does: type is
// a label, int fields compare natively, others coerce to float for numeric operands
// and to string otherwise. A missing field yields null, so WHERE rejects the claim.
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

// typeClause lowers a comparison on type, which neo4j stores as a node label.
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

// orderLimitClause renders ORDER BY + LIMIT over node as the reference does: the
// named fields then (created_at, id) for a total order, missing values last.
func orderLimitClause(keys []ranke.OrderKey, limit int, node string) string {
	var b strings.Builder
	b.WriteString("\nORDER BY ")
	for _, k := range keys {
		b.WriteString(orderTerm(k, node) + ", ")
	}
	// Natural order (created_at, id) — the tiebreak, always last.
	b.WriteString(node + ".created_at, " + node + ".id")
	if limit > 0 {
		// One past the cap: the extra row is how the caller learns more existed, and
		// it is trimmed before the results are returned.
		b.WriteString("\nLIMIT " + strconv.Itoa(limit+1))
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
	if k.Compare == ranke.CompareTemporal && k.Field == "dated" {
		// datedMidProperty is projected at write time (claimParam) — the storage
		// layer's own tactic, since Cypher cannot parse EDTF (R-QTEMPORAL).
		mid := node + "." + datedMidProperty
		return mid + " IS NULL, " + mid + dir
	}
	acc := node + "." + k.Field
	key := acc
	if k.Compare == ranke.CompareNumeric || intField[k.Field] {
		key = "toFloat(" + acc + ")"
	}
	return acc + " IS NULL, " + key + dir
}

// isNumeric reports whether v is a Go numeric type, the operand kind that makes a
// comparison numeric (compareOrdered's asFloat path).
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

// cypherStream is neo4j's ResultStream over an already-resolved slice: Cypher ran
// the filter/order/limit, so this hands rows out in order, the report last.
type cypherStream struct {
	results []ranke.QueryResult
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
func (s *cypherStream) Report() *ranke.QueryReport { return ranke.ReportOf(s.results) }
func (s *cypherStream) Err() error                 { return nil }
func (s *cypherStream) Close() error               { return nil }

// boundedByTime reports whether err is limit.time being spent rather than the read
// failing: a budget was set, the caller's context is still live, and the server ended
// the transaction on the budget the statement carried.
func boundedByTime(err error, budget time.Duration, ctxErr error) bool {
	return budget > 0 && ctxErr == nil && isTxTimeout(err)
}

// isTxTimeout reports whether err is the server ending the transaction on its
// timeout, which is how limit.time arrives back here. Only the TransactionTimedOut
// codes count: the driver normalises every transient termination — an operator's
// killTransaction, a leader switch, a lock manager stopping — into a bare
// Transaction.Terminated, and reading those as a bounded answer would turn each of
// them into a silent empty success.
func isTxTimeout(err error) bool {
	var nerr *neo4jdriver.Neo4jError
	return errors.As(err, &nerr) && strings.Contains(nerr.Code, "TransactionTimedOut")
}
