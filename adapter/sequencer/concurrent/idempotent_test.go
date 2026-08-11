package concurrent_test

// Idempotence of the write path: a contribution carrying nothing the branch does
// not already hold must leave the archive exactly where it was — no new head, no
// revision, and no error. Identical claims have identical ids (§Idempotency), so
// re-contributing them cannot change RG_k, and an advance would be a revision
// that says nothing.

import (
	"context"
	"sync"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/tests/helpers"
	"github.com/stretchr/testify/require"
)

// revisions is the number of entries in the head timeline — the count a spurious
// advance would inflate.
func (f *fixture) revisions(t *testing.T, ctx context.Context) int {
	t.Helper()
	n, err := f.hist.Len(ctx)
	require.NoError(t, err)
	return n
}

// TestReContributionIsNoop: the same claim contributed twice. The second merge
// must return the standing head rather than mint a revision, and must not error —
// the caller asked for a state that already holds.
func TestReContributionIsNoop(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	c := f.note(t, "contributed twice")
	first, err := helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{c})
	require.NoError(t, err)

	headAfter := f.head(t, ctx)
	branchAfter := f.branchHead(t, ctx, "main")
	revsAfter := f.revisions(t, ctx)

	second, err := helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{c})
	require.NoError(t, err, "re-contributing an existing claim is a no-op, not an error")

	require.True(t, first.Equal(second), "the second merge returns the standing head")
	require.True(t, headAfter.Equal(f.head(t, ctx)), "the archive head must not advance")
	require.True(t, branchAfter.Equal(f.branchHead(t, ctx, "main")), "the branch head must not advance")
	require.Equal(t, revsAfter, f.revisions(t, ctx), "no revision is recorded for a no-op")
	f.requireReachable(t, ctx, "main", []ranke.Id{c.ID()})
}

// TestReContributionOfInteriorClaimIsNoop is the case a head-equality check alone
// would miss: by the time the claim is offered again the branch head has moved
// past it, so it is interior rather than the head. Folding it back in would wrap
// the current head and its own ancestor in a fresh consolidating claim — a new
// head over an unchanged closure.
func TestReContributionOfInteriorClaimIsNoop(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	first := f.note(t, "first")
	_, err := helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{first})
	require.NoError(t, err)

	second := f.note(t, "second")
	_, err = helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{second})
	require.NoError(t, err)

	// The branch head is now a fold over both, so `first` is interior to it.
	branchAfter := f.branchHead(t, ctx, "main")
	require.False(t, branchAfter.Equal(first.ID()), "the head has moved past the first claim")
	revsAfter := f.revisions(t, ctx)

	_, err = helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{first})
	require.NoError(t, err)

	require.True(t, branchAfter.Equal(f.branchHead(t, ctx, "main")),
		"re-offering an interior claim must not fold a new head over the branch")
	require.Equal(t, revsAfter, f.revisions(t, ctx), "and must not record a revision")
	f.requireReachable(t, ctx, "main", []ranke.Id{first.ID(), second.ID()})
}

// TestPartlyNewContributionAdvances is the control: a batch mixing a claim the
// branch holds with one it does not still advances, and both end up reachable.
// Without this, idempotence could pass by refusing everything.
func TestPartlyNewContributionAdvances(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	old := f.note(t, "already present")
	_, err := helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{old})
	require.NoError(t, err)

	branchBefore := f.branchHead(t, ctx, "main")
	revsBefore := f.revisions(t, ctx)

	fresh := f.note(t, "genuinely new")
	_, err = helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{old, fresh})
	require.NoError(t, err)

	require.False(t, branchBefore.Equal(f.branchHead(t, ctx, "main")),
		"a batch containing anything new must advance the branch")
	require.Equal(t, revsBefore+1, f.revisions(t, ctx), "exactly one revision for one advance")
	f.requireReachable(t, ctx, "main", []ranke.Id{old.ID(), fresh.ID()})
}

// TestSameClaimToAnotherBranchAdvances: "already in the archive" is not "already
// in this branch". Branches are independent heads, so a claim main holds is new to
// alpha and must advance it — the closure test has to be per branch.
func TestSameClaimToAnotherBranchAdvances(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	c := f.note(t, "shared across branches")
	_, err := helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{c})
	require.NoError(t, err)
	revsBefore := f.revisions(t, ctx)

	_, err = helpers.Contribute(ctx, f.seq, "alpha", []ranke.Claim{c})
	require.NoError(t, err, "a claim another branch holds is new to this one")

	require.Equal(t, revsBefore+1, f.revisions(t, ctx))
	f.requireReachable(t, ctx, "alpha", []ranke.Id{c.ID()})
	f.requireReachable(t, ctx, "main", []ranke.Id{c.ID()})
}

// TestConcurrentDuplicateContributionsConverge: many writers offering the SAME
// claim at once. Testing against the live closure rather than each contribution's
// base is what makes them converge — whoever commits first advances, the rest find
// it present and stand down. The timeline gains exactly one entry however the
// writers interleave or batch.
func TestConcurrentDuplicateContributionsConverge(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t, ctx)

	c := f.note(t, "contributed by everyone at once")
	before := f.revisions(t, ctx)

	const writers = 8
	errs := make([]error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = helpers.Contribute(ctx, f.seq, "main", []ranke.Claim{c})
		}()
	}
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "writer %d", i)
	}
	require.Equal(t, before+1, f.revisions(t, ctx),
		"%d writers offering one claim advance the archive once", writers)
	f.requireReachable(t, ctx, "main", []ranke.Id{c.ID()})
}
