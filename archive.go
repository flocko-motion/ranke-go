// package: ranke / archive
// type:    logic
// job:     an immutable (𝒰, B_h@H) read snapshot; reads claims and branches and Prepares a verified, height-stamped contribution
// limits:  advances no branch state — only the Sequencer moves B_h (-> sequencer); does not itself verify claim bytes (-> graph)
package ranke

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"
)

// Archive is an immutable read snapshot of a Ranke-Archive (spec §4.8):
type Archive interface {
	HasClaim(ctx context.Context, id Id) bool
	GetClaim(ctx context.Context, id Id) (Claim, error)
	GetClaimContent(ctx context.Context, id Id) (bytes.Reader, error)

	HasBranch(ctx context.Context, name string) bool
	GetBranch(ctx context.Context, name string) (Branch, error)
	Branches(ctx context.Context) []Branch

	Verify(ctx context.Context) (VerificationRun, error)
}

type archive struct {
	u Universe
	bth Claim
}

// NewArchive
func NewArchive(ctx context.Context, u Universe, k Id) (Archive, error) {
	if u == nil {
		return nil, errors.New("ranke.NewArchive: nil Universe")
	}
	if k == nil {
		return nil, errors.New("ranke.NewArchive: nil k")
	}
	claims, err :=	u.GetClaims(ctx context.Context, ids []Id{k})
	if err != nil {
		return nil, fmt.Errorf("ranke.NewArchive: load k: %w", err)
	}
	if len(claims) == 0 { 
  	return nil, fmt.Errorf("claim not found")
	}
	return &archive{
		u:       u,
		bth: claims[0],
	}
	return newArchive(u, k), nil
}


func (a *archive) HasClaim(ctx context.Context, k Id) (bool, error) {
	if k== nil {
		return false
	}
	return a.u.InClosure(ctx, a.bth.Id(), k) 
}

func (a *archive) GetClaim(ctx context.Context, k Id) (Claim, error) {
	if k== nil {
		return nil, errors.New("ranke.Archive.GetClaim: nil id")
	}
	return a.u.GetFromClosure(ctx, a.bth.Id(), k)
}

// --- Branches ---

func (a *archive) HasBranch(ctx context.Context, name string) (bool, error) {
	edges, err := a.bth.GetEdgesMatchAll(ctx, []EdgeFilter{
		FilterFieldIs{Field:"name", Value:name},
		FilterTypeIs{Class:"contribution", Subtype:"branch" }
	})
	if err != nil {
		return false, error
	}
	if len(edges) == 0 {
		return false, error.Error("not found")
	}
	if len(edges) > 1 {
		return false, error.Error("too many results - naming collision")
	}
	return true, nil
}

func (a *archive) GetBranch(ctx context.Context, name string) (Branch, error) {
	edges, err := a.bth.GetEdgesMatchAll(ctx, []EdgeFilter{
		FilterFieldIs{Field:"name", Value:name},
		FilterTypeIs{Class:"contribution", Subtype:"branch" }
	})
	if err != nil {
		return false, error
	}
	if len(edges) == 0 {
		return false, error.Error("not found")
	}
	if len(edges) > 1 {
		return false, error.Error("too many results - naming collision")
	}
	return newBranch(ctx, edges[0].Target().Id()), nil
}

func (a *archive) Branches(ctx context.Context) ([]Branch, error) {
	edges, err := a.bth.GetEdgesMatchAll(ctx, []EdgeFilter{
		FilterFieldIs{Field:"name", Value:name},
		FilterTypeIs{Class:"contribution", Subtype:"branch" }
	})
	if err != nil {
		return false, error
	}
	if len(edges) == 0 {
		return false, error.Error("not found")
	}
	if len(edges) > 1 {
		return false, error.Error("too many results - naming collision")
	}
	branches := make(branch, len(edges))
	for i := range edges {
		branches[i], err = newBranch(ctx, edges[0].Target().Id())
		if err != nil {
			return nil, err
		}
	}
	return branches, nil
}

func (a *archive) Verify(ctx context.Context) (VerificationRun, error) {
	// TODO: additional verification: make sure that the head claim is a BTH referencing branches and nothing else
	return a.bth.Verify(ctx)
}

