// package: adapter/sequencer/concurrent / adapter
// type:    adapter
// job:     the concurrent Sequencer's contribution stages — steps 2–5 (adding, completing, verifying, persisting), the work that runs OFF the sequencing thread
// limits:  one contribution is not itself concurrency-safe for filling beyond the mutex here (fill it from one goroutine); merging is step 6 and lives with the Sequencer (-> concurrent.go)
package concurrent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flocko-motion/ranke-go"
)

var (
	errSealed            = errors.New("concurrent.Contribution: already sealed")
	errNilClaim          = errors.New("concurrent.Contribution.AddClaims: nil claim")
	errNilGraph          = errors.New("concurrent.Contribution.AddGraph: nil graph")
	errEmptyContribution = errors.New("concurrent.Contribution.CompleteAndVerify: nothing was added")
	errFutureDated       = errors.New("concurrent.Contribution: claim is dated after the contribution base")
	errReservedType      = errors.New("concurrent.Contribution: node type is reserved to the Sequencer")
	errContribution      = errors.New("concurrent.Contribution")
)

var (
	_ ranke.Contribution         = (*contribution)(nil)
	_ ranke.VerifiedContribution = (*verified)(nil)
	_ ranke.MergableContribution = (*mergable)(nil)
)

// contribution is an in-progress advance of the archive — steps 2–4 against the
// base (k, t) of step 1, off the sequencing thread. CompleteAndVerify seals it.
type contribution struct {
	s        *Sequencer
	branch   string
	baseHead ranke.Id
	baseTime time.Time

	mu      sync.Mutex
	sealed  bool
	staged  []ranke.Claim // claims handed over by AddClaims, in dependency order
	adopted []ranke.Id    // open heads handed over by AddGraph (already in 𝒰)
}

// Base is the (k, t) the contribution was opened against.
func (c *contribution) Base() (ranke.Id, time.Time) { return c.baseHead, c.baseTime }

// AddClaims is step 2: it stages a batch in dependency order (references first),
// each claim checked against the base. In-memory work alone.
func (c *contribution) AddClaims(claims []ranke.Claim) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealed {
		return errSealed
	}
	for _, cl := range claims {
		if cl == nil {
			return errNilClaim
		}
		if err := c.admissible(cl); err != nil {
			return err
		}
	}
	c.staged = append(c.staged, claims...)
	return nil
}

// AddGraph is step 2 for a Graph the caller filled: it adopts the graph's open
// heads as roots, which CompleteAndVerify loads and checks.
func (c *contribution) AddGraph(g ranke.Graph) error {
	if g == nil {
		return errNilGraph
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealed {
		return errSealed
	}
	c.adopted = append(c.adopted, g.Heads()...)
	return nil
}

// admissible applies the two step-2 rules: a claim is dated at or before the base
// time t (§Timestamping), and its type is one the Sequencer leaves to clients.
func (c *contribution) admissible(cl ranke.Claim) error {
	if t := cl.Node().CreatedAt(); t.After(c.baseTime) {
		return fmt.Errorf("%w: %s is dated %s, base is %s", errFutureDated,
			cl.ID(), t.Format(time.RFC3339Nano), c.baseTime.Format(time.RFC3339Nano))
	}
	if cl.Node().Type() == ranke.NodeBranches {
		return fmt.Errorf("%w: %s is a %s claim", errReservedType, cl.ID(), ranke.NodeBranches)
	}
	return nil
}

// CompleteAndVerify runs steps 3 and 4 in one traversal: it closes the
// contribution over its base, verifies what it reaches, and seals the result.
func (c *contribution) CompleteAndVerify(ctx context.Context) (ranke.VerifiedContribution, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sealed {
		return nil, errSealed
	}
	if len(c.staged) == 0 && len(c.adopted) == 0 {
		return nil, errEmptyContribution
	}

	g, err := ranke.NewGraph(ctx, c.s.u)
	if err != nil {
		return nil, fmt.Errorf("%w: new graph: %w", errContribution, err)
	}
	ids := make([]ranke.Id, 0, len(c.adopted)+len(c.staged))
	for _, id := range c.adopted { // first, so a staged claim citing one pushes it interior
		cl, err := ranke.GetClaim(ctx, c.s.u, id)
		if err != nil {
			return nil, fmt.Errorf("%w: load adopted head %s: %w", errContribution, id, err)
		}
		if err := c.admissible(cl); err != nil {
			return nil, err
		}
		if err := g.AddClaims(ctx, cl); err != nil {
			return nil, fmt.Errorf("%w: adopt %s: %w", errContribution, id, err)
		}
		ids = append(ids, id)
	}
	// The verifier reads the closure out of 𝒰, so staging happens here. 𝒰 is
	// add-only, leaving a failed contribution's claims unreachable from any head.
	if err := g.AddClaims(ctx, c.staged...); err != nil {
		return nil, fmt.Errorf("%w: stage: %w", errContribution, err)
	}
	for _, cl := range c.staged {
		ids = append(ids, cl.ID())
	}

	// Pruning at merged claims costs a contribution its own closure, not the archive's.
	run := g.Verify(ranke.WithTrusted(c.s.isCommitted))
	run.Wait()
	if err := run.Err(); err != nil {
		return nil, fmt.Errorf("%w: verify: %w", errContribution, err)
	}
	if fs := run.Failures(); len(fs) > 0 {
		return nil, fmt.Errorf("%w: verify: %d failure(s), first: %v", errContribution, len(fs), fs[0])
	}

	c.sealed = true
	return &verified{c: c, ids: ids, heads: g.Heads()}, nil
}

// verified is a sealed contribution: its contents are fixed, so the verification
// holds however long the merge waits.
type verified struct {
	c     *contribution
	ids   []ranke.Id
	heads []ranke.Id
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
	ids := make([]ranke.Id, 0, len(v.ids)+len(v.heads))
	ids = append(ids, v.ids...)
	ids = append(ids, v.heads...)

	present, err := v.c.s.u.HasClaims(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("%w: persist: %w", errContribution, err)
	}
	if len(present) != len(ids) {
		return nil, fmt.Errorf("%w: persist: HasClaims answered %d of %d ids",
			errContribution, len(present), len(ids))
	}
	for i, ok := range present {
		if !ok {
			return nil, fmt.Errorf("%w: persist: %s did not land in the Universe", errContribution, ids[i])
		}
	}
	return &mergable{s: v.c.s, branch: v.c.branch, heads: v.heads, ids: v.ids}, nil
}

// mergable is a persisted contribution ready for step 6. It names its target
// branch and opening Sequencer, so a merge lands in the right place.
type mergable struct {
	s      *Sequencer
	branch string
	heads  []ranke.Id
	ids    []ranke.Id
}

// Heads are the contribution's open head claim ids, which the Sequencer folds
// into the target branch's new head.
func (m *mergable) Heads() []ranke.Id {
	out := make([]ranke.Id, len(m.heads))
	copy(out, m.heads)
	return out
}
