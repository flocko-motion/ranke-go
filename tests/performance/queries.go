// package: tests/performance / integration
// type:    test
// job:     the fixed RQL query set the matrix runs on every backend — timed a few times each for per-query latency, and hashed (order-sensitive) so every backend's result set can be checked against the mem reference
// limits:  queries only; wiring into the run + the mem-reference comparison live in the harness (-> harness.go). Hashes identity (ids) in stream order, not content bytes.
package performance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/generator"
)

// printQueryList prints the fixed query set once, before the matrix — name, RQL,
// and reference result count — since it is identical for every backend. Each
// backend's report then shows the query timings as "4.N" rows in its table.
func printQueryList(w io.Writer, stats []queryStat, withResults bool) {
	rule := strings.Repeat("═", 88)
	fmt.Fprintf(w, "\n%s\n  chapter 4 — queries (same for every backend; timings appear as 4.N table rows)\n%s\n", rule, rule)
	for _, s := range stats {
		if withResults {
			fmt.Fprintf(w, "  %-4s %-24s %-46s %d results\n", s.id, s.name, s.rql, s.results)
		} else {
			fmt.Fprintf(w, "  %-4s %-24s %s\n", s.id, s.name, s.rql)
		}
	}
	fmt.Fprintf(w, "%s\n", rule)
}

// queryList returns the query set's id/name/rql without running anything — for
// the list when --correctness is off (no result counts). Filtered by step like
// runQuerySet. Root is irrelevant to the RQL rendering, so it is left nil.
func queryList(m *generator.Manifest, step string) []queryStat {
	var out []queryStat
	for i, nq := range perfQueries(m, nil) {
		id := fmt.Sprintf("4.%d", i+1)
		if step != "" && step != "4" && step != id {
			continue
		}
		out = append(out, queryStat{id: id, name: nq.name, rql: describeQuery(nq.q)})
	}
	return out
}

// namedQuery pairs a stable name with an RQL query. The name keys both the
// per-query latency report and the cross-backend determinism check.
type namedQuery struct {
	name string
	q    ranke.Query
}

// branchScoped reports whether the query confines to a named branch (resolved
// via tags), as opposed to the unconfined $universe scope. A backend that holds
// no tags runs only the unconfined queries.
func (nq namedQuery) branchScoped() bool { return nq.q.Select.Branch != ranke.BranchUniverse }

// queryBranch is the branch the branch-scoped perf queries confine to (always
// present — the generator's first branch). Reads confined to a branch are the
// typical case; $universe is the privileged, unconfined exception.
const queryBranch = "main"

// perfQueries is the fixed query set: five confined to the "main" branch (rooted
// at its head — the everyday case) and one unconfined $universe node-type search
// (exploring an unknown closure — the privileged exception). Deterministic
// generation makes the default (created_at, id) order — and thus each result
// hash — identical across backends that answer the same meaning.
func perfQueries(m *generator.Manifest, root ranke.Id) []namedQuery {
	sel := func() ranke.Select { return ranke.Select{Branch: queryBranch, Claim: root} }
	return []namedQuery{
		{"branch/closure", ranke.Query{Select: sel()}},
		{"branch/sources", ranke.Query{
			Select: sel(),
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
		}},
		{"branch/height-ge-2", ranke.Query{
			Select: sel(),
			Where:  &ranke.Where{Field: "height", Test: &ranke.Comparison{Ge: 2}},
		}},
		{"branch/derivation-d3", ranke.Query{
			Select: ranke.Select{Branch: queryBranch, Claim: root, Path: []ranke.PathStep{{Edges: []string{"derivation/*"}, Depth: 3}}},
		}},
		{"branch/order-height-20", ranke.Query{
			Select: sel(),
			Order:  &ranke.Order{Field: "height", Desc: true},
			Limit:  ranke.Limit{Results: 20},
		}},
		// Reverse walk: reach the branch's sources (forward), then DirUses to the
		// derivations that use them — unreachable going forward, so it exercises the
		// backend's reverse path (native on neo4j; closure-inversion in the reference).
		{"branch/uses-of-sources", ranke.Query{
			Select: ranke.Select{Branch: queryBranch, Claim: root, Path: []ranke.PathStep{
				{Nodes: []string{"source/*"}},
				{Dir: ranke.DirUses, Edges: []string{"derivation/*"}, Nodes: []string{"derivation/*"}},
			}},
		}},
		// The one $universe query: an unconfined node-type search — exploring an
		// unknown closure for a type. The atypical, most-expensive shape. Rooted at
		// the universe head (no branch/tags), so it runs on every backend.
		{"universe/type-search", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchUniverse, Claim: m.Head},
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "entity/*"}},
		}},
	}
}

// queryRoot resolves the head of the branch the perf queries confine to. It is
// deterministic (same generation → same id), so the reference and every backend
// resolve the same root and run byte-identical queries.
func queryRoot(ctx context.Context, u ranke.Universe, m *generator.Manifest) (ranke.Id, error) {
	arc, err := ranke.NewArchive(ctx, u, m.Head)
	if err != nil {
		return nil, err
	}
	br, err := arc.GetBranch(ctx, queryBranch)
	if err != nil {
		return nil, err
	}
	return br.Head(), nil
}

// runQuery executes q against u, draining the stream, and returns the
// order-sensitive hash of the reached ids, the result count, and how long the
// query-plus-drain took. The hash commits to identity in emitted order, so a
// backend whose engine reorders or drops results diverges from the reference.
// queryScope resolves the scope an Archive would apply: a branch query is
// confined to its head's closure (root == branch head), $universe is unconfined.
// Height completes it — a native backend confines by _b_<branch> <= Height, so
// it must be the branch head's height (the reference confines by Head, ignores it).
func queryScope(ctx context.Context, u ranke.Universe, q ranke.Query) (ranke.Scope, error) {
	scope := ranke.Scope{Branch: q.Select.Branch}
	if q.Select.Branch != ranke.BranchUniverse {
		scope.Head = q.Select.Claim
		hc, err := u.GetClaims(ctx, []ranke.Id{scope.Head})
		if err != nil {
			return ranke.Scope{}, err
		}
		scope.Height = hc[0].Node().Height()
	}
	return scope, nil
}

func runQuery(ctx context.Context, u ranke.Universe, q ranke.Query) (hash string, results int, dur time.Duration, err error) {
	start := time.Now()
	scope, err := queryScope(ctx, u, q)
	if err != nil {
		return "", 0, 0, err
	}
	rs, err := u.Query(ctx, q, scope)
	if err != nil {
		return "", 0, 0, err
	}
	h := sha256.New()
	for rs.Next() {
		h.Write([]byte(rs.Result().Claim.ID().String()))
		h.Write([]byte{'\n'})
		results++
	}
	dur = time.Since(start)
	if e := rs.Err(); e != nil {
		_ = rs.Close()
		return "", 0, 0, e
	}
	if e := rs.Close(); e != nil {
		return "", 0, 0, e
	}
	return hex.EncodeToString(h.Sum(nil)), results, dur, nil
}

// queryStat is one named query's outcome on a backend: latency over the reps
// and the result-set hash (compared to the mem reference for determinism).
type queryStat struct {
	id            string // stable phase id ("4.1", "4.2", …) — the table-row label
	name          string
	rql           string // one-line RQL rendering (shown under --rql)
	min, avg, max time.Duration
	results       int
	hash          string
}

// describeQuery renders a query's RQL on one line — compact, shown only under
// --rql (it is verbose). Just the meaningful parts: scope, path/closure, filter,
// order, limit.
func describeQuery(q ranke.Query) string {
	parts := []string{"branch=" + q.Select.Branch}
	if len(q.Select.Path) == 0 {
		parts = append(parts, "closure")
	} else {
		for _, s := range q.Select.Path {
			seg := "path("
			if s.Dir != "" {
				seg += "dir=" + string(s.Dir) + " "
			}
			if len(s.Edges) > 0 {
				seg += "edges=" + strings.Join(s.Edges, ",") + " "
			}
			if len(s.Nodes) > 0 {
				seg += "nodes=" + strings.Join(s.Nodes, ",") + " "
			}
			if s.Depth > 0 {
				seg += fmt.Sprintf("depth=%d", s.Depth)
			}
			parts = append(parts, strings.TrimSpace(seg)+")")
		}
	}
	if q.Where != nil {
		parts = append(parts, "where "+describeWhere(q.Where))
	}
	if q.Order != nil {
		dir := "asc"
		if q.Order.Desc {
			dir = "desc"
		}
		parts = append(parts, "order("+q.Order.Field+" "+dir+")")
	}
	if q.Limit.Results > 0 {
		parts = append(parts, fmt.Sprintf("limit=%d", q.Limit.Results))
	}
	return strings.Join(parts, " ")
}

func describeWhere(w *ranke.Where) string {
	switch {
	case len(w.And) > 0:
		return "(" + joinWheres(w.And, " and ") + ")"
	case len(w.Or) > 0:
		return "(" + joinWheres(w.Or, " or ") + ")"
	case w.Not != nil:
		return "not " + describeWhere(w.Not)
	case w.Test != nil:
		return w.Field + describeCmp(w.Test)
	}
	return "?"
}

func joinWheres(ws []ranke.Where, sep string) string {
	p := make([]string, len(ws))
	for i := range ws {
		p[i] = describeWhere(&ws[i])
	}
	return strings.Join(p, sep)
}

func describeCmp(c *ranke.Comparison) string {
	switch {
	case c.Glob != "":
		return " glob " + c.Glob
	case c.Eq != nil:
		return fmt.Sprintf(" == %v", c.Eq)
	case c.Ne != nil:
		return fmt.Sprintf(" != %v", c.Ne)
	case c.Lt != nil:
		return fmt.Sprintf(" < %v", c.Lt)
	case c.Le != nil:
		return fmt.Sprintf(" <= %v", c.Le)
	case c.Gt != nil:
		return fmt.Sprintf(" > %v", c.Gt)
	case c.Ge != nil:
		return fmt.Sprintf(" >= %v", c.Ge)
	case len(c.In) > 0:
		return fmt.Sprintf(" in %v", c.In)
	}
	return "?"
}

// runQuerySet runs the query set reps times against u, returning the per-query
// latency stats (with the result-set hash) in query order. reps ≤ 0 defaults to
// 10. Each query is timed under its own phase ("4.1", "4.2", …) so it appears as
// a row in the metered table. step, when non-empty, runs only the matching query
// ("4.5"; "4" runs all). When report is set, each query's full execution report
// is printed to w.
func runQuerySet(ctx context.Context, u ranke.Universe, m *generator.Manifest, reps int, taggable bool, step string, report bool, w io.Writer) ([]queryStat, error) {
	if reps <= 0 {
		reps = 10
	}
	// The branch-scoped queries root at a branch head (resolved via tags); a
	// backend that holds no tags runs only the $universe query.
	var root ranke.Id
	if taggable {
		var err error
		if root, err = queryRoot(ctx, u, m); err != nil {
			return nil, err
		}
	}
	met, isMet := u.(*metered)
	var stats []queryStat
	for i, nq := range perfQueries(m, root) {
		phaseID := fmt.Sprintf("4.%d", i+1)
		if step != "" && step != "4" && step != phaseID {
			continue // --step selects one query (or "4" for the whole chapter)
		}
		if !taggable && nq.branchScoped() {
			continue // branch-scoped query needs tags this backend can't hold
		}
		if isMet {
			met.setPhase(phaseID)
		}
		var mn, mx, sum time.Duration
		var hash string
		var results int
		for j := 0; j < reps; j++ {
			h, n, d, err := runQuery(ctx, u, nq.q)
			if err != nil {
				return nil, err
			}
			hash, results = h, n
			if j == 0 || d < mn {
				mn = d
			}
			if d > mx {
				mx = d
			}
			sum += d
		}
		stats = append(stats, queryStat{
			id: phaseID, name: nq.name, rql: describeQuery(nq.q),
			min: mn, avg: sum / time.Duration(reps), max: mx,
			results: results, hash: hash,
		})
		if report {
			if isMet {
				met.setPhase("report") // keep the report run's ops out of the "4.N" timing
			}
			if err := printQueryReport(ctx, w, u, phaseID, nq); err != nil {
				return nil, err
			}
		}
	}
	return stats, nil
}

// printQueryReport runs nq once with full execution reporting on and prints the
// per-station event log to w — the --report view, shown in the queries section.
func printQueryReport(ctx context.Context, w io.Writer, u ranke.Universe, label string, nq namedQuery) error {
	scope, err := queryScope(ctx, u, nq.q)
	if err != nil {
		return err
	}
	q := nq.q
	q.Execution.Report = ranke.ReportDebug
	rs, err := u.Query(ctx, q, scope)
	if err != nil {
		return err
	}
	for rs.Next() {
	}
	if e := rs.Err(); e != nil {
		_ = rs.Close()
		return e
	}
	rep := rs.Report()
	_ = rs.Close()

	fmt.Fprintf(w, "\n  %s %s — %s\n", label, nq.name, describeQuery(nq.q))
	if rep == nil {
		fmt.Fprintf(w, "    (backend produced no report)\n")
		return nil
	}
	fmt.Fprintf(w, "    elapsed %s  %d results%s\n", rep.Elapsed.Round(time.Microsecond), rep.Results, truncMark(rep.Truncated))
	for _, e := range rep.Events {
		dur := ""
		if e.Duration > 0 {
			dur = " (" + e.Duration.Round(time.Microsecond).String() + ")"
		}
		fmt.Fprintf(w, "    %10s  %-9s %-14s%s", e.At.Round(time.Microsecond), e.Engine, e.Op, dur)
		if e.Detail != "" {
			fmt.Fprintf(w, "  %s", e.Detail)
		}
		if len(e.Attrs) > 0 {
			fmt.Fprintf(w, "  %v", e.Attrs)
		}
		fmt.Fprintln(w)
	}
	return nil
}

func truncMark(t bool) string {
	if t {
		return " (truncated)"
	}
	return ""
}
