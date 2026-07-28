// Sequencer tests — the reference dev Sequencer write path: bootstrap at an
// empty branch table, a head that advances per commit, and deterministic head
// ids from a fixed clock.
//
// The optimistic-concurrency write path (Prepare / Submit against a snapshot
// height, consolidating concurrent same-branch contributions) is a draft
// contract (ranke.Sequencer's NewContribution/Merge) with no implementation
// yet — those tests return when that Sequencer lands.
package tests

import (
	"context"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

// TestSequencerBootstrap: a fresh archive opens at an empty branch table — a
// contribution/branches head carrying no branches until the first commit.
func TestSequencerBootstrap(t *testing.T) {
	ctx := context.Background()
	f := memFixture(t, ctx)

	arc := f.snapshot(t)
	brs, err := arc.GetBranches(ctx)
	require.NoError(t, err, "GetBranches")
	require.Empty(t, brs, "fresh archive has no branches")

	has, err := arc.HasBranch(ctx, "main")
	require.NoError(t, err, "HasBranch")
	require.False(t, has, "no main branch before the first commit")
}

// TestSequencerAdvancesHead: each AddClaims advances the head, and Head()
// tracks the latest commit.
func TestSequencerAdvancesHead(t *testing.T) {
	ctx := context.Background()
	f := memFixture(t, ctx)

	h0 := f.seq.Head()
	h1 := f.write(t, f.email(t, f.self, "a@example.com", "b@example.com", "one\r\n"))
	require.False(t, h1.Equal(h0), "first commit advanced the head")

	h2 := f.write(t, f.email(t, f.self, "a@example.com", "b@example.com", "two\r\n"))
	require.False(t, h2.Equal(h1), "second commit advanced the head again")
	require.True(t, f.seq.Head().Equal(h2), "Head() tracks the latest commit")
}

// TestSequencerDeterministic: same clock, self, and claims → identical head
// ids, the property the cross-implementation conformance vectors rely on.
func TestSequencerDeterministic(t *testing.T) {
	ctx := context.Background()
	run := func() ranke.Id {
		f := memFixture(t, ctx)
		return f.write(t, f.email(t, f.self, "a@example.com", "b@example.com", "same\r\n"))
	}
	require.True(t, run().Equal(run()), "identical inputs yield identical head ids")
}
