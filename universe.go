// package: ranke / universe
// type:    io
// job:     the 𝒰 interface — a content-addressed bag of claims and content with bulk get/put/has, copy, and single-item wrappers
// limits:  defines the contract only; concrete backends live under adapter/; does no validation (-> graph, content)
package ranke

import (
	"bytes"
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
	// ReverseWalk: the backend can follow edges backward NATIVELY/efficiently
	// (e.g. neo4j). Any Universe can answer uses/connections via DefaultQuery's
	// closure-inversion; this flag signals a cheap native path, not mere ability.
	ReverseWalk bool
	// RawClaims: the backend stores claims as CBOR
	RawClaims bool
	// ExternalContent: the backend holds externalized content of ANY size —
	ExternalContent bool
	// ContentCap is the largest .content value (bytes) this backend may hold;
	// 0 = no limit
	ContentCap uint64
	// Tags: holds mutable, pure-functional per-claim tags (branch membership
	// etc.) and implements Tagger; opaque byte stores report false.
	Tags bool
	// Tier is the write-durability role this layer is configured in — how a
	// stack writes to it. The deployment picks it (an adapter option) within what
	// the adapter allows; the stack composes its write path from each layer's
	// reported tier.
	Tier StorageTier
}

// StorageTier is how a stack writes to a layer. The adapter constrains which
// tiers it allows (from its nature — authoritative needs RawClaims &&
// ExternalContent), the deployment picks one via an adapter option, and the
// layer reports the choice as Capabilities.Tier.
type StorageTier string

const (
	// StorageTierAuthoritative: the source of truth — a write MUST succeed here
	// (else the whole write fails); holds the archive verbatim. Requires
	// RawClaims && ExternalContent; a stack needs at least one.
	StorageTierAuthoritative StorageTier = "authoritative"
	// StorageTierEager: written synchronously, in parallel with the authoritative
	// tier(s), best-effort — a failure does not fail the write; the layer
	// re-syncs from the authoritative copy. The write-through queryable layer (neo4j).
	StorageTierEager StorageTier = "eager"
	// StorageTierBackground: written in a background goroutine the write does not
	// wait for — populated eventually, best-effort.
	StorageTierBackground StorageTier = "background"
	// StorageTierLazy: not written on a write; populated on a read miss served
	// from below (a read-through cache, e.g. redis).
	StorageTierLazy StorageTier = "lazy"
)

// AllowsTier reports whether a layer with these capabilities may be configured
// in tier t — the "adapters inform" half of the write model: the adapter's
// nature constrains the choice, the deployment picks within it, the stack reads
// the result. Authoritative is the archive's source of truth, so it demands
// verbatim, unbounded storage (RawClaims && ExternalContent); a lossy
// projection (neo4j) or a capped cache cannot hold it. Every other tier is a
// pure routing role any layer can fill.
func (c Capabilities) AllowsTier(t StorageTier) bool {
	switch t {
	case StorageTierAuthoritative:
		return c.RawClaims && c.ExternalContent
	case StorageTierEager, StorageTierBackground, StorageTierLazy:
		return true
	default:
		return false
	}
}

// SyncResult is the outcome of a Sync: SyncedTo is the branch-table claim now
// fully readable (nil on error), Err any failure.
type SyncResult struct {
	SyncedTo Id
	Err      error
}

// SyncedNow returns a closed channel with an immediate success — the trivial
// Sync for a layer that holds the whole archive (nothing to assure).
func SyncedNow(id Id) <-chan SyncResult {
	ch := make(chan SyncResult, 1)
	ch <- SyncResult{SyncedTo: id}
	close(ch)
	return ch
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
	// GetClaimsRaw returns each claim's stored canonical CBOR verbatim (never a
	// re-encode), positionally; a missing claim fails with ErrNotFound. The
	// verify/replicate counterpart of GetClaims — verification hashes these
	// bytes, replication copies them. Byte stores return the blob; a
	// structure-only cache (neo4j) holds no CBOR (Capabilities.RawClaims false)
	// and misses, so a stack routes to the byte layer below.
	GetClaimsRaw(ctx context.Context, ids []Id) ([][]byte, error)

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

	// GetClaimHeights returns the committed heights (§4.1) of the claims at
	// ids, positionally. Bulk like the other reads so a backend can serve
	// many from one cache sweep or one batched query. Height is the hot field
	// for caching and scoping: computing it from scratch over a large closure
	// is expensive, so it is fixed at creation and read back cheaply here. A
	// byte-store backend delegates to DefaultGetClaimHeights (load + read the
	// field); a backend that maintains an id→height cache (HeightCache) answers
	// from it, deserialising only the misses.
	GetClaimHeights(ctx context.Context, ids []Id) ([]uint64, error)

	// Query answers a declarative RQL read (the paper's §Filtered Reads) — the
	// primary read endpoint. scope is an injected visibility predicate (nil =
	// unrestricted): mechanism applies it, policy supplies it. A byte store
	// delegates to DefaultQuery (the reference closure walk, which serves
	// uses/connections by inverting the closure — correct but O(closure)); a
	// graph-native backend overrides with a native lowering (e.g. Cypher). A
	// query's meaning is unchanged by which layer answers it.
	Query(ctx context.Context, q Query, scope Scope) (ResultStream, error)

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

	// SetClaimsTags applies tags per claim, keyed by claim-id string: for each
	// claim it first clears every existing tag whose key matches a clearTags
	// glob (globs allowed), then applies that claim's key→value pairs. A backend
	// that can't hold tags (an opaque byte store) returns ErrUnsupported.
	SetClaimsTags(ctx context.Context, clearTags []string, tags map[string]map[string]string) error
	// GetClaimTags returns each claim's tags positionally (nil when a claim has
	// none), like GetClaims/GetClaimHeights.
	GetClaimTags(ctx context.Context, claims []Id) ([]map[string]string, error)

	// Sync fills the receiver for id's closure by copying whatever it is missing
	// from src (claims + content). A stack calls it on its eager layer, passing
	// the layers below as src — the stack only routes; the layer does the copy.
	// A nil src means nothing to copy from, so the receiver is taken as synced.
	Sync(ctx context.Context, src Universe, id Id) <-chan SyncResult

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

// GetClaimHeight is the single-item form of Universe.GetClaimHeights.
func GetClaimHeight(ctx context.Context, u Universe, id Id) (uint64, error) {
	hs, err := u.GetClaimHeights(ctx, []Id{id})
	if err != nil {
		return 0, err
	}
	return hs[0], nil
}

// ContentKind states whether and where a claim's content lives — a node's own,
// definitive answer (Claim.GetContent routes on it):
//
//	Inline   — held in the claim record
//	External — a separate Universe blob, addressed by content hash
//	None     — the claim carries no content
type ContentKind int

const (
	ContentNone     ContentKind = iota // no content
	ContentInline                      // inline in the claim record
	ContentExternal                    // a separate Universe blob
)

// claimInlineReader recovers inline bytes a structure-only cache dropped: it
// reads the raw claim via GetClaimsRaw (which falls through the cache to the byte
// layer, unlike GetClaim) and decodes the content. Callers pass the content
// source's id, so the decoded claim carries the bytes directly.
func claimInlineReader(ctx context.Context, u Universe, id Id) (io.ReadCloser, error) {
	raws, err := u.GetClaimsRaw(ctx, []Id{id})
	if err != nil {
		return nil, err
	}
	c, err := DecodeClaim(id, raws[0])
	if err != nil {
		return nil, err
	}
	b, err := c.Node().GetInlineContent()
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// GetTag is a single-item form of Universe.GetClaimTags: it reports whether
// tag is set on the claim at id, and its value.
func GetTag(ctx context.Context, u Universe, id Id, tag string) (found bool, value string, err error) {
	tags, err := u.GetClaimTags(ctx, []Id{id})
	if err != nil {
		return false, "", err
	}
	value, found = tags[0][tag]
	return found, value, nil
}

// PutContent is the single-item form of Universe.PutContents.
func PutContent(ctx context.Context, u Universe, id Id, content []byte) error {
	return u.PutContents(ctx, []ContentBlob{{Hash: id, Content: content}})
}

// HasContent is the single-item form of Universe.HasContents.
func HasContent(ctx context.Context, u Universe, id Id) (bool, error) {
	got, err := u.HasContents(ctx, []Id{id})
	if err != nil {
		return false, err
	}
	return got[0], nil
}
