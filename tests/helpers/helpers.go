// package: tests/helpers / testkit
// type:    tool
// job:     shared helpers for driving a ranke.Sequencer from tests and test tooling
// limits:  the contract only — no fixtures, no assertions, no backend knowledge (-> tests, tests/backends)
package helpers

import (
	"context"

	"github.com/flocko-motion/ranke-go"
)

// Contribute drives one contribution through the Sequencer contract's six steps —
// open, fill, verify, persist, merge — returning the archive head it advanced to.
// opts carry the constraints the contribution opens under.
func Contribute(ctx context.Context, seq ranke.Sequencer, branch string, claims []ranke.Claim,
	opts ...ranke.ContributionOption) (ranke.Id, error) {
	c, err := seq.NewContribution(ctx, opts...)
	if err != nil {
		return nil, err
	}
	if err := c.AddClaims(branch, claims); err != nil {
		return nil, err
	}
	v, err := c.CompleteAndVerify(ctx)
	if err != nil {
		return nil, err
	}
	m, err := v.Persist(ctx)
	if err != nil {
		return nil, err
	}
	r, err := seq.Merge(ctx, m)
	if err != nil {
		return nil, err
	}
	return r.Head(), nil
}
