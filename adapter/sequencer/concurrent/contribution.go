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

// contribution is an in-progress advance of the archive: steps 2–4, opened
// against a base (k, t) the Sequencer stamped at step 1. Nothing it does touches
// the sequencing thread, so any number of these run at once — the whole point of
// the concurrent write path. It is sealed by CompleteAndVerify and refuses
// further filling from then on.
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

// AddClaims is step 2, adding: it admits a batch of claims in dependency order
// (a claim's references precede it). Each is checked against the contribution's
// base before it is staged, and nothing is written to 𝒰 yet — filling is pure
// in-memory work, so it never contends with another writer.
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

// AddGraph is step 2 for claims the caller already staged in a Graph. A Graph
// writes through to 𝒰 as it is filled, so the claims are present already and
// what the contribution needs from it is its open-head frontier — those heads
// become the contribution's roots. They are checked (and their closure walked)
// at CompleteAndVerify, where they can be loaded.
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

// admissible applies the two step-2 rules to one claim: it may not be dated
// after the base time t — no future-dating past the server's clock (§Timestamping)
// — and it may not use a node type reserved to the Sequencer. Branch tables are
// the Sequencer's alone; letting a client author one would let it forge the
// archive head. (Limiting and expiry claims are the paper's other reserved types;
// this library does not model them yet, so there is nothing further to reject.)
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

// CompleteAndVerify runs steps 3 and 4 as the paper prescribes — one traversal
// that both closes the contribution over its base and verifies what it finds —
// and seals the result.
//
// Closing is the graph's own job: each added claim becomes an open head and the
// ids it references drop out of the frontier, so what remains is exactly the set
// of roots this contribution offers the branch. Adopted graph heads go in FIRST,
// so a staged claim citing one correctly pushes it interior.
//
// The walk prunes at anything the Sequencer has already merged (WithTrusted):
// those claims were verified before they landed and immutability keeps them
// valid, so the cost of a contribution is its own closure, not the archive's.
// What the walk does reach beyond that is drawn in and verified — the paper's
// "any referenced claim outside it, from another branch or the wider Universe".
//
// Note what the step-2 rules cover once the walk is done. Every claim handed to
// this contribution — staged or adopted — is checked explicitly. Claims pulled in
// transitively are covered too, and provably: the verifier forbids any claim from
// referencing a branch table, so a branch table can only ever enter as something
// handed over directly; and created_at monotonicity (§4.3) means a referenced
// claim is never dated later than the claim citing it, so a checked root's
// ceiling propagates down its whole closure.
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
	for _, id := range c.adopted {
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
	// Staging into 𝒰 happens here rather than at step 5 because the verifier
	// reads the closure back out of 𝒰 — there is nowhere else for the walk to
	// find it. That is safe: 𝒰 is content-addressed and add-only, so a claim
	// written by a contribution that then fails verification is unreachable
	// from any head and can never enter the archive. It is garbage, not a
	// corruption (and collecting it is out of this adapter's scope).
	if err := g.AddClaims(ctx, c.staged...); err != nil {
		return nil, fmt.Errorf("%w: stage: %w", errContribution, err)
	}
	for _, cl := range c.staged {
		ids = append(ids, cl.ID())
	}

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

// verified is a sealed, verified contribution: its contents are fixed, so by
// immutability whatever verified stays valid however long it waits for its
// merge.
type verified struct {
	c     *contribution
	ids   []ranke.Id
	heads []ranke.Id
}

// Ids are the claim ids the contribution adds — everything handed to it, staged
// or adopted. Claims its closure drew in from elsewhere in the Universe are not
// listed: the contribution did not add them, it only depends on them.
func (v *verified) Ids() []ranke.Id {
	out := make([]ranke.Id, len(v.ids))
	copy(out, v.ids)
	return out
}

// Persist is step 5, the durability barrier before the head may advance.
// Completion already wrote the batch into 𝒰 — the verification walk had to read
// it from there — so what is left, and what actually matters here, is confirming
// it reads back. A composed Universe (§Composing Universes) writes its lazy and
// async tiers off the critical path, and the paper is explicit about why that
// must be settled first: a head pointing at a claim that failed to persist
// leaves the graph unretrievable, with the claims still in 𝒰 but no way to find
// them. HasClaims over the contribution's own ids and heads is that confirmation.
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

// mergable is a persisted contribution ready for step 6: its closure is durably
// in 𝒰 and its open heads are known. It carries the branch it targets and the
// Sequencer that opened it, neither of which is on the MergableContribution
// contract — the Sequencer needs both to merge it into the right place, and to
// refuse one that came from somewhere else.
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
