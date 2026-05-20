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

	// MergeClosure pulls every claim and content blob reachable
	// from head in src into the receiver. Implementations should
	// use their native fast path (S3 batch copy, Neo4j graph dump,
	// SQL bulk insert) — a 1M-claim merge against a cloud backend
	// via per-claim round trips is unworkable. Trivial backends
	// (mem, fs) can delegate to DefaultMergeClosure.
	MergeClosure(ctx context.Context, src Universe, head Id) error

	Close() error
}

var (
	ErrNotFound  = errors.New("ranke: not found")
	ErrIntegrity = errors.New("ranke: integrity check failed")
	ErrClosed    = errors.New("ranke: closed")
)
