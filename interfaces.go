// Package ranke is the Go reference implementation of the
// Ranke-Graph ADT (spec §4).
//
// Architecture follows the spec directly:
//
//   - Universe (𝒰, §4.5) — a content-addressed bag of claims and
//     content bytes. Composable: NewFsUniverse, NewMemUniverse, plus
//     downstream backends (S3, Neo4j, ...) that satisfy the Universe
//     interface.
//
//   - BranchTableHead (B_h, §4.7) — persists the single mutable Id of
//     the current contribution/branches claim. NewFsBranchTableHead,
//     NewMemBranchTableHead, or anything else that satisfies it.
//
//   - Archive (§4.8) — the (𝒰, B_h) tuple. NewArchive(u, bth) composes
//     them. No per-backend factories: callers compose explicitly so
//     the shape of the deployment is visible at the call site.
//
// Multiple Archives may share one Universe. Archive does not own
// either of its dependencies — closing an Archive does not close
// the Universe or BranchTableHead. The caller does.
//
// Higher-level concerns (queries, indices, cache stacks, federation)
// live in the application layer above this library.
package ranke

import (
	"context"
	"crypto"
	"time"
)

// Filter selects a subset of edges matching some criterion.
//
// Filters are passed to methods that return edge collections (e.g.
// Claim.Edges) as a variadic list — every filter must match (AND).
// Callers can implement Filter to add custom matchers; the package
// ships NewTypeFilter and NewEncodingFilter for the common cases,
// and they can be composed via the variadic Edges call.
type Filter interface {
	// Match reports whether the given edge matches this filter.
	Match(e Edge) bool
}

// Id is a self-describing, content-addressed identifier.
//
// Per spec §4, id(v) = Sign(H(S(v))) for nodes and id(e) = H(S(e))
// for edges, where S is the canonical serialization and H is a
// cryptographic hash. The reference implementation uses CBOR
// Deterministic encoding (RFC 8949 §4.2) for S and IPFS multihash
// with SHA2-256 for H — so an Id is implemented as a self-describing
// multihash, but the public interface treats it as an opaque id.
//
// Id values are immutable; methods never mutate the receiver. The raw
// digest bytes are deliberately not exposed on the interface — that
// is internal tooling for storage backends, not part of the public
// surface.
type Id interface {
	// String returns a multibase-encoded, self-describing
	// representation suitable for serialization and display.
	String() string
	// Equal reports whether two ids name the same record.
	Equal(other Id) bool
	// Algorithm returns the human-readable name of the hash
	// algorithm used to build this id (e.g. "sha2-256"). The
	// reference implementation produces "sha2-256" for every id,
	// but a Archive may receive ids built with other algorithms
	// from external sources.
	Algorithm() string
}

// Edge is a directed reference from the owning claim to a prior claim.
//
// Each edge is part of exactly one claim, recoverable as the claim whose
// node lists the edge's id in its edges set. The owning claim's id is
// never stored on the edge — that would make S(e) depend on itself
// (§4.2). Direction is universal: every edge runs from an older claim
// (its Reference) to the newer claim that owns it.
type Edge interface {
	// Reference returns the id of the referenced (older) claim.
	Reference() Id
	// Type is the full class/subtype string, e.g. "derivation/chunk",
	// "relation/family", "contribution/contributor".
	Type() string
	// TypeClass is the first segment of Type, from the closed edge
	// class vocabulary (§4.8).
	TypeClass() EdgeClass
	// TypeSub is the second segment of Type — open vocabulary.
	TypeSub() string
	// Content is the edge's inline content (paper §4.2 simplified
	// schema). Bytes travel with the edge — there is no separate
	// content_hash for edges. Empty for edges that carry no content.
	Content() []byte
	// RelationDirection is RelationFrom (+1) or RelationTo (-1) on
	// relation/* edges, and 0 on non-relation edges (§4.7). Stored
	// as one of the additional implementation-defined fields.
	RelationDirection() RelationDirection
	// HasField reports whether an additional implementation-defined
	// field with the given name is set on this edge (paper §4.2:
	// "additional implementation-defined fields").
	HasField(name string) bool
	// GetField retrieves an additional implementation-defined field
	// by name. Such fields participate in the canonical
	// serialization just like the structural ones, so they are
	// covered by the edge's id. Returns an error if the named field
	// is not set.
	GetField(name string) (string, error)
	// Fields returns the names of every additional field set on
	// this edge.
	Fields() []string
	// ID is the content-addressed identifier of this edge.
	ID() Id
}

// Node is the structural component of a claim. A node's id is the claim id.
//
// Two nodes with identical content but different provenance produce
// different ids (§4.1).
type Node interface {
	// Type is the full class/subtype string, e.g. "source/conversation".
	Type() string
	// TypeClass is the first segment of Type, from the closed node
	// class vocabulary (§4.8).
	TypeClass() NodeClass
	// TypeSub is the second segment of Type — open vocabulary.
	TypeSub() string
	// ContentHash is H(content); nil if the node carries no content.
	// The bytes themselves live in implementation-defined storage,
	// addressed by ContentHash.
	ContentHash() Id
	// Size is the byte-length of the content addressed by ContentHash.
	// 0 when the node has no content. Paired with ContentHash in the
	// canonical encoding so verifiers detect truncation/extension and
	// storage layers can know the size without loading the bytes.
	Size() uint64
	// Content returns the node's content bytes. Returns (nil, nil)
	// if the node has no content. The bytes travel with the claim;
	// no Archive lookup is required.
	Content() ([]byte, error)
	// Encoding is the full MIME media type (RFC 6838) telling
	// consumers how to interpret the content bytes (§4.9), e.g.
	// "message/rfc822".
	Encoding() string
	// EncodingClass is the first segment of Encoding, from the
	// closed RFC 6838 top-level type vocabulary.
	EncodingClass() EncodingClass
	// EncodingSub is the second segment of Encoding — open vocabulary.
	EncodingSub() string
	// CreatedAt is the UTC timestamp the claim was added to the graph.
	// Not the time of any external artifact the claim may represent.
	CreatedAt() time.Time
	// Edges returns the ids of edges created with this claim, in
	// canonical (sort) order.
	Edges() []Id
	// HasField reports whether an additional implementation-defined
	// field with the given name is set on this node (paper §4.1:
	// "additional implementation-defined fields").
	HasField(name string) bool
	// GetField retrieves an additional implementation-defined field
	// by name. Such fields participate in the canonical
	// serialization just like the structural ones, so they are
	// covered by the node's id. Returns an error if the named field
	// is not set.
	GetField(name string) (string, error)
	// Fields returns the names of every additional field set on
	// this node.
	Fields() []string
	// Pubkey returns the multikey-encoded public key on this node
	// (paper §4.1, §5.7). Non-empty only on contributor claims that
	// declare a key — i.e. a signed contributor. Empty for every
	// other claim and for unsigned contributors (identity-Sign case).
	Pubkey() []byte
	// Title returns the node's optional short text label, or the
	// empty string if none is set. Title is omitted from the
	// canonical encoding when empty, so an unset Title doesn't
	// affect the claim's id.
	Title() string
	// ID is the content-addressed identifier of this node and thus of
	// the owning claim.
	ID() Id
}

// Claim is a node together with the edges in its edges set.
//
// A claim is created in a single atomic transaction (§4.3); nothing can
// be added to it afterward. The node's hash covers every edge created
// with it, so ID is final at construction time.
type Claim interface {
	// Node returns the structural component of the claim.
	Node() Node
	// Edges returns the edges created with this claim, in the same
	// canonical order as Node().Edges(). The contribution/contributor
	// edge enforced by §4.3 is included.
	//
	// If filters are supplied, only edges matching every filter (AND)
	// are returned. Calling Edges() with no filters returns all edges.
	Edges(filters ...Filter) []Edge
	// Contributor returns the contributor for this claim —
	// always a contribution/contributor claim (§4.3).
	//
	// Every claim has exactly one contributor: the claim referenced
	// by its contribution/contributor edge. For the root
	// contribution/contributor claim — which has no edges because it
	// cannot reference a prior contributor — Contributor returns the
	// claim itself (self-attribution).
	Contributor() Contributor
	// IsContributor reports whether this claim is itself a
	// contribution/contributor claim — i.e. its node type is
	// "contribution/contributor". Convenience over inspecting
	// Node().TypeClass() and Node().TypeSub().
	IsContributor() bool
	// AsContributor returns this claim as a Contributor for typed
	// access. Errors if the claim is not of node type
	// "contribution/contributor".
	//
	// If a signing key is supplied, it must match the contributor's
	// pubkey field — the returned Contributor is then wrapped via
	// WithSigningKey so subsequent claims attributed to it sign
	// automatically. A key-vs-pubkey mismatch (or a key on an
	// unsigned contributor) is returned as an error. Skip the key
	// for an unwrapped contributor (e.g. for a downstream that just
	// references it).
	AsContributor(signingKey ...crypto.Signer) (Contributor, error)
	// ID is the claim's content-addressed identifier (= Node().ID()).
	ID() Id
}

// Contributor is a typed view over a Claim whose node type is
// "contribution/contributor". Largely overlaps with Claim — every
// Contributor IS a Claim, by interface embedding — but the named
// type buys type safety at function signatures: `func f(c Contributor)`
// documents intent and lets compile-time checks catch the case
// where a function expecting a contributor is given a plain Claim.
//
// The package's concrete implementation of Claim trivially satisfies
// Contributor too (Contributor adds no methods to Claim's set).
// The blessed path to obtain a Contributor is through one of the
// validating accessors below — they check the node type before
// returning a Contributor:
//
//   - Claim.AsContributor — cast this claim to Contributor
//   - Claim.Contributor   — contributor of any claim (always typed)
//   - Branch.Contributor  — contributor of a branch's binding
//   - BranchEntry.Contributor — contributor of a historical binding
//
// Direct type assertion (claim.(Contributor)) is technically possible
// but bypasses validation; callers should use the blessed path.
//
// The interface is intentionally minimal in v0 — it exists as a
// typed marker. Convenience accessors specific to contributors
// (Name, public key, etc.) can be added later as the content schema
// of contribution/contributor claims solidifies.
type Contributor interface {
	Claim
	// SigningKey returns the private key matching this contributor's
	// pubkey, or nil when the contributor has no key on record
	// (identity-Sign case — paper §5.7). NewClaim, SetBranch, and
	// Consolidate consult it for any claim minted on this
	// contributor's behalf, so the key threads through the API
	// without an explicit parameter at every site.
	//
	// The Ranke-Graph itself stores no private keys: a bare
	// contributor claim (returned from AsContributor or loaded
	// from disk) returns nil here. To attach a key for use during
	// a session, wrap the contributor with WithSigningKey.
	SigningKey() crypto.Signer
}

// Archive is the (𝒰, B_h) tuple from spec §4.8. Branches and graphs
// are projections of claims that live in the underlying Universe.
//
// Every method takes a ctx; pass context.Background() if there's
// nothing to cancel. The library honors ctx.Done() between Universe
// calls.
type Archive interface {
	HasGraph(ctx context.Context, head Id) bool
	GetGraph(ctx context.Context, head Id) (Graph, error)

	HasBranch(ctx context.Context, name string) bool
	GetBranch(ctx context.Context, name string) (Branch, error)
	SetBranch(ctx context.Context, name string, g Graph, contributor Contributor, createdAt ...time.Time) error
	Branches(ctx context.Context) []Branch

	// VerifyBranch loads the closure rooted at the branch's latest
	// head and runs the spec §5.10 checks across it.
	VerifyBranch(ctx context.Context, name string) error
}

// Branch is a convenience view over a contribution/branch claim.
//
// Per the §4.6 model, B is a map[string]Id from branch name to the id
// of the most-recent contribution/branch claim for that name. The
// branch claim itself is an ordinary record in U:
//
//   - node type:    "contribution/branch"
//   - node content: the branch name, in text/plain (self-describing,
//     redundant with B's key but useful for self-identifying claims)
//   - edges (three; see SetBranch for the construction algorithm):
//   - contribution/contributor → the contributor (required by §4.3)
//   - contribution/head        → the head being bound
//   - contribution/branch      → the previous branch claim for this
//     name (omitted for the first binding)
//
// The three distinct edge subtypes make every reference's purpose
// unambiguous: walking provenance is "follow the contribution/branch
// edge"; reading the bound head is "follow the contribution/head edge".
//
// Branch is a read-only projection of that claim, exposing Name,
// the Latest binding (as a BranchEntry), and the Provenance chain
// of prior bindings. Mutation goes through Archive.SetBranch, which
// builds and appends a new contribution/branch claim and updates
// B[name].
type Branch interface {
	// Name is the branch name (= the branch claim's content).
	Name() string
	// Latest is the current binding — exposed as a BranchEntry, so
	// the (head, time, contributor, claim) accessors are the same
	// shape as for prior entries. The current binding is not
	// duplicated in Provenance.
	Latest() BranchEntry
	// Provenance returns previously-bound entries, most-recent first,
	// by walking the contribution/branch edges back through prior
	// branch claims for this name. Latest is not included.
	//
	// "Provenance" rather than "history" — the prior bindings are
	// the chain this binding was derived from, in the paper's sense
	// of the term.
	Provenance() []BranchEntry
}

// BranchEntry is one entry in a branch's history — the head it was
// bound to, the time of that binding, and the contributor that made
// it. All fields are convenience projections of the underlying
// contribution/branch claim.
type BranchEntry interface {
	// Head is the id the branch was bound to at this entry.
	// Convenience over walking Claim()'s contribution/head edge.
	Head() Id
	// Time is the timestamp when this binding was made (UTC).
	// Convenience over Claim().Node().CreatedAt().
	Time() time.Time
	// Contributor returns the contributor of this branch claim.
	// Convenience over Claim().Contributor().
	Contributor() Contributor
	// Claim returns the underlying contribution/branch claim.
	Claim() Claim
}

// Graph is a Ranke-Graph instance RG ⊆ 𝒰 (spec §4.5), in memory.
// Built standalone via NewGraph for fresh contributions, or returned
// from Archive.GetGraph(head) for a hash-rooted instance.
type Graph interface {
	// Add inserts one or more claims into the graph atomically.
	// Every edge reference must already be reachable in the graph
	// at insertion time (atomic creation rule, §4.3). Non-root
	// claims must have at least one edge — the only no-edge claim a
	// graph may contain is its root, supplied at NewGraph. Adding
	// an already-present claim is idempotent. Returns the first
	// error encountered; claims added before the failing one stay
	// in the graph.
	Add(claims ...Claim) error

	// Contains reports whether the graph contains a claim with the
	// given id.
	//
	// Note: edges are addressable only through their parent claim's
	// Edges() method — exposing them by id would leak the existence
	// of pruned content. Content is reachable via ContentHash on
	// the records that name it; pruning visibility is enforced at
	// that level.
	Contains(id Id) bool

	// Get retrieves a claim from the graph by id.
	Get(id Id) (Claim, bool)

	// Heads returns the open heads of this graph: claims in the
	// graph that no other claim in the graph references (§4.5). A
	// single-headed graph (RG_h) returns one element; a multi-headed
	// graph returns several.
	Heads() []Id

	// IsConsolidated reports whether this graph has exactly one open
	// head — i.e. it is RG_h for some id h, addressable by a single id.
	IsConsolidated() bool

	// Consolidate builds a contribution/head claim that references
	// every open head of this graph, attributed to the given
	// contributor (§4.5), adds it to the graph, and returns it.
	// After this call the graph is single-headed at the new claim's
	// id.
	//
	// Returns an error if the graph is empty or already consolidated.
	// createdAt is optional. Zero / omitted → time.Now().UTC().
	// Non-zero → stamped onto the consolidation claim. Must satisfy
	// monotonicity (§4.3): >= the createdAt of every open head.
	Consolidate(contributor Contributor, createdAt ...time.Time) (Claim, error)

	// Validate recursively walks the closure from every open head
	// and runs the per-claim integrity + authenticity check (§5.10)
	// on each: edges resolve, Merkle hash matches, contributor's
	// signature verifies. Always walks the full closure (so verbose
	// callers see every claim) and returns the first error
	// encountered, or nil if all valid.
	//
	// If report callbacks are supplied, each is called once per
	// visited claim with its recursion depth and check result —
	// nil err = valid. Useful for tooling that wants to show a
	// tree-walk of the validation (the CLI's validate command,
	// scenario reload-and-verify).
	Validate(report ...func(c Claim, depth int, err error)) error

	// ValidateWithExceptions is Validate, but skips integrity checks
	// for claims whose ids appear in skip. Useful when a subset is
	// known-good (already validated upstream, loaded from a trusted
	// source) and the caller wants to avoid redundant work. All
	// other checks still run.
	ValidateWithExceptions(skip ...Id) error
}

