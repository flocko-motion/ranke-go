// package: ranke / query
// type:    data
// job:     the declarative read AST (RQL — the RankeDB paper's §Filtered Reads), its
//
//	result/stream shapes, and the visibility Scope — the value types Universe.Query carries
//
// limits:  types only; the reference executor is DefaultQuery (-> query_default.go); a capable
//
//	backend lowers the same AST natively (e.g. neo4j → Cypher)
package ranke

import "time"

// Query is a declarative read (RQL): generate a set of claims (Select), filter
// it (Where), shape each result (Output), then order and bound the read. It is
// the ADT's native read language; the Universe answers it via Universe.Query,
// either through the reference DefaultQuery (a byte store) or a native lowering
// (a graph-native backend). A query's meaning is fixed here and unchanged by
// which layer answers it.
type Query struct {
	Select    Select
	Where     *Where // nil = no filter
	Output    Output
	Order     *Order // nil = default order, (created_at, id)
	Limit     Limit
	Execution Execution
}

// Reserved virtual branch scopes for Select.Branch (the $-target family, spec
// §Access). A real branch name confines a query to that branch's closure;
// these two name the archive-wide and unconfined scopes.
const (
	// BranchUniverse applies no confinement — privileged access rooted at
	// Select.Claim, which is therefore required. The explicit form of "no
	// branch"; an empty Branch is not allowed.
	BranchUniverse = "$universe"
	// BranchArchive confines to the whole Ranke-Archive: the closure of the
	// branch-table header. Select.Claim defaults to the current archive head.
	BranchArchive = "$archive"
)

// Select is a generator: a scope, a root, and a traversal. Branch is the
// mandatory scope — a real branch name (confined to that branch), or the
// reserved BranchUniverse / BranchArchive. Claim is the root within the scope:
// required under BranchUniverse (there is no head to default to), optional
// otherwise (the archive layer defaults it to the scope's current head — the
// branch head, or the branch-table header for BranchArchive). An empty Path
// follows every edge outward to the full closure (§Closures).
type Select struct {
	Branch string // scope: BranchUniverse, BranchArchive, or a branch name
	Claim  Id     // root within the scope; required under BranchUniverse
	Path   []PathStep
}

// PathStep follows typed edges to a bounded depth, optionally constraining the
// endpoint node types. A leading "-" on an Edges or Nodes entry excludes that
// type; entries are glob patterns over "class/sub".
type PathStep struct {
	Edges []string
	Dir   Direction // default DirProvenance
	Depth int       // max hops; 0 = unbounded for this step
	Nodes []string
}

// Direction is which way a step follows an edge. Provenance (outgoing, toward
// references) is always available; Uses/Connections require a backend that can
// walk edges backward (Capabilities.ReverseWalk) — the reference executor
// refuses them rather than sweep the whole closure.
type Direction string

const (
	DirProvenance  Direction = "provenance"  // outgoing (default) — follow each edge to its reference
	DirUses        Direction = "uses"        // incoming — the claims that reference this one
	DirConnections Direction = "connections" // either direction
)

// Where is a boolean tree of comparisons. Exactly one of And, Or, Not, or a
// leaf (Field + Test) is set on any node.
type Where struct {
	And   []Where
	Or    []Where
	Not   *Where
	Field string      // leaf: the field tested
	Test  *Comparison // leaf: the comparison on Field
}

// Comparison tests one field with exactly one operator.
type Comparison struct {
	Eq   any
	Ne   any
	Lt   any
	Le   any
	Gt   any
	Ge   any
	In   []any  // set membership
	Glob string // shell-style wildcard (path.Match)
}

// Output shapes each result.
type Output struct {
	Detail   Detail
	Content  int64    // max inlined content bytes per claim; 0 = none
	Overflow Overflow // how content past Content is handled
}

// Detail sets how much each result carries.
type Detail string

const (
	DetailID    Detail = "id"    // just the id
	DetailClaim Detail = "claim" // the reached claim (default)
	DetailPath  Detail = "path"  // the whole route to it
)

// Overflow is how content larger than Output.Content is handled.
type Overflow string

const (
	OverflowCutoff    Overflow = "cutoff"    // truncate at the cap
	OverflowOmit      Overflow = "omit"      // drop the content
	OverflowReference Overflow = "reference" // return a hash stub in its place
)

// Order is a named sort; without it, results order by (created_at, id).
type Order struct {
	Field string
	Desc  bool
}

// Limit bounds a read.
type Limit struct {
	Results int           // max claims; 0 = unbounded
	Time    time.Duration // execution budget; 0 = none
}

// Execution selects where the query runs and how deeply it reports on itself.
type Execution struct {
	Layer string // pin to one named storage layer; empty = the backend chooses
	// Report sets the execution-report verbosity threshold (see ReportLevel):
	// empty = no report; ReportInfo for high-level stages; ReportDebug for
	// routing/lowering; ReportTrace for per-claim detail. When set, the stream
	// carries a QueryReport (ResultStream.Report).
	Report ReportLevel
}

// Scope is an injected visibility predicate applied to every candidate claim:
// mechanism (this library) applies it, policy (the server layer) supplies it,
// so the ADT enforces what the server decides without knowing about users or
// grants. A nil Scope admits everything.
type Scope func(Claim) bool

// ResultStream streams query results, one at a time, in the query's order.
// After Next returns false, check Err; Report is non-nil only when
// Execution.Report was set.
type ResultStream interface {
	// Next advances to the next result, returning false at end of stream or error.
	Next() bool
	// Result returns the current result (valid after Next returned true).
	Result() QueryResult
	// Report returns the query's report, available after Next returns false when
	// Execution.Report was set; nil otherwise.
	Report() *QueryReport
	// Err returns the first error that stopped the stream, if any.
	Err() error
	// Close releases the stream's resources.
	Close() error
}

// QueryResult is one reached claim, shaped per Output.
type QueryResult struct {
	Claim Claim
	// Path is the full route to the claim (root first), set only when
	// Output.Detail is DetailPath.
	Path []Claim
	// Content is the claim's inlined content, present only when
	// Output.Content > 0; truncated per Output.Overflow when it exceeds the cap.
	Content []byte
}

// QueryReport, QueryEvent, and the reporting machinery live in query_report.go.
