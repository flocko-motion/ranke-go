// package: adapter/sequencer/dev / testkit
// job:     the dev Sequencer's contribution — steps 2–5 (filling, completing, verifying, persisting) behind the ranke.Contribution contract
// type:    adapter
// limits:  one contribution at a time, filled from one goroutine; merging is step 6 and belongs to the Sequencer (-> dev.go)
package dev

import (
	"context"
	"fmt"
	"time"

	"github.com/flocko-motion/ranke-go"
)

var (
	_ ranke.Contribution         = (*contribution)(nil)
	_ ranke.VerifiedContribution = (*verified)(nil)
	_ ranke.MergableContribution = (*mergable)(nil)
)

// contribution is an in-progress advance of the archive against the base (k, t)
// of step 1, staged per branch. CompleteAndVerify seals it.
type contribution struct {
	s        *Sequencer
	baseHead ranke.Id
	baseTime time.Time

	sealed   bool
	staged   map[string][]ranke.Claim
	adopted  map[string][]ranke.Id
	branches []string // first-named order, so a merge is deterministic
}

// Base is the (k, t) the contribution was opened against.
func (c *contribution) Base() (ranke.Id, time.Time) { return c.baseHead, c.baseTime }

// AddClaims is step 2: it stages a batch for branch in dependency order
// (references first).
func (c *contribution) AddClaims(branch string, claims []ranke.Claim) error {
	if c.sealed {
		return errSealed
	}
	if branch == "" {
		return errNoBranch
	}
	for _, cl := range claims {
		if cl == nil {
			return errNilClaim
		}
	}
	c.note(branch)
	c.staged[branch] = append(c.staged[branch], claims...)
	return nil
}

// AddGraph is step 2 for a Graph the caller filled: it adopts the graph's open
// heads as roots for branch, which CompleteAndVerify loads.
func (c *contribution) AddGraph(branch string, g ranke.Graph) error {
	if g == nil {
		return errNilGraph
	}
	if c.sealed {
		return errSealed
	}
	if branch == "" {
		return errNoBranch
	}
	c.note(branch)
	c.adopted[branch] = append(c.adopted[branch], g.Heads()...)
	return nil
}

// note records a branch the first time it is named.
func (c *contribution) note(branch string) {
	if _, seen := c.staged[branch]; seen {
		return
	}
	if _, seen := c.adopted[branch]; seen {
		return
	}
	c.branches = append(c.branches, branch)
}

// CompleteAndVerify runs steps 3 and 4 in one traversal: it closes the
// contribution over its base, verifies what it reaches, and seals the result.
func (c *contribution) CompleteAndVerify(ctx context.Context) (ranke.VerifiedContribution, error) {
	if c.sealed {
		return nil, errSealed
	}
	if len(c.branches) == 0 {
		return nil, errEmptyContribution
	}

	var ids []ranke.Id
	heads := map[string][]ranke.Id{}
	// One graph per branch: each branch takes its own root, and a claim named for
	// two branches is written once — 𝒰 is add-only.
	for _, branch := range c.branches {
		g, err := ranke.NewGraph(ctx, c.s.u)
		if err != nil {
			return nil, fmt.Errorf("%w: new graph: %w", errSequencer, err)
		}
		for _, id := range c.adopted[branch] { // first, so a staged claim citing one pushes it interior
			cl, err := ranke.GetClaim(ctx, c.s.u, id)
			if err != nil {
				return nil, fmt.Errorf("%w: load adopted head %s: %w", errSequencer, id, err)
			}
			if err := g.AddClaims(ctx, cl); err != nil {
				return nil, fmt.Errorf("%w: adopt %s: %w", errSequencer, id, err)
			}
			ids = append(ids, id)
		}
		// The verifier reads the closure out of 𝒰, so staging happens here. 𝒰 is
		// add-only, leaving a failed contribution's claims unreachable from any head.
		if err := g.AddClaims(ctx, c.staged[branch]...); err != nil {
			return nil, fmt.Errorf("%w: populate %q: %w", errSequencer, branch, err)
		}
		for _, cl := range c.staged[branch] {
			ids = append(ids, cl.ID())
		}

		run := g.Verify()
		run.Wait()
		if err := run.Err(); err != nil {
			return nil, fmt.Errorf("%w: verify: %w", errSequencer, err)
		}
		if fs := run.Failures(); len(fs) > 0 {
			return nil, fmt.Errorf("%w: verify: %d failure(s), first: %v", errSequencer, len(fs), fs[0])
		}

		// Fold this branch's open heads into one, so it takes a single root.
		head, err := c.s.consolidateGraph(ctx, g)
		if err != nil {
			return nil, err
		}
		heads[branch] = []ranke.Id{head}
	}
	c.sealed = true
	return &verified{c: c, ids: ids, heads: heads}, nil
}

// verified is a sealed contribution: its contents are fixed, so the verification
// holds however long the merge waits.
type verified struct {
	c     *contribution
	ids   []ranke.Id
	heads map[string][]ranke.Id
}

// Ids are the claim ids the contribution adds: everything handed to it, staged
// or adopted.
func (v *verified) Ids() []ranke.Id {
	out := make([]ranke.Id, len(v.ids))
	copy(out, v.ids)
	return out
}

// Persist is step 5, the durability barrier before the head advances: HasClaims
// confirms every id and head reads back, since a head may not outrun its closure.
func (v *verified) Persist(ctx context.Context) (ranke.MergableContribution, error) {
	ids := append([]ranke.Id{}, v.ids...)
	for _, branch := range v.c.branches {
		ids = append(ids, v.heads[branch]...)
	}
	present, err := v.c.s.u.HasClaims(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("%w: persist: %w", errSequencer, err)
	}
	for i, ok := range present {
		if !ok {
			return nil, fmt.Errorf("%w: persist: %s did not land in the Universe", errSequencer, ids[i])
		}
	}
	return &mergable{s: v.c.s, branches: v.c.branches, heads: v.heads, ids: v.ids}, nil
}

// mergable is a persisted contribution ready for step 6. It names its opening
// Sequencer, so a merge lands in the right archive.
type mergable struct {
	s        *Sequencer
	branches []string // first-named order, so a merge is deterministic
	heads    map[string][]ranke.Id
	ids      []ranke.Id
}

// Heads are the contribution's open head claim ids per branch, which the
// Sequencer folds into each branch's new head.
func (m *mergable) Heads() map[string][]ranke.Id {
	out := make(map[string][]ranke.Id, len(m.heads))
	for b, hs := range m.heads {
		out[b] = append([]ranke.Id{}, hs...)
	}
	return out
}

// receipt is the outcome of a committed merge — the head the archive advanced to.
type receipt struct{ head ranke.Id }

// Head returns the archive head the merge advanced to.
func (r receipt) Head() ranke.Id { return r.head }
