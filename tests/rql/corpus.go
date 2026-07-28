// package: tests/rql / integration
// type:    tool
// job:     the shared RQL corpus — a broad set covering each axis of the read language (traversal, filter, shape, order, bound) for the conformance matrix, plus the small subset the timing harness measures
// limits:  queries and their names only; executing them and comparing answers live alongside (-> run.go), and the backend rows come from tests/backends
package rql

import (
	"time"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/tests/generator"
)

// Branch is the branch the branch-scoped queries confine to. The generator
// advances a single "main"; reads confined to a branch are the everyday case,
// and $universe is the privileged, unconfined exception.
const Branch = "main"

// NamedQuery pairs a stable name with an RQL query. The name keys both the
// per-query timing report and the cross-backend agreement check, so it must not
// change without intent — a renamed entry reads as a new query.
type NamedQuery struct {
	Name string
	Q    ranke.Query
}

// BranchScoped reports whether the query confines to a named branch (resolved
// via tags), as opposed to the unconfined $universe scope. A backend holding no
// tag overlay can answer only the unconfined entries.
func (nq NamedQuery) BranchScoped() bool { return nq.Q.Select.Branch != ranke.BranchUniverse }

// Timed is the small fixed set the performance harness measures — one entry per
// broad shape, kept short because each is run many times to average. Names are
// drawn from Corpus, so a timing row and a conformance row mean the same query.
func Timed(m *generator.Manifest, root ranke.Id) []NamedQuery {
	want := map[string]bool{
		"closure/branch":          true,
		"where/type-glob-source":  true,
		"where/height-ge-2":       true,
		"path/derivation-d3":      true,
		"order/height-desc-limit": true,
		"path/uses-of-sources":    true,
		"closure/universe":        true,
	}
	var out []NamedQuery
	for _, nq := range Corpus(m, root) {
		if want[nq.Name] {
			out = append(out, nq)
		}
	}
	return out
}

// Corpus is the broad conformance set: every axis of the read language, each
// entry grounded in a corner the generator produces. Every entry must match at
// least one claim (the matrix asserts it), else backends agree on nothing.
// root is the "main" branch head; m.Head the archive head.
func Corpus(m *generator.Manifest, root ranke.Id) []NamedQuery {
	// A scan of the branch: the branch is the scope, so no sub-selection and no
	// starting point. A traversal anchors at the branch head.
	sel := func() ranke.Select { return ranke.Select{Branch: Branch} }
	path := func(steps ...ranke.PathStep) ranke.Select {
		return ranke.Select{Branch: Branch, Claim: root, Path: steps}
	}
	// scanFrom narrows a branch scan to one claim's closure.
	scanFrom := func(head ranke.Id) ranke.Select {
		return ranke.Select{Branch: Branch, Head: head}
	}
	// pathFrom anchors a traversal at a claim that carries the edges the steps
	// follow — the branch head carries only contribution/* ones.
	pathFrom := func(claim ranke.Id, steps ...ranke.PathStep) ranke.Select {
		return ranke.Select{Branch: Branch, Claim: claim, Path: steps}
	}
	// archivePath spans every branch, for steps whose endpoints the generator may
	// have committed to any of them.
	archivePath := func(steps ...ranke.PathStep) ranke.Select {
		return ranke.Select{Branch: ranke.BranchArchive, Claim: m.Head, Path: steps}
	}
	// A mid-graph instant: the clock starts at Base and advances Step per claim,
	// so this splits the archive into a before and an after deterministically.
	midpoint := m.Spec.Base.Add(time.Duration(m.Spec.Size/2) * m.Spec.Step).Format(time.RFC3339Nano)

	return []NamedQuery{
		// ── traversal: the whole closure, in each of the three scopes ────────
		{"closure/branch", ranke.Query{Select: sel()}},
		{"closure/universe", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchUniverse, Head: m.Head},
		}},
		// $archive is the whole Ranke-Archive: the branch-table header's closure,
		// so it reaches the spine and every branch. Its root defaults to the
		// archive head, so Claim is deliberately left unset here.
		{"closure/archive", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive},
		}},
		{"archive/where-branch-tables", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive},
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "contribution/branches"}},
		}},

		// ── the walk's starting point ────────────────────────────────────────
		// Rooted mid-branch, not at the branch head: the walk starts here and the
		// branch confines it, so the answer is smaller than the branch. A lowering
		// that scans branch membership and ignores the root over-returns.
		{"select/root-mid-branch", ranke.Query{Select: scanFrom(m.DiffChainHead)}},
		{"select/root-mid-branch-path", ranke.Query{
			Select: ranke.Select{Branch: Branch, Claim: m.DiffChainHead,
				Path: []ranke.PathStep{{Edges: []string{"contribution/*"}, Max: 2}}},
		}},
		{"select/root-mid-archive", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive, Head: m.DiffChainHead},
		}},

		// ── traversal: typed edges to a bounded depth ───────────────────────
		// Anchored at the diff chain's head: a derivation, so it has the
		// derivation/* edges these steps follow.
		{"path/derivation-d1", ranke.Query{Select: pathFrom(m.DiffChainHead,
			ranke.PathStep{Edges: []string{"derivation/*"}, Max: 1})}},
		{"path/derivation-d3", ranke.Query{Select: pathFrom(m.DiffChainHead,
			ranke.PathStep{Edges: []string{"derivation/*"}, Max: 3})}},
		{"path/derivation-unbounded", ranke.Query{Select: pathFrom(m.DiffChainHead,
			ranke.PathStep{Edges: []string{"derivation/*"}})}},
		{"path/contribution-d2", ranke.Query{Select: path(
			ranke.PathStep{Edges: []string{"contribution/*"}, Max: 2})}},
		{"path/edges-exclude-contribution", ranke.Query{Select: pathFrom(m.DiffChainHead,
			ranke.PathStep{Edges: []string{"-contribution/*"}, Max: 3})}},

		// ── traversal: endpoint node constraints ────────────────────────────
		// Two steps: a derivation-only step cannot leave the root (a
		// contribution/head carries only contribution/* edges).
		{"path/nodes-source", ranke.Query{Select: path(
			ranke.PathStep{Nodes: []string{"derivation/*"}},
			ranke.PathStep{Edges: []string{"derivation/*"}, Nodes: []string{"source/*"}})}},
		// The sources a derivation cites are not entities, so the exclusion keeps
		// them; excluding source/* here would leave nothing, which proves nothing.
		{"path/nodes-exclude-entity", ranke.Query{Select: pathFrom(m.DiffChainHead,
			ranke.PathStep{Edges: []string{"derivation/*"}, Nodes: []string{"-entity/*"}})}},
		{"path/chain-entity-then-source", ranke.Query{Select: archivePath(
			ranke.PathStep{Nodes: []string{"entity/person"}},
			ranke.PathStep{Edges: []string{"derivation/*"}, Nodes: []string{"source/*"}})}},

		// ── traversal: direction (the reverse-walk paths) ────────────────────
		// Forward to the sources, then backward to the derivations that cite
		// them — unreachable going forward, so it exercises the reverse path
		// (native on neo4j; closure-inversion in the reference executor).
		// Scoped to the archive: "who uses this" reaches across branches, and the
		// generator commits a source and its deriver to whichever it likes.
		{"path/uses-of-sources", ranke.Query{Select: archivePath(
			ranke.PathStep{Min: ranke.Hops(0), Nodes: []string{"source/*"}},
			ranke.PathStep{Dir: ranke.DirUses, Edges: []string{"derivation/*"}, Nodes: []string{"derivation/*"}})}},
		{"path/connections-relation", ranke.Query{Select: archivePath(
			ranke.PathStep{Nodes: []string{"entity/person"}},
			ranke.PathStep{Dir: ranke.DirConnections, Edges: []string{"relation/*"}, Max: 2})}},
		{"path/uses-of-entities", ranke.Query{Select: archivePath(
			ranke.PathStep{Nodes: []string{"entity/person"}},
			ranke.PathStep{Dir: ranke.DirUses, Edges: []string{"relation/*"}})}},

		// ── filter: one operator at a time ──────────────────────────────────
		{"where/type-glob-source", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}}}},
		// Scoped to the archive: the generator may commit entities to any branch.
		{"where/type-eq-entity-person", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive},
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Eq: "entity/person"}}}},
		{"where/type-ne-source-note", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "type", Test: &ranke.Comparison{Ne: "source/note"}}}},
		{"where/type-in-set", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "type", Test: &ranke.Comparison{
				In: []any{"source/note", "entity/person", "relation/knows"}}}}},
		{"where/height-ge-2", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "height", Test: &ranke.Comparison{Ge: 2}}}},
		{"where/height-lt-3", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "height", Test: &ranke.Comparison{Lt: 3}}}},
		{"where/content-size-gt-tiny", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "content_size", Test: &ranke.Comparison{Gt: m.Spec.TinyBlobBytes}}}},
		{"where/content-size-le-tiny", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "content_size", Test: &ranke.Comparison{Le: m.Spec.TinyBlobBytes}}}},
		{"where/encoding-glob-text", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "encoding", Test: &ranke.Comparison{Glob: "text/*"}}}},
		{"where/created-at-ge-midpoint", ranke.Query{Select: sel(),
			Where: &ranke.Where{Field: "created_at", Test: &ranke.Comparison{Ge: midpoint}}}},

		// ── filter: boolean combinators ─────────────────────────────────────
		{"where/and", ranke.Query{Select: sel(), Where: &ranke.Where{And: []ranke.Where{
			{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
			{Field: "height", Test: &ranke.Comparison{Ge: 1}},
		}}}},
		{"where/or", ranke.Query{
			Select: ranke.Select{Branch: ranke.BranchArchive},
			Where: &ranke.Where{Or: []ranke.Where{
				{Field: "type", Test: &ranke.Comparison{Eq: "entity/person"}},
				{Field: "type", Test: &ranke.Comparison{Eq: "relation/knows"}},
			}}}},
		{"where/not", ranke.Query{Select: sel(), Where: &ranke.Where{
			Not: &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "contribution/*"}}}}},
		{"where/nested", ranke.Query{Select: sel(), Where: &ranke.Where{And: []ranke.Where{
			{Or: []ranke.Where{
				{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
				{Field: "type", Test: &ranke.Comparison{Glob: "derivation/*"}},
			}},
			{Not: &ranke.Where{Field: "height", Test: &ranke.Comparison{Eq: 0}}},
		}}}},

		// ── shape: how much each result carries ─────────────────────────────
		{"output/detail-id", ranke.Query{Select: sel(),
			Output: ranke.Output{Detail: ranke.DetailID}}},
		{"output/detail-graph", ranke.Query{Select: sel(),
			Output: ranke.Output{Detail: ranke.DetailGraph}}},
		{"output/detail-claims", ranke.Query{Select: sel(),
			Output: ranke.Output{Detail: ranke.DetailClaims}}},
		{"output/shape-path", ranke.Query{
			Select: pathFrom(m.DiffChainHead, ranke.PathStep{Edges: []string{"derivation/*"}, Max: 3}),
			Output: ranke.Output{Shape: ranke.ShapePath}}},
		{"output/shape-path-detail-id", ranke.Query{
			Select: pathFrom(m.DiffChainHead, ranke.PathStep{Edges: []string{"derivation/*"}, Max: 3}),
			Output: ranke.Output{Shape: ranke.ShapePath, Detail: ranke.DetailID}}},

		// ── shape: diff-chain resolution (the generator builds a long chain) ─
		{"output/form-materialized", ranke.Query{
			Select: scanFrom(m.DiffChainHead),
			Output: ranke.Output{Form: ranke.FormMaterialized}}},
		{"output/form-original", ranke.Query{
			Select: scanFrom(m.DiffChainHead),
			Output: ranke.Output{Form: ranke.FormOriginal}}},

		// ── shape: inline content and its overflow handling ──────────────────
		{"output/content-cutoff", ranke.Query{Select: sel(),
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
			Output: ranke.Output{Content: &ranke.Content{Max: 256, Overflow: ranke.OverflowCutoff}}}},
		{"output/content-omit", ranke.Query{Select: sel(),
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
			Output: ranke.Output{Content: &ranke.Content{Max: 256, Overflow: ranke.OverflowOmit}}}},
		{"output/content-reference", ranke.Query{Select: sel(),
			Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}},
			Output: ranke.Output{Content: &ranke.Content{Max: 256, Overflow: ranke.OverflowReference}}}},

		// ── order and bound ─────────────────────────────────────────────────
		{"order/height-desc-limit", ranke.Query{Select: sel(),
			Order: []ranke.OrderKey{{Field: "height", Compare: ranke.CompareNumeric, Dir: ranke.SortDesc}},
			Limit: ranke.Limit{Results: 20}}},
		{"order/height-asc", ranke.Query{Select: sel(),
			Order: []ranke.OrderKey{{Field: "height", Compare: ranke.CompareNumeric, Dir: ranke.SortAsc}}}},
		{"order/created-at-desc", ranke.Query{Select: sel(),
			Order: []ranke.OrderKey{{Field: "created_at", Dir: ranke.SortDesc}}}},
		{"order/type-lexical", ranke.Query{Select: sel(),
			Order: []ranke.OrderKey{{Field: "type", Compare: ranke.CompareLexical, Dir: ranke.SortAsc}}}},
		{"order/multi-key", ranke.Query{Select: sel(),
			Order: []ranke.OrderKey{
				{Field: "type", Compare: ranke.CompareLexical, Dir: ranke.SortAsc},
				{Field: "height", Compare: ranke.CompareNumeric, Dir: ranke.SortDesc},
			}}},
		{"limit/results-5", ranke.Query{Select: sel(), Limit: ranke.Limit{Results: 5}}},
	}
}
