// package: tests/performance / integration
// type:    test
// job:     time the shared RQL corpus' timed subset on a backend — each query run repeatedly for a
// per-query latency distribution, with an optional per-query execution report
// limits:  timing only; the queries come from tests/rql and whether a backend's ANSWERS are right
// is the conformance matrix's question (-> tests/matrix)
package performance

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/rankegraph/ranke-go/tests/rql"
)

// printQueryList prints the timed query set once, before the matrix — name and
// RQL — since it is identical for every backend. Each backend's report then shows
// its timings as "4.N" rows in its table.
func printQueryList(w io.Writer, stats []queryStat) {
	rule := strings.Repeat("═", 88)
	fmt.Fprintf(w, "\n%s\n  chapter 4 — queries (same for every backend; timings appear as 4.N table rows)\n%s\n", rule, rule)
	for _, s := range stats {
		fmt.Fprintf(w, "  %-4s %-24s %s\n", s.id, s.name, s.rql)
	}
	fmt.Fprintf(w, "%s\n", rule)
}

// queryList returns the timed set's id/name/rql without running anything — the
// list printed before the matrix. Filtered by step like runQuerySet. Root is
// irrelevant to the RQL rendering, so it is left nil.
func queryList(m *generator.Manifest, step string) []queryStat {
	var out []queryStat
	for i, nq := range rql.Timed(m, nil) {
		id := fmt.Sprintf("4.%d", i+1)
		if step != "" && step != "4" && step != id {
			continue
		}
		out = append(out, queryStat{id: id, name: nq.Name, rql: rql.Describe(nq.Q)})
	}
	return out
}

// queryRoot resolves the head of the branch the timed queries confine to. It is
// deterministic (same generation → same id), so every backend resolves the same
// root and runs byte-identical queries.
func queryRoot(ctx context.Context, u ranke.Universe, m *generator.Manifest) (ranke.Id, error) {
	arc, err := ranke.NewArchive(ctx, u, m.Head)
	if err != nil {
		return nil, err
	}
	br, err := arc.GetBranch(ctx, rql.Branch)
	if err != nil {
		return nil, err
	}
	return br.Head(), nil
}

// runQuery executes q against u, draining the stream, and returns the result
// count and how long the query-plus-drain took.
func runQuery(ctx context.Context, u ranke.Universe, q ranke.Query, archiveHead ranke.Id) (results int, dur time.Duration, err error) {
	start := time.Now()
	arc, err := ranke.NewArchive(ctx, u, archiveHead)
	if err != nil {
		return 0, 0, err
	}
	rs, err := arc.Query(ctx, q)
	if err != nil {
		return 0, 0, err
	}
	for rs.Next() {
		results++
	}
	dur = time.Since(start)
	if e := rs.Err(); e != nil {
		_ = rs.Close()
		return 0, 0, e
	}
	if e := rs.Close(); e != nil {
		return 0, 0, e
	}
	return results, dur, nil
}

// queryStat is one named query's latency over the reps on one backend.
type queryStat struct {
	id            string // stable phase id ("4.1", "4.2", …) — the table-row label
	name          string
	rql           string // one-line RQL rendering (shown under --rql)
	min, avg, max time.Duration
	results       int
}

// runQuerySet runs the timed set reps times against u, returning the per-query
// latency stats in query order. reps ≤ 0 defaults to 10. Each query is timed
// under its own phase ("4.1", "4.2", …) so it appears as a row in the metered
// table. step, when non-empty, runs only the matching query ("4.5"; "4" runs
// all). When report is set, each query's full execution report is printed to w.
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
	for i, nq := range rql.Timed(m, root) {
		phaseID := fmt.Sprintf("4.%d", i+1)
		if step != "" && step != "4" && step != phaseID {
			continue // --step selects one query (or "4" for the whole chapter)
		}
		if !taggable && nq.BranchScoped() {
			continue // branch-scoped query needs tags this backend can't hold
		}
		if isMet {
			met.setPhase(phaseID)
		}
		var mn, mx, sum time.Duration
		var results int
		for j := 0; j < reps; j++ {
			n, d, err := runQuery(ctx, u, nq.Q, m.Head)
			if err != nil {
				return nil, err
			}
			results = n
			if j == 0 || d < mn {
				mn = d
			}
			if d > mx {
				mx = d
			}
			sum += d
		}
		stats = append(stats, queryStat{
			id: phaseID, name: nq.Name, rql: rql.Describe(nq.Q),
			min: mn, avg: sum / time.Duration(reps), max: mx,
			results: results,
		})
		if report {
			if isMet {
				met.setPhase("report") // keep the report run's ops out of the "4.N" timing
			}
			if err := printQueryReport(ctx, w, u, phaseID, nq, m.Head); err != nil {
				return nil, err
			}
		}
	}
	return stats, nil
}

// printQueryReport runs nq once with full execution reporting on and prints the
// per-station event log to w — the --report view, shown in the queries section.
func printQueryReport(ctx context.Context, w io.Writer, u ranke.Universe, label string, nq rql.NamedQuery, archiveHead ranke.Id) error {
	arc, err := ranke.NewArchive(ctx, u, archiveHead)
	if err != nil {
		return err
	}
	q := nq.Q
	q.Execution.Report = ranke.ReportDebug
	rs, err := arc.Query(ctx, q)
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

	fmt.Fprintf(w, "\n  %s %s — %s\n", label, nq.Name, rql.Describe(nq.Q))
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
