// Sequencer tests — the write path introduced by the Archive/Sequencer
// split: atomic (snapshot, height) reads, and the auto-consolidation of
// concurrent same-branch contributions (Q1 = no lost writes).
package tests

import (
	"context"
	"testing"

	"github.com/flocko-motion/ranke-go"
	seqmem "github.com/flocko-motion/ranke-go/adapter/sequencer/mem"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	"github.com/stretchr/testify/require"
)

func newMemSequencer(t *testing.T, ctx context.Context) ranke.Sequencer {
	t.Helper()
	s, err := ranke.NewSequencer(ctx, mem.New(), seqmem.New(), seqSelf())
	require.NoError(t, err)
	return s
}

// TestSequencerHeightAdvances confirms GetArchive reports a monotone
// height that advances by one per commit, starting at 0 for a fresh
// archive.
func TestSequencerHeightAdvances(t *testing.T) {
	ctx := context.Background()
	s := newMemSequencer(t, ctx)
	op := contributor(t, "op@example.com")

	_, h0, err := s.GetArchive(ctx)
	require.NoError(t, err)
	require.Equal(t, ranke.Height(0), h0, "fresh archive at height 0")

	require.NoError(t, s.AddGraph(ctx, "main", ranke.NewGraph(op), op))
	_, h1, err := s.GetArchive(ctx)
	require.NoError(t, err)
	require.Equal(t, ranke.Height(1), h1, "one commit → height 1")

	em := email(t, op, "alice@example.com", "bob@example.com", "hi\n")
	require.NoError(t, s.AddClaim(ctx, "main", em))
	_, h2, err := s.GetArchive(ctx)
	require.NoError(t, err)
	require.Equal(t, ranke.Height(2), h2, "two commits → height 2")
}

// TestSequencerPrepareStampsSnapshotHeight confirms a proof carries the
// height of the snapshot it was prepared against.
func TestSequencerPrepareStampsSnapshotHeight(t *testing.T) {
	ctx := context.Background()
	s := newMemSequencer(t, ctx)
	op := contributor(t, "op@example.com")
	require.NoError(t, s.AddGraph(ctx, "main", ranke.NewGraph(op), op))

	arc, h, err := s.GetArchive(ctx)
	require.NoError(t, err)
	em := email(t, op, "alice@example.com", "bob@example.com", "x\n")
	g := ranke.NewGraph(op)
	require.NoError(t, g.Add(em))
	proof, err := arc.Prepare(ctx, "main", g, op)
	require.NoError(t, err)
	require.Equal(t, h, proof.Height(), "proof stamped with the snapshot height")
	require.False(t, proof.ContainsLimiting(), "no limiting semantics yet → clean path")
}

// TestSequencerConcurrentWritesConsolidate is the core Q1 proof: two
// contributions to the same branch, both prepared against the same prior
// head, both survive. Submit consolidates the moved head instead of
// dropping the earlier write.
func TestSequencerConcurrentWritesConsolidate(t *testing.T) {
	ctx := context.Background()
	s := newMemSequencer(t, ctx)
	op := contributor(t, "op@example.com")

	// Bootstrap main.
	require.NoError(t, s.AddGraph(ctx, "main", ranke.NewGraph(op), op))

	// Load two independent extensions of main, each against the same
	// prior head (no commit between the two GetArchive calls).
	loadMainGraph := func() (ranke.Graph, ranke.Archive) {
		arc, _, err := s.GetArchive(ctx)
		require.NoError(t, err)
		b, err := arc.GetBranch(ctx, "main")
		require.NoError(t, err)
		hc, err := arc.GetClaim(ctx, b.Latest().Head())
		require.NoError(t, err)
		g, err := hc.Graph(ctx)
		require.NoError(t, err)
		return g, arc
	}

	gA, arcA := loadMainGraph()
	emA := email(t, op, "alice@example.com", "bob@example.com", "A\n")
	require.NoError(t, gA.Add(emA))

	gB, arcB := loadMainGraph()
	emB := email(t, op, "alice@example.com", "bob@example.com", "B\n")
	require.NoError(t, gB.Add(emB))

	proofA, err := arcA.Prepare(ctx, "main", gA, op)
	require.NoError(t, err)
	proofB, err := arcB.Prepare(ctx, "main", gB, op)
	require.NoError(t, err)
	require.Equal(t, proofA.Height(), proofB.Height(), "both prepared at the same height")

	// Serial submit: A lands, B finds the head moved and consolidates.
	_, err = s.Submit(ctx, proofA)
	require.NoError(t, err)
	rB, err := s.Submit(ctx, proofB)
	require.NoError(t, err)

	// Both emails reachable through the consolidated head — no lost write.
	arc, _, err := s.GetArchive(ctx)
	require.NoError(t, err)
	head, err := arc.GetClaim(ctx, rB.Head())
	require.NoError(t, err)
	g, err := head.Graph(ctx)
	require.NoError(t, err)
	require.True(t, g.Contains(emA.ID()), "first concurrent write survived consolidation")
	require.True(t, g.Contains(emB.ID()), "second concurrent write present")
	require.NoError(t, g.Validate(), "consolidated closure validates")
}
