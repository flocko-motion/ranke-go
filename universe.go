// package: ranke / universe
// type:    io
// job:     the 𝒰 interface — a content-addressed bag of claims and content with bulk get/put/has, copy, and single-item wrappers
// limits:  defines the contract only; concrete backends live under adapter/; does no validation (-> graph, content)
package ranke

import (
	"context"
	"io"
)

// ContentRef addresses one content blob for a read: its hash plus the
// expected size (backends use size to allocate / range-fetch / verify).
type ContentRef struct {
	Hash        Id
	ContentSize uint64
}

// ContentBlob is one content blob for a write: its hash and the bytes.
type ContentBlob struct {
	Hash    Id
	Content []byte
}

// Capabilities is an honest descriptor of what a Universe's backend can do
// beyond the base get/put/has contract, so composites (stack, partition) can
// derive their own and callers + config can reason about a deployment —
// repair, deletion, recovery, durability, query. The zero value is the most
// restrictive (everything false).
type Capabilities struct {
	// Overwrite: Put replaces an existing key's bytes rather than leaving them
	// untouched — lets a read-through repair restore a corrupted entry in
	// place. Add-only backends (WORM, immutable buckets) report false.
	Overwrite bool
	// Delete: a stored key can be removed — for lawful deletion (R6) and cache
	// eviction. Append-only backends report false.
	Delete bool
	// Enumerate: stored keys can be listed/iterated — needed to recover or
	// audit a Universe without a known head; impossible on a backend that
	// cannot enumerate (e.g. a plain GET/PUT/HEAD blob service).
	Enumerate bool
	// Persistent: stored data survives a process restart (a durable medium).
	// In-memory backends report false.
	Persistent bool
	// GQL: the backend offers a GQL (Cypher) query surface that a query
	// endpoint can push down to (e.g. a graph-native store like neo4j). This is
	// the optional graph-query language, NOT the native filtered query — that
	// is a separate, always-available read.
	GQL bool
}

// Universe is 𝒰 from spec §4.5 — a content-addressed bag of claims
// and the content bytes they reference. No notion of branches, no
// validation. Multiple Archives can share one Universe.
//
// Read/write/exists operations are bulk so backends can use their
// native batch path (S3 batch, Neo4j UNWIND, SQL bulk insert) — a
// 1M-item transfer via per-item round trips is unworkable on cloud
// backends. The package provides GetClaim/PutClaim/HasClaim and the
// content equivalents as single-item convenience wrappers over the
// bulk forms. StreamContent is the only inherently singular op.
type Universe interface {
	// GetClaims returns the claims for ids, positionally. A missing
	// claim fails the whole call with ErrNotFound; callers that
	// tolerate gaps should HasClaims first.
	//
	// Claims are diff-materialised by default (the contribution/diff
	// overlay is resolved, so GetField/Edges/content reflect the chain);
	// pass WithNotDiffMaterialized for the stored delta. A byte-store
	// backend may return raw claims and let the read path apply
	// DefaultMaterialize; a graph-native backend may materialise (and
	// cache) itself.
	GetClaims(ctx context.Context, ids []Id, opts ...GetOption) ([]Claim, error)
	// PutClaims stores cs. Idempotent (content-addressed): re-putting
	// an existing claim is a no-op.
	PutClaims(ctx context.Context, cs []Claim) error
	// HasClaims reports, positionally per id, whether each is present.
	HasClaims(ctx context.Context, ids []Id) ([]bool, error)

	// GetContents returns the bytes for each ref, positionally. A
	// missing blob fails the whole call with ErrNotFound.
	GetContents(ctx context.Context, refs []ContentRef) ([][]byte, error)
	// PutContents stores blobs. Idempotent.
	PutContents(ctx context.Context, blobs []ContentBlob) error
	// HasContents reports, positionally per hash, whether each is present.
	HasContents(ctx context.Context, hashes []Id) ([]bool, error)
	// StreamContent returns a reader for one blob — the lazy variant
	// of GetContents for large content. Inherently singular.
	StreamContent(ctx context.Context, hash Id, size uint64) (io.ReadCloser, error)

	// InClosure reports whether id is in the closure of any of heads — the
	// heads and every claim reachable from them by following edges. Multiple
	// heads answer a multi-headed graph's membership in one call. Closure
	// traversal is engine-dependent (a graph-native backend answers with one
	// query over all heads; a plain byte store walks the edges from every
	// head, sharing one visited set), so it lives on the Universe port —
	// exactly the kind of thing a backend optimises. Byte-store backends
	// delegate to DefaultInClosure.
	InClosure(ctx context.Context, heads []Id, id Id) (bool, error)
	// GetFromClosure returns the claim at id, but only if it is in the
	// closure of any of heads; ErrNotFound otherwise. Multi-headed like
	// InClosure. Engine-dependent — byte stores delegate to
	// DefaultGetFromClosure.
	GetFromClosure(ctx context.Context, heads []Id, id Id) (Claim, error)

	// CopyClaims copies the claim records at ids from src into the
	// receiver. Two orthogonal axes tune what comes along, both opt-in:
	//
	//   WithClosure() — also copy the full provenance closure of each
	//     id (every claim reachable from it). This is the "copy ===
	//     merge" semantics: a claim is only valid with its complete
	//     provenance, so this produces a mergeable state. Without it
	//     the copy is deliberately PARTIAL — valid at this layer
	//     (Universe does no validation) but not a complete graph; use
	//     it only for HasClaims-driven frontier sync where the caller
	//     guarantees the rest is present.
	//   WithContent() — also copy the content bytes the copied claims
	//     reference (claim records carry only the hash+size otherwise).
	//
	// ids are treated as roots under WithClosure, as the exact set
	// otherwise. Implementations should use their native fast path; a
	// set of heads is one ephemeral super-head away from being a single
	// closure, so a multi-root closure copy is not meaningfully heavier
	// than a single-root one — it's one walk over the union, with shared
	// provenance deduped. Trivial backends (mem, fs) can delegate to
	// DefaultCopyClaims. ctx cancellation is honored between items; an
	// interrupted copy may leave a partial result (no rollback).
	CopyClaims(ctx context.Context, src Universe, ids []Id, opts ...CopyOption) error
	// CopyContents copies the content blobs at refs from src into the
	// receiver — the content-only half of sync (for claims the receiver
	// already has). WithClosure/WithContent are no-ops here; only
	// WithProgress is honored.
	CopyContents(ctx context.Context, src Universe, refs []ContentRef, opts ...CopyOption) error

	// Capabilities reports optional backend abilities (see Capabilities).
	// Composites (stack, partition) derive theirs from their members'.
	Capabilities() Capabilities

	Close() error
}

// CopyOption tunes CopyClaims / CopyContents. Not every option is
// meaningful on every method — see each method's doc for which it honors.
type CopyOption func(*CopyConfig)

// CopyConfig is the resolved set of copy options. Universe implementations
// — including out-of-tree adapters — resolve their opts via NewCopyConfig
// and read these fields to decide what to copy and how to report progress.
type CopyConfig struct {
	Closure  bool
	Content  bool
	Progress func(CopyProgress)
}

// NewCopyConfig applies opts in order and returns the resolved config.
func NewCopyConfig(opts ...CopyOption) CopyConfig {
	var c CopyConfig
	for _, o := range opts {
		o(&c)
	}
	return c
}

// WithClosure copies each id's full provenance closure, not just the
// named records (CopyClaims only).
func WithClosure() CopyOption { return func(c *CopyConfig) { c.Closure = true } }

// WithContent also copies the content bytes the copied claims reference
// (CopyClaims only).
func WithContent() CopyOption { return func(c *CopyConfig) { c.Content = true } }

// WithProgress registers a best-effort progress callback. It is called
// on an unspecified goroutine, at a frequency entirely up to the
// implementation — which may be continuous, may fire only at start and
// completion (e.g. opaque native bulk ops), or may never call it. Keep
// the callback cheap: it runs on the copy's goroutine, so a slow
// callback throttles the copy.
func WithProgress(fn func(CopyProgress)) CopyOption {
	return func(c *CopyConfig) { c.Progress = fn }
}

// CopyProgress is a best-effort snapshot of an in-flight copy.
type CopyProgress struct {
	ClaimsCopied uint64 // claim records written so far
	BytesCopied  uint64 // content bytes written so far (only under WithContent)

	// Outstanding work known so far. While DiscoveryComplete is false
	// these are LOWER BOUNDS and may rise as the closure (and the
	// content it references) is walked; once it is true they only fall
	// to 0. With no closure walk the work set is known up front.
	ClaimsRemaining uint64
	BytesRemaining  uint64

	// DiscoveryComplete reports that the full work set — claims and the
	// content the receiver intends to fetch — has been enumerated.
	// Render an indeterminate indicator until this flips true, then a
	// real percentage. Note content is best-effort and at the
	// receiver's discretion (it may omit large blobs), so BytesRemaining
	// reflects what the receiver intends to fetch, not the absolute total.
	DiscoveryComplete bool
}

// GetClaim is the single-item form of Universe.GetClaims. Materialisation
// is the Universe's job (diff-materialised by default; pass
// WithNotDiffMaterialized for the stored delta), so this just delegates.
func GetClaim(ctx context.Context, u Universe, id Id, opts ...GetOption) (Claim, error) {
	cs, err := u.GetClaims(ctx, []Id{id}, opts...)
	if err != nil {
		return nil, err
	}
	return cs[0], nil
}

// PutClaim is the single-item form of Universe.PutClaims.
func PutClaim(ctx context.Context, u Universe, c Claim) error {
	return u.PutClaims(ctx, []Claim{c})
}

// HasClaim is the single-item form of Universe.HasClaims.
func HasClaim(ctx context.Context, u Universe, id Id) (bool, error) {
	got, err := u.HasClaims(ctx, []Id{id})
	if err != nil {
		return false, err
	}
	return got[0], nil
}

// GetContent is the single-item form of Universe.GetContents.
func GetContent(ctx context.Context, u Universe, hash Id, size uint64) ([]byte, error) {
	bs, err := u.GetContents(ctx, []ContentRef{{Hash: hash, ContentSize: size}})
	if err != nil {
		return nil, err
	}
	return bs[0], nil
}

// PutContent is the single-item form of Universe.PutContents.
func PutContent(ctx context.Context, u Universe, hash Id, content []byte) error {
	return u.PutContents(ctx, []ContentBlob{{Hash: hash, Content: content}})
}

// HasContent is the single-item form of Universe.HasContents.
func HasContent(ctx context.Context, u Universe, hash Id) (bool, error) {
	got, err := u.HasContents(ctx, []Id{hash})
	if err != nil {
		return false, err
	}
	return got[0], nil
}
