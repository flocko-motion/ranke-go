// package: tests/performance / integration
// type:    tool
// job:     summarise the generated archive (totals, per-type histograms, content/shape stats) and print it once before the matrix — so the scale of a --size N run is legible (N is a scale knob, not a node count)
// limits:  read-only over the reference (mem) graph; the matrix run + per-backend timing live in harness.go
package performance

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/generator"
)

// graphOverview summarises the shape of a generated archive.
type graphOverview struct {
	claims    int // total claims in the closure (contributed + heads/branch tables)
	edges     int // total edges (each belongs to exactly one claim)
	nodeTypes map[string]int
	edgeTypes map[string]int
	encodings map[string]int // content media type → count (content-bearing claims)
	inline    int            // claims with inline content
	external  int            // claims with external content
	noContent int            // claims with no content (heads, branch tables, …)
	bytes     uint64         // total content byte length (inline + external)
	maxHeight uint64         // deepest provenance generation
	maxDegree int            // widest out-degree (most edges on one claim)

	revisions    int      // branch-table revisions in the head's provenance (§Branches)
	contributors int      // distinct signing contributors
	branches     []string // branches the head table names
}

// computeOverview walks the whole archive closure from the head — every claim
// reachable (contributed claims plus the Sequencer's heads and branch tables) —
// and tallies totals, per-type histograms, and content/shape stats. It reads
// through the universe-scoped closure query, the same traversal verification
// uses, so the claim total matches the matrix's verified count.
func computeOverview(ctx context.Context, u ranke.Universe, m *generator.Manifest) (*graphOverview, error) {
	rs, err := u.Query(ctx, ranke.Query{Select: ranke.Select{Branch: ranke.BranchUniverse, Claim: m.Head}}, ranke.Scope{Branch: ranke.BranchUniverse})
	if err != nil {
		return nil, err
	}
	ov := &graphOverview{
		nodeTypes: map[string]int{},
		edgeTypes: map[string]int{},
		encodings: map[string]int{},
	}
	for rs.Next() {
		c := rs.Result().Claim
		n := c.Node()
		ov.claims++
		ov.nodeTypes[n.Type()]++
		if h := n.Height(); h > ov.maxHeight {
			ov.maxHeight = h
		}
		switch n.ContentKind() {
		case ranke.ContentExternal:
			ov.external++
			ov.bytes += n.GetContentSize()
		case ranke.ContentInline:
			ov.inline++
			ov.bytes += n.GetContentSize()
		default:
			ov.noContent++
		}
		if enc := n.Encoding(); enc != "" {
			ov.encodings[enc]++
		}
		deg := 0
		for _, e := range c.Edges() {
			ov.edges++
			ov.edgeTypes[e.Type()]++
			deg++
		}
		if deg > ov.maxDegree {
			ov.maxDegree = deg
		}
	}
	if err := rs.Err(); err != nil {
		_ = rs.Close()
		return nil, err
	}
	if err := rs.Close(); err != nil {
		return nil, err
	}

	// Archive shape (§Branches): the revision count is the contributions merged
	// (m.Revisions) — the dev Sequencer restates each branch table in full rather
	// than diff-chaining them, so only the latest is reachable from the head. The
	// head table names the current branches via contribution/branch edges.
	ov.revisions = m.Revisions
	ov.contributors = ov.nodeTypes[ranke.NodeContributor]
	head, err := ranke.GetClaim(ctx, u, m.Head)
	if err != nil {
		return nil, err
	}
	for _, e := range head.Edges() {
		if e.Type() == ranke.EdgeTypeBranch {
			if name, err := e.GetField(ranke.FieldName); err == nil {
				ov.branches = append(ov.branches, name)
			}
		}
	}
	sort.Strings(ov.branches)
	return ov, nil
}

// printOverview renders the overview as a banner block before the matrix.
func printOverview(w io.Writer, spec generator.Spec, m *generator.Manifest, ov *graphOverview) {
	rule := strings.Repeat("═", 88)
	fmt.Fprintf(w, "\n%s\n  generated graph   seed=%d  size=%d\n%s\n", rule, spec.Seed, spec.Size, rule)
	fmt.Fprintf(w, "  claims (nodes) %d   edges %d   (%.1f edges/claim)\n",
		ov.claims, ov.edges, ratio(ov.edges, ov.claims))
	fmt.Fprintf(w, "  contributed %d + %d Sequencer heads/branch-tables = %d\n",
		m.ClaimCount, ov.claims-m.ClaimCount, ov.claims)
	fmt.Fprintf(w, "  branch-table revisions %d   contributors %d   branches (%d): %s\n",
		ov.revisions, ov.contributors, len(ov.branches), strings.Join(ov.branches, ", "))
	fmt.Fprintf(w, "  max height %d (provenance depth)   max out-degree %d\n", ov.maxHeight, ov.maxDegree)
	fmt.Fprintf(w, "  content: %d inline, %d external, %d none   %s total\n",
		ov.inline, ov.external, ov.noContent, humanBytes(ov.bytes))
	printHist(w, "node types", ov.nodeTypes)
	printHist(w, "edge types", ov.edgeTypes)
	printHist(w, "content encodings", ov.encodings)
	fmt.Fprintf(w, "%s\n", rule)
}

// printHist prints a count-sorted histogram (descending, ties broken by name).
func printHist(w io.Writer, title string, hist map[string]int) {
	if len(hist) == 0 {
		return
	}
	type kv struct {
		k string
		v int
	}
	rows := make([]kv, 0, len(hist))
	for k, v := range hist {
		rows = append(rows, kv{k, v})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].v != rows[j].v {
			return rows[i].v > rows[j].v
		}
		return rows[i].k < rows[j].k
	})
	fmt.Fprintf(w, "  %s:\n", title)
	for _, r := range rows {
		fmt.Fprintf(w, "    %-42s %6d\n", r.k, r.v)
	}
}

func ratio(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

// humanBytes renders a byte count as B / KiB / MiB.
func humanBytes(b uint64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
