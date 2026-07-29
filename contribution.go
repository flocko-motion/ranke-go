// package: ranke / contribution
// type:    logic
// job:     the contribution contract — the staged → verified → mergable advance of a Ranke-Archive (RA_k → RA_k')
// limits:  interfaces only; the concrete implementation is a write-path mechanism (-> adapter/sequencer_mechanism)
package ranke

import (
	"context"
	"time"
)

// Contribution is an in-progress advance of a Ranke-Archive — the paper's
// six steps, up to sealing. Open it against a base RA_k, fill it with
// claims, then CompleteAndVerify seals and verifies it.
type Contribution interface {
	// Base is the (k, t) the contribution was opened against.
	Base() (head Id, t time.Time)
	// AddGraph / AddClaims fill the contribution (step 2, Filling), naming the
	// branch the claims join. One contribution may name several, so a bulk
	// contribution advances them all in one merge. An empty branch is an error,
	// and a sealed contribution refuses both.
	AddGraph(branch string, g Graph) error
	AddClaims(branch string, claims []Claim) error
	// CompleteAndVerify closes the contribution over the base closure and
	// verifies it (steps 3–4), yielding a sealed VerifiedContribution.
	CompleteAndVerify(ctx context.Context) (VerifiedContribution, error)
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

// MergableContribution is a persisted contribution ready for the
// Sequencer to merge (step 6): its claims are durably in 𝒰 and its head
// claim(s) are known.
type MergableContribution interface {
	// Heads are the contribution's open head claim ids per branch, which the
	// Sequencer references under those names from the new branch-table claim.
	Heads() map[string][]Id
}
