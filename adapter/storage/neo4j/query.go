// package: neo4j / query
// type:    adapter
// job:     native RQL execution — lower Select/Path to one Cypher query (reached ids, and routes for DetailPath), then reuse the reference pipeline (ranke.DefaultQueryFrom)
// limits:  every query lowers; there is no fallback shape except a nil root. Branch confinement is the _b_<branch> tag (below), not a structural walk
package neo4j

// The scope model, so it stops getting relearned:
//
//   - Universe (this layer) is a content-addressed SET — id→claim. It knows
//     nothing about branches. The Archive, whose head is the branch table, resolves
//     q.Select.Branch into a Scope{Head, Branch, Height, Revision} and defaults
//     q.Select.Claim to the head, then calls this (ranke paper 1 §Universe/§Archive).
//   - _b_<branch> is a per-claim MEMBERSHIP TAG the tagger stamps on every claim in
//     a branch's closure, valued with the branch-head height it joined at
//     (ranke/universe_taxonomy.go). It answers "is this claim in branch B as of
//     height H?" in O(1) as `_b_B <= H` — the scope test the reference can only
//     answer by walking the closure. That height bound makes point-in-time reads
//     (an Archive opened at an older head) answerable from one cache.
//   - So confinement is a filter, never a walk: a real branch → `_b_<Branch> <=
//     Height`; $universe/$archive → nothing, because a neo4j instance already IS
//     the snapshot of that scope (confined() is false for both).
//   - Query shape is orthogonal to confinement: whole-branch (Claim==Head, no path)
//     → tag scan (membershipCypher); otherwise walk from q.Select.Claim, applying
//     the same confinement as a WHERE (see traversalCypher).

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/flocko-motion/ranke-go"
	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Query lowers Select/Path to one Cypher query for the reached ids (and, for
// DetailPath, each one's route), reconstructs via GetClaims, and finishes through
// ranke.DefaultQueryFrom — so results match the reference walk in two batched
// queries instead of N. Every query lowers; there is no fallback shape.
func (u *neo4jUniverse) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	if q.Select.Claim == nil {
		return ranke.DefaultQuery(ctx, u, q, scope) // no root → the reference returns errQueryNoRoot
	}
	needPaths := q.Output.Detail == ranke.DetailPath
	return ranke.DefaultQueryFrom(ctx, u, "neo4j", q,
		func(ctx context.Context) ([]ranke.Claim, map[string][]ranke.Claim, error) {
			cypher, params := lowerCypher(q, scope, needPaths)
			ranke.ReportEvent(ctx, "cypher", "lower", ranke.ReportDebug, cypher, nil)
			res, err := u.query(ctx, cypher, params)
			if err != nil {
				return nil, nil, fmt.Errorf("%w: query traverse: %w", errQuery, err)
			}
			if needPaths {
				return u.reachedWithRoutes(ctx, res.Records)
			}
			return u.reachedIDs(ctx, res.Records)
		})
}

// reachedIDs turns id-only records into the reconstructed endpoint claims.
func (u *neo4jUniverse) reachedIDs(ctx context.Context, records []*neo4jdriver.Record) ([]ranke.Claim, map[string][]ranke.Claim, error) {
	ids := make([]ranke.Id, 0, len(records))
	for _, r := range records {
		id, err := ranke.ParseId(asString(valOf(r, "id")))
		if err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, nil, nil
	}
	// Reconstruct via the tested GetClaims path (decode + materialise).
	claims, err := u.GetClaims(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	return claims, nil, nil
}

// reachedWithRoutes turns (route, id) records — one row per matched path, ordered
// shortest-first — into the endpoint claims plus each one's route. It keeps the
// first (shortest) route per endpoint, mirroring the reference walk's BFS-first
// route, and materialises every id on the kept routes in one GetClaims batch.
func (u *neo4jUniverse) reachedWithRoutes(ctx context.Context, records []*neo4jdriver.Record) ([]ranke.Claim, map[string][]ranke.Claim, error) {
	routeIDs := map[string][]ranke.Id{} // endpoint id → its route (first/shortest wins)
	order := make([]string, 0, len(records))
	need := map[string]ranke.Id{}
	for _, r := range records {
		endpoint := asString(valOf(r, "id"))
		if _, seen := routeIDs[endpoint]; seen {
			continue // a longer route to an already-reached endpoint — reference kept the first
		}
		hops, ok := valOf(r, "route").([]any)
		if !ok {
			return nil, nil, fmt.Errorf("%w: route is not a list", errQuery)
		}
		route := make([]ranke.Id, 0, len(hops))
		for _, h := range hops {
			id, err := ranke.ParseId(asString(h))
			if err != nil {
				return nil, nil, err
			}
			route = append(route, id)
			need[id.String()] = id
		}
		routeIDs[endpoint] = route
		order = append(order, endpoint)
	}
	if len(order) == 0 {
		return nil, nil, nil
	}
	ids := make([]ranke.Id, 0, len(need))
	for _, id := range need {
		ids = append(ids, id)
	}
	claims, err := u.GetClaims(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	byID := make(map[string]ranke.Claim, len(claims))
	for _, c := range claims {
		byID[c.ID().String()] = c
	}
	reached := make([]ranke.Claim, 0, len(order))
	routes := make(map[string][]ranke.Claim, len(order))
	for _, endpoint := range order {
		reached = append(reached, byID[endpoint])
		route := make([]ranke.Claim, 0, len(routeIDs[endpoint]))
		for _, id := range routeIDs[endpoint] {
			route = append(route, byID[id.String()])
		}
		routes[endpoint] = route
	}
	return reached, routes, nil
}

// confined reports whether scope prunes to a real branch's membership tag.
// $universe/$archive carry no _b_ tag — a neo4j instance is already the snapshot
// of that scope, so they are unrestricted.
func confined(scope ranke.Scope) bool {
	return scope.Branch != "" && scope.Branch != ranke.BranchUniverse && scope.Branch != ranke.BranchArchive
}

// lowerCypher picks the generator for q. A whole-branch read (rooted at the
// branch head with no path, confined to a real branch) is answered straight from
// the _b_<branch> membership tag — a property scan, no traversal. Anything else
// walks from q.Select.Claim (with each endpoint's route when needPaths).
func lowerCypher(q ranke.Query, scope ranke.Scope, needPaths bool) (string, map[string]any) {
	if !needPaths && len(q.Select.Path) == 0 && confined(scope) && q.Select.Claim.Equal(scope.Head) {
		return membershipCypher(scope)
	}
	return traversalCypher(q, scope, needPaths)
}

// membershipCypher returns every claim in the branch's closure straight from the
// precomputed _b_<branch> tag — the whole point of the tag: an O(members) scan
// instead of a walk. The tag value is the branch-head height a claim joined at,
// so `<= Height` gives membership as of the resolved revision (point-in-time);
// null <= H is false, so non-members drop out. bkey is a bound parameter (dynamic
// property access), never interpolated. size(labels) > 0 skips edge-only nodes.
func membershipCypher(scope ranke.Scope) (string, map[string]any) {
	return "MATCH (n)\nWHERE n[$bkey] <= $height AND size(labels(n)) > 0\n" +
			"RETURN DISTINCT n.id AS id",
		map[string]any{"bkey": ranke.BranchTagKey(scope.Branch), "height": scope.Height}
}

// traversalCypher builds the reachability query for the Select: chain one
// variable-length segment per PathStep (direction, depth, edge/node type
// filters), and return the final endpoint's claim ids. Matches DefaultQuery's
// walk (a 0-length segment includes its start, so the root can pass through).
// When needPaths (DetailPath), it returns each matched route (nodes(path), root
// →endpoint) ordered shortest-first, so the caller keeps the reference's
// BFS-first route per endpoint.
func traversalCypher(q ranke.Query, scope ranke.Scope, needPaths bool) (string, map[string]any) {
	steps := q.Select.Path
	if len(steps) == 0 {
		steps = []ranke.PathStep{{}} // implicit: all edges, provenance, unbounded → full closure
	}
	params := map[string]any{"root": q.Select.Claim.String()}

	var pat, where strings.Builder
	pat.WriteString("MATCH path=(n0 {id: $root})")
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
	// Branch confinement (§Filtered Reads: select.branch "confines the query to
	// that branch"). A branch's membership is the _b_<branch> tag the tagger
	// stamps on every claim in its closure, so confinement is: every node on the
	// path is a member as of the resolved revision. This is the whole point of the
	// tag — an O(1) scope test instead of re-deriving reachability. It bounds
	// reverse/connections steps (which can otherwise leave the branch) and keeps
	// DetailPath routes in-scope. The tag value is the join height, so `<= Height`
	// is point-in-time (null <= H is false → non-members drop). $universe/$archive
	// carry no tag and mean "unrestricted" (confined false).
	if confined(scope) {
		params["bkey"] = ranke.BranchTagKey(scope.Branch)
		params["height"] = scope.Height
		addWhere("all(x IN nodes(path) WHERE x[$bkey] <= $height)")
	}
	body := pat.String() + "\nWHERE " + where.String()
	if needPaths {
		// One row per matched route, ordered by the query total order (length,
		// then (created_at, id) per node). created_at is fixed-width iso8601Nano,
		// so concatenating it with the id gives a per-node key that sorts exactly
		// like the reference's (created_at, id); the caller keeps the first
		// (canonical) route per endpoint — byte-identical to the reference walk.
		return body + "\nRETURN [x IN nodes(path) | x.id] AS route, " + final +
			".id AS id, [x IN nodes(path) | x.created_at + x.id] AS routekey" +
			"\nORDER BY length(path), routekey", params
	}
	return body + "\nRETURN DISTINCT " + final + ".id AS id", params
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
