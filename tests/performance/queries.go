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
func printQueryList(w io.Writer, stats []queryStat) {
	rule := strings.Repeat("═", 88)
	fmt.Fprintf(w, "\n%s\n  chapter 4 — queries (same for every backend; timings appear as 4.N table rows)\n%s\n", rule, rule)
	for i, s := range stats {
		fmt.Fprintf(w, "  4.%-2d %-24s %-46s %d results\n", i+1, s.name, s.rql, s.results)
	}
	fmt.Fprintf(w, "%s\n", rule)
}

// namedQuery pairs a stable name with an RQL query. The name keys both the
// per-query latency report and the cross-backend determinism check.
type namedQuery struct {
	name string
	q    ranke.Query
}

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
		// The one $universe query: an unconfined node-type search — exploring an
		// unknown closure for a type. The atypical, most-expensive shape.
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
func runQuery(ctx context.Context, u ranke.Universe, q ranke.Query) (hash string, results int, dur time.Duration, err error) {
	start := time.Now()
	rs, err := u.Query(ctx, q, nil)
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

// runQuerySet runs every query in the set reps times against u, returning the
// per-query latency stats (with the result-set hash) in query order. reps ≤ 0
// defaults to 10. Each query is timed under its own phase ("4.1", "4.2", …) so
// it appears as a row in the metered table (the pre-matrix listing maps the
// numbers to names + RQL); a mem reference (non-metered) skips the phase.
func runQuerySet(ctx context.Context, u ranke.Universe, m *generator.Manifest, reps int) ([]queryStat, error) {
	if reps <= 0 {
		reps = 10
	}
	root, err := queryRoot(ctx, u, m)
	if err != nil {
		return nil, err
	}
	met, isMet := u.(*metered)
	var stats []queryStat
	for i, nq := range perfQueries(m, root) {
		if isMet {
			met.setPhase(fmt.Sprintf("4.%d", i+1))
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
			name: nq.name, rql: describeQuery(nq.q),
			min: mn, avg: sum / time.Duration(reps), max: mx,
			results: results, hash: hash,
		})
	}
	return stats, nil
}
