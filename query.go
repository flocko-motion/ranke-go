// package: ranke / query
// type:    data
// job:     the declarative read AST (RQL — the paper's §Filtered Reads) and its result/stream shapes
// limits:  types only; the reference executor is DefaultQuery (-> query_default.go)
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
	Where     *Where     // nil = no filter
	Output    Output
	Order     []OrderKey // sort keys in priority order; empty = natural (created_at, id)
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

// Select is a generator built from four orthogonal parts: Branch is the scope,
// Head narrows it to one claim's closure, Path is the traversal, and Claim anchors
// that traversal. A Select with no Path is a scan of the scope.
type Select struct {
	Branch string // scope: BranchUniverse, BranchArchive, or a branch name
	Head   Id     // narrows the scope to this claim's closure
	Claim  Id     // anchors a traversal; nil leaves the pattern unanchored
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
// references) is the cheap default; Uses/Connections walk edges backward —
// native on a Capabilities.ReverseWalk backend, and the reference executor
// serves them by sweeping and inverting the closure (correct but O(closure)).
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

// Output shapes each result along orthogonal axes: Shape (single item or path),
// Detail (how much each entity carries), Form (as written or resolved), Encoding
// (json | cbor), and optional inline Content.
type Output struct {
	Shape    Shape          // single | path
	Detail   Detail         // id | graph | claims
	Form     Form           // original | materialized
	Content  *Content       // nil = no inline content
	Encoding ResultEncoding // serialized form of each claim (json | cbor)
}

// Content bounds inline claim content: up to Max bytes, with Overflow handling
// anything larger.
type Content struct {
	Max      int64
	Overflow Overflow
}

// Form is whether a claim is returned as written or resolved via its diff chain.
// Materialized (the resolved graph) is a graph-native backend's cheapest output;
// original is the id-defining form, served from the byte layer.
type Form string

const (
	FormMaterialized Form = "materialized" // resolved via the diff chain (default; empty == materialized)
	FormOriginal     Form = "original"     // as written (the stored/canonical claim)
)

// ResultEncoding is the serialized form of each result's claim, orthogonal to
// Output.Form (either form can be JSON or CBOR).
type ResultEncoding string

const (
	ResultJSON ResultEncoding = "json" // default; content base64 in the raw form
	ResultCBOR ResultEncoding = "cbor" // binary; the original form is the id-defining bytes
)

// Shape is whether each result is a single item or a path (the chain to it).
type Shape string

const (
	ShapeSingle Shape = "single" // individual claims (default; empty == single)
	ShapePath   Shape = "path"   // the route to each claim
)

// Detail is how much each entity carries. A claim is a graph *part* (a node and
// all its outgoing edges, which may point outside the result), so it carries more
// than graph — which is closed (only edges among the returned nodes).
type Detail string

const (
	DetailID     Detail = "id"     // identities only
	DetailGraph  Detail = "graph"  // closed graph: nodes + edges among them
	DetailClaims Detail = "claims" // claim parts: node + all outgoing edges (default; empty == claims)
)

// Overflow is how content larger than Content.Max is handled.
type Overflow string

const (
	OverflowCutoff    Overflow = "cutoff"    // truncate at the cap
	OverflowOmit      Overflow = "omit"      // drop the content
	OverflowReference Overflow = "reference" // return a hash stub in its place
)

// OrderKey is one sort key. Keys apply in priority order; ties fall back to the
// natural (created_at, id) order.
type OrderKey struct {
	Field   string
	Compare Collation // how values compare; empty = lexical
	Dir     SortDir   // asc | desc; empty = asc
}

// Collation is how ordered values compare.
type Collation string

const (
	CompareLexical Collation = "lexical" // string comparison (default)
	CompareNumeric Collation = "numeric" // numeric comparison
)

// SortDir is a sort direction.
type SortDir string

const (
	SortAsc  SortDir = "asc"  // ascending (default)
	SortDesc SortDir = "desc" // descending
)

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

// Scope is the branch context a query runs in — the Archive's resolution of
// q.Select.Branch. Head confines to closure(Head) (nil = unconfined); the rest
// lets a native backend prune by tag/height (a byte store uses only Head).
type Scope struct {
	Head     Id     // closure anchor; nil = unconfined ($universe)
	Branch   string // resolved branch name — a native prune key
	Height   uint64 // branch-head height
	Revision int    // archive spine revision (_br)
}

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
	// Id is always set. Claim is nil for DetailID (no reconstruction needed).
	Id    Id
	Claim Claim
	// Path is the full route to the claim (root first), set only when
	// Output.Shape is ShapePath.
	Path []Claim
	// Content is the claim's inlined content, present only when
	// Output.Content > 0; truncated per Output.Overflow when it exceeds the cap.
	Content []byte
}

// QueryReport, QueryEvent, and the reporting machinery live in query_report.go.
