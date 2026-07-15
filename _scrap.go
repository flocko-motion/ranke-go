// package: core / orchestration
// type:    domain types
// job:     the capability surface's domain values — the query language, results, and verification model
// limits:  types only; the pipeline that carries them is request.go/core.go (-> adapters/endpoints)
//
// These are the typed values a Request carries in and out: the declarative query
// AST (the paper's §Filtered Reads), the result/stream shapes, and the
// verification-run model. They were drafted in the endpoint-side coreapi package;
// they are core's domain, so they live here and the endpoint adapters import core
// — not the other way round.
package core

import (
	"io"
	"time"

	"github.com/flocko-motion/ranke-go"
)

// --- Query -----------------------------------------------------------------

// Query is a declarative read: generate a set of claims (Select), filter it
// (Where), shape each result (Output), then order and bound the read.
type Query struct {
	Select    Select
	Where     *Where // nil = no filter
	Output    Output
	Order     *Order // nil = default order, (created_at, id)
	Limit     Limit
	Execution Execution
}

// Select is a generator: a starting point and a traversal. Branch roots at a
// branch's current head; Claim optionally roots at a claim id within it; Claim
// with an empty Branch is the privileged $universe read. An empty Path follows
// every edge outward to the full closure.
type Select struct {
	Branch string
	Claim  ranke.Id
	Path   []PathStep
}

// PathStep follows typed edges to a bounded depth, optionally constraining the
// endpoint node types. A leading "-" on an Edges or Nodes entry excludes that type.
type PathStep struct {
	Edges []string
	Dir   Direction // default DirProvenance
	Depth int       // max hops; 0 = unbounded for this step
	Nodes []string
}

// Direction is which way a step follows an edge.
type Direction string

const (
	DirProvenance  Direction = "provenance"  // outgoing (default)
	DirUses        Direction = "uses"        // incoming
	DirConnections Direction = "connections" // either
)

// Where is a boolean tree of comparisons. Exactly one of And, Or, Not, or a leaf
// (Field + Test) is set on any node.
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
	Glob string // shell-style wildcard
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

// Execution selects where the query runs and whether it reports on itself.
type Execution struct {
	Layer  string // pin to one named storage layer (see StorageLayer); empty = ranke-db chooses
	Report bool   // when true, the stream ends with a QueryReport
}

// ResultStream streams query results, one at a time, in the query's order. After
// Next returns false, check Err; Report is non-nil only when Execution.Report was set.
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
	Claim ranke.Claim
	// Path is the full route to the claim, set only when Output.Detail is DetailPath.
	Path []ranke.Claim
	// Content is the claim's inlined content, present only when Output.Content > 0;
	// truncated per Output.Overflow when it exceeds the cap.
	Content []byte
}

// QueryReport ends a stream when Execution.Report is set — a report of how the
// query ran.
type QueryReport struct {
	Engine    string        // which engine ran the query
	Layer     string        // which storage layer answered
	Lowered   string        // the query as that engine ran it
	Elapsed   time.Duration // wall-clock time
	Results   int           // items emitted
	Truncated bool          // whether Limit cut the read short
}

// --- Contribute / operate --------------------------------------------------

// Contribution is the outcome of a merge: the new head and the contributed ids.
type Contribution struct {
	Head ranke.Id
	Ids  []ranke.Id
}

// Health is liveness plus the contributor identity the stack signs merges with.
type Health struct {
	Status string // "ok" when serving
	Signer string // signing/contributor identity (e.g. "ed25519:…")
}

// StorageLayer names one storage layer — name and type only, by design.
type StorageLayer struct {
	Name string
	Type string // backend kind: mem, fs, sqlite, s3, postgres, neo4j, …
}

// Content streams a blob's bytes; the read side of Contribute's io.Reader body.
type Content = io.ReadCloser

// --- Verification ----------------------------------------------------------

// VerificationConfig parameters a run.
type VerificationConfig struct {
	Closure          string            // root — a branch name or a head id
	Layer            string            // optional single storage layer to check directly
	Depth            VerificationDepth //
	ContentThreshold int64             // for full-content, max bytes checked per claim; 0 = all
}

// VerificationDepth is how thoroughly a run checks each claim.
type VerificationDepth string

const (
	DepthCompleteness      VerificationDepth = "completeness"       // every referenced claim is present
	DepthRecordCorrectness VerificationDepth = "record-correctness" // ids and signatures check out
	DepthFullContent       VerificationDepth = "full-content"       // content matches its hash too
)

// RunStatus is the lifecycle state of a verification run.
type RunStatus string

const (
	RunRunning  RunStatus = "running"
	RunComplete RunStatus = "complete"
	RunStopped  RunStatus = "stopped" // cancelled; partial findings kept
	RunError    RunStatus = "error"
)

// VerificationReport is a point-in-time record of a run.
type VerificationReport struct {
	ID            string
	Config        VerificationConfig
	Head          ranke.Id // the head the run pinned at start
	Status        RunStatus
	StartedAt     time.Time
	CompletedAt   time.Time // zero while running
	ClaimsChecked int64
	BytesRead     int64
	OK            bool // true when no failures were found
	Failures      []VerificationFailure
}

// VerificationFailure is one integrity problem a run found.
type VerificationFailure struct {
	ID     ranke.Id
	Mode   FailureMode
	Layer  string // where it was found
	Detail string
}

// FailureMode classifies an integrity problem.
type FailureMode string

const (
	FailureCorruptBytes   FailureMode = "corrupt-bytes"   // stored bytes do not match their hash
	FailureInvalidContent FailureMode = "invalid-content" // the claim itself does not validate
)
