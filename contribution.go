// package: ranke / contribution
// type:    logic
// job:     the contribution contract — the staged → verified → mergable advance of a Ranke-Archive
// (RA_k → RA_k')
// limits:  the contract plus the shared wire drain; opening, verifying and merging a contribution
// are a Sequencer's (-> adapter/sequencer/dev, adapter/sequencer/concurrent)
package ranke

import (
	"context"
	"time"
)

// Contribution is an in-progress advance of a Ranke-Archive: open it against a
// base RA_k, fill it, then CompleteAndVerify seals and verifies it.
type Contribution interface {
	// Base is the (k, t) the contribution was opened against.
	Base() (head Id, t time.Time)
	// AddGraph / AddClaims fill the contribution (step 2), naming the branch the
	// claims join. Several may be named, and an empty one is an error.
	AddGraph(branch string, g Graph) error
	AddClaims(branch string, claims []Claim) error
	// AddWire fills from a WireMediaType stream, which declares its own branches.
	// It takes the reader, so a caller that checked Branches hands on the same one.
	AddWire(ctx context.Context, wr *WireReader) error
	// CompleteAndVerify closes the contribution over its base and verifies it
	// (steps 3–4), yielding a sealed VerifiedContribution.
	CompleteAndVerify(ctx context.Context) (VerifiedContribution, error)
}

// DrainWire fills c as records arrive: content lands in u under its hash, each
// claim stages under its branch, and an undeclared branch stops the fill.
func DrainWire(ctx context.Context, c Contribution, u Universe, wr *WireReader) error {
	for wr.Next() {
		switch rec := wr.Record(); rec.Kind {
		case WireContent:
			if err := u.PutContents(ctx, []ContentBlob{rec.Blob}); err != nil {
				return err
			}
		case WireClaim:
			if err := c.AddClaims(rec.Branch, []Claim{rec.Claim}); err != nil {
				return err
			}
		}
	}
	return wr.Err()
}

// VerifiedContribution is a sealed, verified contribution: its contents
// are fixed, so by immutability whatever verified stays valid.
type VerifiedContribution interface {
	// Ids are the claim ids the contribution adds.
	Ids() []Id
	// Persist writes the sealed closure to 𝒰 (step 5), yielding a
	// MergableContribution the Sequencer can merge.
	Persist(ctx context.Context) (MergableContribution, error)
}

// MergableContribution is ready for step 6: its claims are durably in 𝒰 and its
// head claim(s) are known.
type MergableContribution interface {
	// Heads are the open head ids per branch, which the new branch-table claim
	// references under those names.
	Heads() map[string][]Id
}
