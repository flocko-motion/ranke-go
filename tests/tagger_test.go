// Tagger tests — the archive-wide branch-tagging pass (ranke.TagArchiveBranches)
// driven over the mem backend, which is Tags-capable and its own Tagger.
package tests

import (
	"context"
	"testing"
	"time"

	"github.com/flocko-motion/ranke-go"
	devhist "github.com/flocko-motion/ranke-go/adapter/history/dev"
	devseq "github.com/flocko-motion/ranke-go/adapter/sequencer/dev"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	"github.com/flocko-motion/ranke-go/generator"
	"github.com/stretchr/testify/require"
)

// TestTagArchiveBranches exercises the archive-wide tagging pass: after a
// commit, tagging the head-id timeline stamps the branch's closure with the
// revision each claim joined "main", read back via BranchRevision.
func TestTagArchiveBranches(t *testing.T) {
	ctx := context.Background()
	clock := generator.NewClock(fixtureBase, time.Second)
	self := keyedContributor(t, ctx, clock, "sequencer")

	u := mem.New()
	tagger, ok := u.(ranke.Tagger)
	require.True(t, ok, "mem is a Tagger (Tags-capable)")
	hist := devhist.New(clock)
	seq, err := devseq.NewSequencer(ctx, u, hist, self, clock)
	require.NoError(t, err, "NewSequencer")

	em, err := ranke.NewClaim(ranke.TypeSource("email"), self).
		WithInlineContent([]byte("From: a\r\nTo: b\r\n\r\nhi\r\n")).
		WithCreatedAt(clock.Tick()).
		WithHeight(ranke.HeightOf(self)).
		Sign()
	require.NoError(t, err, "sign email")
	_, err = seq.AddClaims(ctx, []ranke.Claim{em})
	require.NoError(t, err, "AddClaims")

	// The head-id timeline k₀…kₙ, oldest→newest — the authoritative spine.
	n, err := hist.Len(ctx)
	require.NoError(t, err, "history len")
	items := make([]ranke.HistoryItem, 0, n)
	for i := 0; i < n; i++ {
		it, err := hist.Get(ctx, i)
		require.NoError(t, err, "history get")
		items = append(items, it)
	}

	require.NoError(t, ranke.TagArchiveBranches(ctx, u, tagger, items), "TagArchiveBranches")

	// The branch head joined "main" — its membership revision is recorded.
	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err, "GetArchive")
	b, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err, "GetBranch main")
	_, tagged, err := tagger.BranchRevision(ctx, b.Head(), "main")
	require.NoError(t, err, "BranchRevision")
	require.True(t, tagged, "the branch head is tagged as a member of main")
}
