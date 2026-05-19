package ranke

import (
	"context"
	"errors"
	"io"
)

// Universe is 𝒰 from spec §4.5 — a content-addressed bag of claims
// and the content bytes they reference. No notion of branches, no
// validation, no closure walks. Multiple Archives can share one
// Universe.
type Universe interface {
	LoadClaim(ctx context.Context, id Id) (Claim, error)
	SaveClaim(ctx context.Context, c Claim) error
	HasClaim(ctx context.Context, id Id) (bool, error)

	GetContent(ctx context.Context, hash Id, size uint64) ([]byte, error)
	StreamContent(ctx context.Context, hash Id, size uint64) (io.ReadCloser, error)
	SaveContent(ctx context.Context, hash Id, content []byte) error
	HasContent(ctx context.Context, hash Id) (bool, error)

	Close() error
}

var (
	ErrNotFound  = errors.New("ranke: not found")
	ErrIntegrity = errors.New("ranke: integrity check failed")
	ErrClosed    = errors.New("ranke: closed")
)
