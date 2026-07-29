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

// ContentRef addresses one content blob for a read: its hash plus the expected
// size, which backends use to allocate, range-fetch or verify.
type ContentRef struct {
	Hash        Id
	ContentSize uint64
}

// ContentBlob is one content blob for a write: its hash and the bytes.
type ContentBlob struct {
	Hash    Id
	Content []byte
}

// Capabilities describes what a Universe's backend can do beyond the base
// get/put/has contract, so composites and config can reason about a deployment.
// The zero value is the most restrictive.
type Capabilities struct {
	// Overwrite: Put replaces an existing key's bytes, so a read-through repair
	// restores a corrupted entry in place. WORM buckets report false.
	Overwrite bool
	// Delete: a stored key can be removed, for lawful deletion (R6) and eviction.
	Delete bool
	// Enumerate: stored keys can be listed, so a Universe can be recovered or
	// audited without a known head.
	Enumerate bool
	// Persistent: stored data survives a process restart.
	Persistent bool
	// ReverseWalk: the backend follows edges backward natively and cheaply (neo4j
	// indexes both directions). A stack ORs it over layers; a partition ANDs.
	ReverseWalk bool
	// RawClaims: the backend stores claims as CBOR
	RawClaims bool
	// ExternalContent: the backend holds externalized content of ANY size —
	ExternalContent bool
	// ContentCap is the largest .content value in bytes this backend holds; 0 = no limit
	ContentCap uint64
	// Tags: holds mutable per-claim tags (branch membership) and implements Tagger.
	Tags bool
	// Tier is the write-durability role this layer is configured in; the stack
	// composes its write path from each layer's reported tier.
	Tier StorageTier
}

// StorageTier is how a stack writes to a layer: the adapter constrains which
// tiers it allows, the deployment picks one, the layer reports the choice.
type StorageTier string

const (
	// StorageTierAuthoritative: the source of truth, holding the archive verbatim —
	// a write MUST succeed here. Requires RawClaims && ExternalContent.
	StorageTierAuthoritative StorageTier = "authoritative"
	// StorageTierEager: written synchronously alongside the authoritative tier,
	// best-effort — the layer re-syncs from the authoritative copy (neo4j).
	StorageTierEager StorageTier = "eager"
	// StorageTierBackground: written in a background goroutine, best-effort.
	StorageTierBackground StorageTier = "background"
	// StorageTierLazy: populated on a read miss served from below (redis).
	StorageTierLazy StorageTier = "lazy"
)

// AllowsTier reports whether a layer with these capabilities may be configured in
// tier t: authoritative demands verbatim, unbounded storage (RawClaims &&
// ExternalContent), while every other tier is a routing role any layer can fill.
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

// SyncResult is a Sync outcome: SyncedTo is the branch-table claim now fully
// readable, nil on error, and Err the failure.
type SyncResult struct {
	SyncedTo Id
	Err      error
}

// SyncedNow returns a closed channel with an immediate success.
func SyncedNow(id Id) <-chan SyncResult {
	ch := make(chan SyncResult, 1)
	ch <- SyncResult{SyncedTo: id}
	close(ch)
	return ch
}

// Universe is 𝒰 from spec §4.5 — a content-addressed bag of claims and the
// content bytes they reference, shareable by many Archives. Operations are bulk so
// backends use their native batch path (S3 batch, Neo4j UNWIND, SQL bulk insert).
type Universe interface {
	// GetClaims returns the claims for ids, positionally; a missing claim fails
	// the whole call with ErrNotFound, so callers tolerating gaps HasClaims first.
	// Claims are diff-materialised unless WithNotDiffMaterialized asks for the delta.
	GetClaims(ctx context.Context, ids []Id, opts ...GetOption) ([]Claim, error)
	// PutClaims stores cs; idempotent, as claims are content-addressed.
	PutClaims(ctx context.Context, cs []Claim) error
	// HasClaims reports, positionally per id, whether each is present.
	HasClaims(ctx context.Context, ids []Id) ([]bool, error)
	// GetClaimsRaw returns each claim's stored canonical CBOR verbatim,
	// positionally — verification hashes these bytes, replication copies them. A
	// structure-only cache (RawClaims false) misses, so a stack routes below it.
	GetClaimsRaw(ctx context.Context, ids []Id) ([][]byte, error)

	// GetContents returns the bytes for each ref, positionally; a missing blob
	// fails the whole call with ErrNotFound.
	GetContents(ctx context.Context, refs []ContentRef) ([][]byte, error)
	// PutContents stores blobs. Idempotent.
	PutContents(ctx context.Context, blobs []ContentBlob) error
	// HasContents reports, positionally per hash, whether each is present.
	HasContents(ctx context.Context, hashes []Id) ([]bool, error)
	// StreamContent returns a reader for one blob — the lazy, singular GetContents.
	StreamContent(ctx context.Context, hash Id, size uint64) (io.ReadCloser, error)

	// GetClaimHeights returns the committed heights (§4.1) of the claims at ids,
	// positionally. Height is fixed at creation and read back cheaply here, since
	// recomputing it over a large closure is expensive.
	GetClaimHeights(ctx context.Context, ids []Id) ([]uint64, error)

	// Query answers a declarative RQL read (the paper's §Filtered Reads) with
	// scope as the resolved branch context, so head/height/revision are known. A
	// byte store delegates to DefaultQuery, a graph-native backend lowers to its
	// own query language; a query means the same whichever layer answers.
	Query(ctx context.Context, q Query, scope Scope) (ResultStream, error)

	// CopyClaims copies the claim records at ids from src into the receiver —
	// roots under WithClosure, the exact set otherwise. WithClosure brings each
	// id's full provenance, the "copy === merge" semantics yielding a mergeable
	// state; bare, the copy is PARTIAL, for HasClaims-driven frontier sync where
	// the caller guarantees the rest. WithContent adds the referenced content
	// bytes. An interrupted copy leaves a partial result.
	CopyClaims(ctx context.Context, src Universe, ids []Id, opts ...CopyOption) error
	// CopyContents copies the content blobs at refs from src into the receiver,
	// the content-only half of sync. Only WithProgress is honored.
	CopyContents(ctx context.Context, src Universe, refs []ContentRef, opts ...CopyOption) error

	// SetClaimsTags applies tags per claim, keyed by claim-id string: it clears
	// every existing tag matching a clearTags glob, then applies that claim's
	// pairs. A backend that cannot hold tags returns ErrUnsupported.
	SetClaimsTags(ctx context.Context, clearTags []string, tags map[string]map[string]string) error
	// GetClaimTags returns each claim's tags positionally, nil when it has none.
	GetClaimTags(ctx context.Context, claims []Id) ([]map[string]string, error)

	// Tag signals that the archive at head has advanced. Purely an accelerator: it
	// changes how fast a read answers, and what a layer indexes is its business.
	Tag(ctx context.Context, head Id) error

	// Sync fills the receiver for id's closure by copying what it lacks from src
	// (claims + content); a stack calls it on its eager layer with the layers below
	// as src. A nil src leaves the receiver synced.
	Sync(ctx context.Context, src Universe, id Id) <-chan SyncResult

	// Capabilities reports optional backend abilities; composites derive theirs.
	Capabilities() Capabilities

	Close() error
}

// CopyOption tunes CopyClaims / CopyContents — see each method for what it honors.
type CopyOption func(*CopyConfig)

// CopyConfig is the resolved set of copy options, which Universe implementations
// obtain via NewCopyConfig.
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

// WithClosure copies each id's full provenance closure (CopyClaims only).
func WithClosure() CopyOption { return func(c *CopyConfig) { c.Closure = true } }

// WithContent also copies the content bytes the copied claims reference.
func WithContent() CopyOption { return func(c *CopyConfig) { c.Content = true } }

// WithProgress registers a best-effort progress callback, called on an
// unspecified goroutine at an implementation-chosen frequency. Keep it cheap: it
// runs on the copy's goroutine.
func WithProgress(fn func(CopyProgress)) CopyOption {
	return func(c *CopyConfig) { c.Progress = fn }
}

// CopyProgress is a best-effort snapshot of an in-flight copy.
type CopyProgress struct {
	ClaimsCopied uint64 // claim records written so far
	BytesCopied  uint64 // content bytes written so far (only under WithContent)

	// Outstanding work known so far: LOWER BOUNDS that may rise as the closure is
	// walked until DiscoveryComplete, after which they only fall to 0.
	ClaimsRemaining uint64
	BytesRemaining  uint64

	// DiscoveryComplete reports the full work set enumerated: render an
	// indeterminate indicator until it flips, then a real percentage.
	// BytesRemaining counts what the receiver intends to fetch, at its discretion.
	DiscoveryComplete bool
}

// GetClaim is the single-item form of Universe.GetClaims; materialisation is the
// Universe's job, so this delegates.
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

// ContentKind states whether and where a claim's content lives — the node's own
// definitive answer, which Claim.GetContent routes on.
type ContentKind int

const (
	ContentNone     ContentKind = iota // no content
	ContentInline                      // inline in the claim record
	ContentExternal                    // a separate Universe blob
)

// claimInlineReader recovers inline bytes a structure-only cache dropped: it reads
// the raw claim via GetClaimsRaw, which falls through to the byte layer, and
// decodes the content at the caller's id.
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

// GetTag reports whether tag is set on the claim at id, and its value.
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
