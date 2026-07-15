// package: ranke / claim_type_branch
// type:    logic
// job:     the Branch view over a contribution/branch edge — a named pointer into an archive's branch table
// limits:  the branch-table logic (materialising the diff chain) lives in the Archive (-> archive); a Branch only navigates the subgraph its edge references
package ranke

import (
	"context"
	"io"
)

// Branch is one entry of an archive's branch table: a named
// contribution/branch edge pointing at the root of that branch's
// subgraph. A Branch *is* an Edge — the named edge is the branch — with
// navigation into the referenced subgraph on top. Obtain branches from an
// Archive; nobody mints them directly.
type Branch interface {
	Edge

	// Name is the branch name — the edge's "name" field.
	Name() string
	// Head is the root of the branch's subgraph — the edge's Reference().
	Head() Id

	// Prev is the previous revision of this branch: the branch pointing at
	// the current head's contribution/diff predecessor, or nil when the
	// head has no predecessor (first revision).
	Prev(ctx context.Context) (Branch, error)

	// Subgraph access — membership and lookup within the branch's closure,
	// resolved against the Universe the branch was read from.
	HasClaim(ctx context.Context, id Id) (bool, error)
	GetClaim(ctx context.Context, id Id) (Claim, error)
	GetClaimContent(ctx context.Context, id Id) (io.Reader, error)

	// Verify runs a (possibly long-running) verification over the branch's
	// closure. See verify.go for options.
	Verify(ctx context.Context, opts ...VerifyOption) (VerificationRun, error)
}

// branch wraps the named contribution/branch edge with the Universe its
// subgraph is read from. Embedding Edge promotes Reference/Type/GetField/
// ID/… so the branch is an Edge with subgraph navigation added.
type branch struct {
	Edge
	u Universe
}

// newBranch wraps a contribution/branch edge as a Branch scoped to u.
func newBranch(u Universe, e Edge) Branch {
	return &branch{Edge: e, u: u}
}

func (b *branch) Name() string {
	name, _ := b.GetField(FieldName)
	return name
}

// Head is the branch's subgraph root — the edge's reference.
func (b *branch) Head() Id { return b.Reference() }

func (b *branch) HasClaim(ctx context.Context, id Id) (bool, error) {
	if id == nil {
		return false, errNilID
	}
	return InClosure(ctx, b.u, b.Name(), []Id{b.Reference()}, id)
}

func (b *branch) GetClaim(ctx context.Context, id Id) (Claim, error) {
	if id == nil {
		return nil, errNilID
	}
	return GetFromClosure(ctx, b.u, b.Name(), []Id{b.Reference()}, id)
}

func (b *branch) GetClaimContent(ctx context.Context, id Id) (io.Reader, error) {
	c, err := b.GetClaim(ctx, id)
	if err != nil {
		return nil, err
	}
	return c.Node().GetContent(ctx, b.u)
}

// Prev returns the previous revision of this branch: a branch, same name,
// pointing at the current head's contribution/diff predecessor. Returns
// nil when the head has no predecessor (first revision).
func (b *branch) Prev(ctx context.Context) (Branch, error) {
	head, err := GetClaim(ctx, b.u, b.Reference())
	if err != nil {
		return nil, err
	}
	diff := head.Edges(EdgeFilterType{Type: EdgeTypeDiff})
	if len(diff) == 0 {
		return nil, nil
	}
	prevEdge, err := NewEdge(EdgeConfig{
		Reference: diff[0].Reference(),
		TypeClass: EdgeClassContribution,
		TypeSub:   string(EdgeSubtypeBranch),
		Fields:    map[string]string{FieldName: b.Name()},
	})
	if err != nil {
		return nil, err
	}
	return newBranch(b.u, prevEdge), nil
}

// Verify lives in verify.go.
