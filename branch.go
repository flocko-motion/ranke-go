// package: ranke / branch
// type:    data
// job:     read-only views over one (name, head) binding and its history, projected from a contribution/branches claim
// limits:  does not mutate branches (-> archive); does not load the table (-> branch_table)
package ranke

import (
	"bytes"
	"context"
	"time"
)

// Branch is a convenience view over a contribution/branch claim.
type Branch interface {
	// Properties
	Name() string
	Head() Id
	Time() time.Time
	Claim() Claim
  // Navigate provenance 
	Prev() (ctx context.Context, Branch, error)
	Contributor(ctx ctx.Context) Contributor
  // Access closure 
	HasClaim(ctx context.Context, id Id) bool
	GetClaim(ctx context.Context, id Id) (Claim, error)
	GetClaimContent(ctx context.Context, id Id) (bytes.Reader, error)
  // Verify from here
	Verify(ctx context.Context) (VerificationRun, error)
}

type branch struct {
	claim *claim
}

func (b *branch) Claim() Claim { return b.claim }

func (b *branch) Name() string {
	name, _ := b.Claim().Node().GetField("name")
	return name
}

func (b *branch) Prev() (Branch, error) {
	return b.Claim().GetEdge("contribution/diff")
}
