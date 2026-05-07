package ranke

import "time"

// Public exported types and the unexported concrete implementations
// of the interfaces declared in interfaces.go.
//
// Two layers in this file:
//
//  1. Closed-vocabulary enums (NodeClass, EdgeClass, EncodingClass,
//     RelationDirection) and config structs (ClaimConfig, EdgeConfig)
//     — public, used at the API boundary.
//  2. Concrete unexported types (hash, edge, node, claim, store,
//     graph, branch, branchEntry, memoryPersistence) — implementation,
//     never returned bare; constructors return interface types.
//
// Internal storage convention: Type and Encoding are stored split
// into class + sub fields. The combined "class/sub" string is
// reconstructed only in the public accessors. Class and sub are
// validated at construction (NewClaim, NewEdge); after that they
// are immutable.

// --- Closed-vocabulary enums (paper §4.7, §4.8) ---

// NodeClass is the closed top-level vocabulary for node types (§4.8).
type NodeClass string

const (
	NodeSource       NodeClass = "source"
	NodeDerivation   NodeClass = "derivation"
	NodeEntity       NodeClass = "entity"
	NodeRelation     NodeClass = "relation"
	NodeContribution NodeClass = "contribution"
)

// EdgeClass is the closed top-level vocabulary for edge types (§4.8).
type EdgeClass string

const (
	EdgeDerivation   EdgeClass = "derivation"
	EdgeRelation     EdgeClass = "relation"
	EdgeContribution EdgeClass = "contribution"
)

// EncodingClass is the closed top-level vocabulary for MIME types
// (RFC 6838: the IANA-registered top-level media types). The subtype
// portion remains open.
type EncodingClass string

const (
	EncodingApplication EncodingClass = "application"
	EncodingAudio       EncodingClass = "audio"
	EncodingExample     EncodingClass = "example"
	EncodingFont        EncodingClass = "font"
	EncodingImage       EncodingClass = "image"
	EncodingMessage     EncodingClass = "message"
	EncodingModel       EncodingClass = "model"
	EncodingMultipart   EncodingClass = "multipart"
	EncodingText        EncodingClass = "text"
	EncodingVideo       EncodingClass = "video"
)

// RelationDirection tags an entity's role on a relation/* edge (§4.7).
//
// The zero value means "not a relation edge"; non-relation edges
// always carry RelationDirection == 0. A relation/* edge always
// carries RelationFrom (+1) or RelationTo (-1). All-from or all-to
// on a relation node expresses a symmetric relation (e.g.
// "are_friends"); no entity is distinguished by role.
type RelationDirection int8

const (
	// RelationFrom is the +1 direction.
	RelationFrom RelationDirection = 1
	// RelationTo is the -1 direction.
	RelationTo RelationDirection = -1
)

// --- Config structs (data-only inputs to constructors) ---

// ClaimConfig is the data-only input to NewClaim.
//
// Required: TypeClass and TypeSub. Content and ContentHash are
// mutually exclusive: set Content to have NewClaim hash the bytes
// for you, or set ContentHash directly when the content lives
// outside the graph. CreatedAt defaults to time.Now().UTC() when
// zero.
//
// Contributor is required for every claim except the root
// contribution/contributor claim (§4.3). NewClaim auto-builds the
// claim's contribution/contributor edge from this field; do not also
// add it to Edges. For the root contribution/contributor claim, leave
// Contributor nil — NewClaim recognizes the type and self-attributes.
//
// Provenance invariant (§3.5): derivation/*, entity/*, and
// relation/* claims are by definition built from existing claims —
// they MUST carry at least one derivation/* edge in Edges (or
// otherwise reference their sources through derivation/* edges).
// NewClaim rejects them with an error if no derivation/* edge is
// present. source/* and contribution/* claims have no such
// requirement (sources are externally captured; contribution/*
// claims carry contribution/* edges instead).
//
// Edges holds the additional edges (provenance, relation, structural)
// for the claim. The contribution/contributor edge is added by
// NewClaim and need not be supplied.
type ClaimConfig struct {
	// TypeClass is the closed-vocabulary node class (§4.8). Required.
	TypeClass NodeClass
	// TypeSub is the open-vocabulary subtype, e.g. "email", "contributor".
	// Required.
	TypeSub string
	// EncodingClass is the closed-vocabulary MIME top-level type (RFC 6838).
	EncodingClass EncodingClass
	// EncodingSub is the open-vocabulary subtype, e.g. "plain", "rfc822".
	EncodingSub string
	// Content and ContentHash are mutually exclusive: set Content
	// to have NewClaim hash the bytes, or set ContentHash directly
	// when the content lives outside the graph.
	Content     []byte
	ContentHash Id
	// CreatedAt defaults to time.Now().UTC() when zero.
	CreatedAt time.Time
	// Contributor is required for every claim except the root
	// contribution/contributor claim. NewClaim auto-builds the
	// contribution/contributor edge from this field; do not also
	// add it to Edges. For the root contribution/contributor claim,
	// leave Contributor nil — NewClaim recognizes the type and
	// self-attributes (§4.3).
	Contributor Contributor
	// Edges holds the additional edges (provenance, relation,
	// structural). The contribution/contributor edge is added by
	// NewClaim and need not be supplied.
	Edges []Edge
	// Fields holds additional implementation-defined fields on the
	// node (§4.1). They participate in the canonical serialization
	// and are covered by the node's id.
	Fields map[string]string
}

// EdgeConfig is the data-only input to NewEdge.
//
// Required: Reference, Type. Content and ContentHash are mutually
// exclusive (same rule as ClaimConfig). RelationDirection must be set
// (RelationFrom or RelationTo) for relation/* edges and left zero
// otherwise; NewEdge enforces this.
type EdgeConfig struct {
	// Reference is the id of the older claim this edge points at.
	// Required.
	Reference Id
	// TypeClass is the closed-vocabulary edge class (§4.8). Required.
	TypeClass EdgeClass
	// TypeSub is the open-vocabulary subtype, e.g. "head", "branch".
	// Required.
	TypeSub string
	// EncodingClass is the closed-vocabulary MIME top-level type (RFC 6838).
	EncodingClass EncodingClass
	// EncodingSub is the open-vocabulary subtype.
	EncodingSub string
	// Content and ContentHash are mutually exclusive (same rule as
	// ClaimConfig).
	Content     []byte
	ContentHash Id
	// RelationDirection must be set (RelationFrom or RelationTo) for
	// relation/* edges and left zero otherwise; NewEdge enforces this.
	RelationDirection RelationDirection
	// Fields holds additional implementation-defined fields on the
	// edge (§4.2). They participate in the canonical serialization
	// and are covered by the edge's id.
	Fields map[string]string
}

// --- Concrete unexported implementations ---

// hash is the concrete implementation of Id. Wraps an IPFS multihash
// (raw) and caches its multibase-encoded string form for cheap String()
// calls and for use as a map key.
type hash struct {
	raw []byte // multihash bytes (sha2-256 prefix + digest)
	str string // multibase-encoded form, cached
}

// edge is the concrete implementation of Edge. Created via NewEdge and
// then immutable. id is computed once at construction from the
// canonical serialization of the other fields.
type edge struct {
	reference         Id
	typeClass         EdgeClass
	typeSub           string
	encodingClass     EncodingClass
	encodingSub       string
	contentHash       Id     // nil when no content
	content           []byte // raw content bytes, kept with the edge
	relationDirection RelationDirection
	fields            map[string]string // additional implementation-defined fields (§4.2)
	id                Id                // = H(S(edge))
}

// node is the concrete implementation of Node. The edges field carries
// the ids of edges created with the owning claim, in canonical sort
// order; this set participates in the node hash.
type node struct {
	typeClass     NodeClass
	typeSub       string
	encodingClass EncodingClass
	encodingSub   string
	contentHash   Id     // nil when no content
	content       []byte // raw content bytes, kept with the node
	createdAt     time.Time
	edges         []Id              // edge ids, sorted canonically
	fields        map[string]string // additional implementation-defined fields (§4.1)
	id            Id                // = H(S(node)); also the claim id
}

// claim is the concrete implementation of Claim. A claim is its node
// together with the full edge records (the node only carries edge ids).
// Atomically created in NewClaim; immutable after.
//
// contributor is the contributor claim for this claim (§4.3): always
// a contribution/contributor claim. For the root
// contribution/contributor claim, contributor is a self-reference;
// for every other claim it is the claim referenced by this claim's
// contribution/contributor edge.
type claim struct {
	node        *node
	edges       []*edge // same order as node.edges
	contributor Contributor
}

// store is the concrete implementation of Store (= (U, B) from §4.6).
//
// U contains all graphs the store knows about, and graphs consist of
// claims, so U transitively contains all claims. Internally we
// deduplicate: claims (keyed by Id.String()) backs every graph;
// graphs indexes registered graphs by head id; content holds the
// raw bytes addressed by ContentHash on nodes and edges.
//
// branches is B: a map from branch name to the id of the most-recent
// contribution/branch claim for that name (concept-team directive,
// 2026-05-07). Branches themselves are ordinary claims in the claims
// map; branches just provides the name → latest-claim-id index.
//
// The public API never exposes the claims map directly — claim-level
// operations live on Graph, not on Store. Listing of U is not
// possible; only graph-by-head, content-by-id, and branch-by-name
// lookups are.
type store struct {
	claims   map[string]*claim // cache when backend != nil; source of truth otherwise
	content  map[string][]byte // same: cache + truth
	branches map[string]Id     // always full in memory (B is small)
	backend  storeBackend      // nil ⇒ pure in-memory; non-nil ⇒ persistent
}

// branch is the concrete implementation of Branch — a thin
// projection of a contribution/branch claim. Constructed by
// GetBranch / Branches() from the underlying claim referenced by
// store.branches. The provenance chain is pre-fetched eagerly so
// that the Branch value is self-contained and survives Store reload
// (no back-pointer to the Store).
//
// claim is the current contribution/branch claim; chain is the
// pre-walked provenance (most-recent first), excluding the current
// binding.
type branch struct {
	claim *claim
	chain []*branchEntry
}

// branchEntry is the concrete implementation of BranchEntry — one
// historical contribution/branch claim discovered by walking the
// chain backwards from a branch's current claim. Self-contained:
// holds only the claim, every interface method derives from it.
type branchEntry struct {
	claim *claim
}

// graph is the concrete implementation of Graph (= RG ⊆ U, §4.4).
//
// A graph is a mutable subset: AddClaim grows it, the rest of the
// Graph methods read it. claims holds every claim in the subset
// (keyed by Id.String()); referenced records every claim id that
// some other claim in this graph references via an edge — so
// Heads() = claims \ referenced. Single-headed graphs (RG_h, §4.5)
// have len(heads)==1; multi-headed graphs have more.
type graph struct {
	claims     map[string]*claim
	referenced map[string]struct{}
}

// memoryPersistence is the in-memory implementation of Persistence.
//
// blobs holds the canonical-encoded bytes of each claim, keyed by the
// claim's hash string — content-addressed by construction. A graph is
// reconstructed by reading its head blob and walking references.
type memoryPersistence struct {
	blobs map[string][]byte
}
