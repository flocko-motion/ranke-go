package ranke

import (
	"context"
	"errors"
	"io"
)

// Universe is 𝒰 from spec §4.5 — a content-addressed bag of claims
// and the content bytes they reference. No notion of branches, no
// validation. Multiple Archives can share one Universe.
type Universe interface {
	LoadClaim(ctx context.Context, id Id) (Claim, error)
	SaveClaim(ctx context.Context, c Claim) error
	HasClaim(ctx context.Context, id Id) (bool, error)

	GetContent(ctx context.Context, hash Id, size uint64) ([]byte, error)
	StreamContent(ctx context.Context, hash Id, size uint64) (io.ReadCloser, error)
	SaveContent(ctx context.Context, hash Id, content []byte) error
	HasContent(ctx context.Context, hash Id) (bool, error)

	// MergeClaim pulls the claim at id from src into the receiver,
	// along with its provenance (every claim reachable from id).
	// In Ranke-Graph a claim is inseparable from its provenance, so
	// "merge claim" means "merge claim + closure". Implementations
	// should use their native fast path (S3 batch copy, Neo4j graph
	// dump, SQL bulk insert) — a 1M-claim merge via per-claim round
	// trips is unworkable on cloud backends. Trivial backends (mem,
	// fs) can delegate to DefaultMergeClaim.
	MergeClaim(ctx context.Context, src Universe, id Id) error

	Close() error
}

var (
	ErrNotFound  = errors.New("ranke: not found")
	ErrIntegrity = errors.New("ranke: integrity check failed")
	ErrClosed    = errors.New("ranke: closed")
)
