// package: ranke / claim_type_branch
// type:    logic
// job:     the Branch extension of Claim — a contribution/branch claim viewed as a named, navigable, verifiable head
// limits:  the base Claim + concrete claim live in claim.go; mutation goes through the Sequencer (-> sequencer)
package ranke

import (
	"bytes"
	"context"
	"time"
)

// Branch is a convenience view over a contribution/branch claim: a Claim
// (all its methods, including Contributor()) plus branch-specific
// properties, provenance navigation, closure access, and verification.
type Branch interface {
	Claim

	// Properties
	Name() string
	Head() Id
	Time() time.Time

	// Navigate provenance — the previous revision of this branch, its
	// contribution/diff predecessor (nil when this is the first).
	Prev(ctx context.Context) (Branch, error)

	// Access the branch's closure
	HasClaim(ctx context.Context, id Id) bool
	GetClaim(ctx context.Context, id Id) (Claim, error)
	GetClaimContent(ctx context.Context, id Id) (bytes.Reader, error)

	// Verify from this head
	Verify(ctx context.Context) (VerificationRun, error)
}

// branch is a thin wrapper embedding the underlying claim, so every Claim
// method is promoted and the branch extension adds the rest.
type branch struct {
	*claim
}

func (b *branch) Name() string {
	name, _ := b.Node().GetField("name")
	return name
}

func (b *branch) Prev(ctx context.Context) (Branch, error) {
	return b.Edges(EdgeFilterFieldValue{
		Type: EdgeDiff,
	})
}
