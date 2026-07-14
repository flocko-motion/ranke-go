// package: ranke / archive
// type:    logic
// job:     an immutable Ranke-Archive snapshot RA_k = (𝒰, k); reads claims and branches through the head claim, delegating closure ops to the Universe
// limits:  advances nothing — writes go through the Sequencer (-> sequencer); closure traversal belongs to the Universe (-> universe)
package ranke

import (
	"context"
	"errors"
	"fmt"
	"io"
)

var (
	errNilUniverse     = errors.New("ranke.NewArchive: nil Universe")
	errNilHeadID       = errors.New("ranke.NewArchive: nil head id")
	errHeadNotFound    = errors.New("ranke.NewArchive: head claim not found")
	errNilID           = errors.New("ranke.Archive: nil id")
	errNoContent       = errors.New("ranke.Archive.GetClaimContent: claim carries no content")
	errBranchNotFound  = errors.New("ranke.Archive.GetBranch: branch not found")
	errBranchCollision = errors.New("ranke.Archive.GetBranch: name collision — multiple branches match")
	errVerifyTODO      = errors.New("ranke: verification not implemented")
)

// Archive is an immutable read snapshot of a Ranke-Archive: the tuple
// RA_k = (𝒰, k), held as the Universe plus the head claim 𝒰(k). Reads
// resolve against the head's closure — membership and lookup are
// delegated to the Universe (engine-dependent). It advances nothing; a
// contribution is the Sequencer's job.
type Archive interface {
	HasClaim(ctx context.Context, id Id) (bool, error)
	GetClaim(ctx context.Context, id Id) (Claim, error)
	GetClaimContent(ctx context.Context, id Id) (io.Reader, error)

	HasBranch(ctx context.Context, name string) (bool, error)
	GetBranch(ctx context.Context, name string) (Branch, error)
	GetBranches(ctx context.Context) ([]Branch, error)

	Verify(ctx context.Context) (VerificationRun, error)
}

type archive struct {
	u   Universe
	bth Claim // the head claim 𝒰(k) — a contribution/branches claim
}

// NewArchive opens the immutable snapshot RA_k = (𝒰, k) by loading the
// head claim at k from u.
func NewArchive(ctx context.Context, u Universe, k Id) (Archive, error) {
	if u == nil {
		return nil, errNilUniverse
	}
	if k == nil {
		return nil, errNilHeadID
	}
	claims, err := u.GetClaims(ctx, []Id{k})
	if err != nil {
		return nil, fmt.Errorf("ranke.NewArchive: load head %s: %w", k.String(), err)
	}
	if len(claims) == 0 {
		return nil, errHeadNotFound
	}
	return &archive{u: u, bth: claims[0]}, nil
}

func (a *archive) HasClaim(ctx context.Context, id Id) (bool, error) {
	if id == nil {
		return false, errNilID
	}
	return a.u.InClosure(ctx, a.bth.ID(), id)
}

func (a *archive) GetClaim(ctx context.Context, id Id) (Claim, error) {
	if id == nil {
		return nil, errNilID
	}
	return a.u.GetFromClosure(ctx, a.bth.ID(), id)
}

func (a *archive) GetClaimContent(ctx context.Context, id Id) (io.Reader, error) {
	c, err := a.GetClaim(ctx, id)
	if err != nil {
		return nil, err
	}
	n := c.Node()
	if n.ContentHash() == nil {
		return nil, errNoContent
	}
	return a.u.StreamContent(ctx, n.ContentHash(), n.Size())
}

// --- Branches ---

// branchEdges returns the head claim's contribution/branch edges, filtered
// by name when one is given (empty name = all branches).
func (a *archive) branchEdges(name string) []Edge {
	filters := []Filter{EdgeFilterType{Type: EdgeTypeBranch}}
	if name != "" {
		filters = append(filters, EdgeFilterFieldValue{Field: "name", Value: name})
	}
	return a.bth.Edges(filters...)
}

func (a *archive) HasBranch(_ context.Context, name string) (bool, error) {
	return len(a.branchEdges(name)) > 0, nil
}

func (a *archive) GetBranch(ctx context.Context, name string) (Branch, error) {
	edges := a.branchEdges(name)
	switch {
	case len(edges) == 0:
		return nil, errBranchNotFound
	case len(edges) > 1:
		return nil, errBranchCollision
	}
	return newBranch(ctx, a.u, edges[0].Reference())
}

func (a *archive) GetBranches(ctx context.Context) ([]Branch, error) {
	edges := a.branchEdges("")
	out := make([]Branch, 0, len(edges))
	for _, e := range edges {
		b, err := newBranch(ctx, a.u, e.Reference())
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

func (a *archive) Verify(_ context.Context) (VerificationRun, error) {
	// TODO: run verification over the head's closure and return a progress
	// handle. Pending the verification design (see VerificationRun).
	return nil, errVerifyTODO
}
