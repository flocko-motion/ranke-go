// package: ranke / claim_type_branch
// type:    logic
// job:     the Branch extension of Claim — a contribution/branch claim viewed as a named, navigable, verifiable head
// limits:  the base Claim + concrete claim live in claim.go; mutation goes through the Sequencer (-> sequencer)
package ranke

import (
	"context"
	"errors"
	"io"
	"time"
)

var errForeignClaim = errors.New("ranke: claim from a foreign implementation")

// Branch is a convenience view over a contribution/branch claim: a Claim
// (all its methods, including Contributor() and the §5.10 Verify) plus
// branch-specific properties, provenance navigation, closure access, and
// a long-running branch verification.
type Branch interface {
	Claim

	// Properties
	Name() string
	Head() Id
	Time() time.Time

	// Navigate provenance — the previous revision of this branch, its
	// contribution/diff predecessor (nil when this is the first).
	Prev(ctx context.Context) (Branch, error)

	// Access the branch's closure (rooted at this branch's head).
	HasClaim(ctx context.Context, id Id) (bool, error)
	GetClaim(ctx context.Context, id Id) (Claim, error)
	GetClaimContent(ctx context.Context, id Id) (io.Reader, error)

	// VerifyBranch runs a (possibly long-running) verification over this
	// branch's closure. Named distinctly from the embedded Claim.Verify
	// (the single-claim §5.10 check) since a Branch is a special Claim.
	VerifyBranch(ctx context.Context) (VerificationRun, error)
}

// branch is a thin wrapper embedding the underlying head claim, so every
// Claim method is promoted, plus the Universe the branch reads its closure
// from. The wrapped claim is the branch's head, so its own id is the
// closure root.
type branch struct {
	*claim
	u Universe
}

// newBranch loads the claim at id (the branch's head) and wraps it as a
// Branch scoped to u.
func newBranch(ctx context.Context, u Universe, id Id) (Branch, error) {
	c, err := GetClaim(ctx, u, id)
	if err != nil {
		return nil, err
	}
	cc, ok := c.(*claim)
	if !ok {
		return nil, errForeignClaim
	}
	return &branch{claim: cc, u: u}, nil
}

func (b *branch) Name() string {
	name, _ := b.Node().GetField("name")
	return name
}

// Head is the branch's head id — the branch wraps its head claim, so that
// is this claim's own id.
func (b *branch) Head() Id { return b.ID() }

func (b *branch) Time() time.Time { return b.Node().CreatedAt() }

// Prev returns the previous revision of this branch — the claim its
// contribution/diff edge references — or nil when this is the first.
func (b *branch) Prev(ctx context.Context) (Branch, error) {
	edges := b.Edges(EdgeFilterType{Type: EdgeTypeDiff})
	if len(edges) == 0 {
		return nil, nil
	}
	return newBranch(ctx, b.u, edges[0].Reference())
}

func (b *branch) HasClaim(ctx context.Context, id Id) (bool, error) {
	if id == nil {
		return false, errNilID
	}
	return b.u.InClosure(ctx, b.ID(), id)
}

func (b *branch) GetClaim(ctx context.Context, id Id) (Claim, error) {
	if id == nil {
		return nil, errNilID
	}
	return b.u.GetFromClosure(ctx, b.ID(), id)
}

func (b *branch) GetClaimContent(ctx context.Context, id Id) (io.Reader, error) {
	c, err := b.GetClaim(ctx, id)
	if err != nil {
		return nil, err
	}
	n := c.Node()
	if n.ContentHash() == nil {
		return nil, errNoContent
	}
	return b.u.StreamContent(ctx, n.ContentHash(), n.Size())
}

func (b *branch) VerifyBranch(_ context.Context) (VerificationRun, error) {
	// TODO: run verification over this branch's closure and return a
	// progress handle. Pending the verification design (see VerificationRun).
	return nil, errVerifyTODO
}
