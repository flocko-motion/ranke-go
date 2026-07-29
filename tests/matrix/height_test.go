package matrix_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
	devhist "github.com/flocko-motion/ranke-go/adapter/history/dev"
	devseq "github.com/flocko-motion/ranke-go/adapter/sequencer/dev"
	"github.com/flocko-motion/ranke-go/tests/backends"
	"github.com/flocko-motion/ranke-go/tests/generator"
	"github.com/flocko-motion/ranke-go/tests/helpers"
	"github.com/flocko-motion/ranke-go/tests/rql"
)

// TestHeightIsAField: height filters through Where like any other field, across an
// archive built over two contributions. A claim's height is its depth, so both
// sources sit at 1 whichever revision added them.
func TestHeightIsAField(t *testing.T) {
	eachRevisionBackend(t, func(t *testing.T, u ranke.Universe, rev revisions) {
		both := []string{rev.first.String(), rev.second.String()}
		sources := ranke.Where{Field: "type", Test: &ranke.Comparison{Glob: "source/*"}}

		atOne := reached(t, u, rev.head2, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch},
			Where: &ranke.Where{And: []ranke.Where{sources,
				{Field: "height", Test: &ranke.Comparison{Le: 1}}}},
		})
		require.ElementsMatch(t, both, atOne, "both sources are at height 1")

		belowOne := reached(t, u, rev.head2, ranke.Query{
			Select: ranke.Select{Branch: rql.Branch},
			Where: &ranke.Where{And: []ranke.Where{sources,
				{Field: "height", Test: &ranke.Comparison{Lt: 1}}}},
		})
		require.Empty(t, belowOne, "no source sits below height 1")
	})
}

// revisions names the two-revision toy: a claim per revision, plus each revision's
// archive head.
type revisions struct {
	first, second ranke.Id
	head1, head2  ranke.Id
}

// eachRevisionBackend builds the two-revision toy into every available backend.
func eachRevisionBackend(t *testing.T, check func(*testing.T, ranke.Universe, revisions)) {
	t.Helper()
	for _, row := range backends.All() {
		t.Run(row.Name, func(t *testing.T) {
			u, cleanup, err := row.Open()
			if errors.Is(err, backends.ErrUnavailable) {
				t.Skipf("unavailable here: %v", err)
			}
			require.NoError(t, err)
			defer cleanup()
			check(t, u, buildRevisions(t, u))
		})
	}
}

// buildRevisions commits two source claims in two contributions, so the branch has
// two revisions whose membership differs by exactly one claim.
func buildRevisions(t *testing.T, u ranke.Universe) revisions {
	t.Helper()
	ctx := context.Background()
	clock := generator.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Second)

	priv := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)
	opClaim, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pubkey).
		WithEncoding(ranke.EncodingOctetStream).
		WithField(ranke.FieldName, "revisions").
		WithCreatedAt(clock.Tick()).
		Sign(priv)
	require.NoError(t, err)
	op, err := opClaim.AsContributor(ctx, nil, priv)
	require.NoError(t, err)

	seq, err := devseq.NewSequencer(ctx, u, devhist.New(clock), op, clock)
	require.NoError(t, err)

	note := func(body string) ranke.Claim {
		c, err := ranke.NewClaim(ranke.TypeSource("note"), op).
			WithInlineContent([]byte(body)).
			WithEncoding(ranke.EncodingPlain).
			WithCreatedAt(clock.Tick()).
			WithHeight(ranke.HeightOf(op)).
			Sign()
		require.NoError(t, err)
		return c
	}

	// Two contributions, so the archive carries two revisions of "main".
	commit := func(c ranke.Claim) ranke.Id {
		head, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{c})
		require.NoError(t, err)
		return head
	}

	first, second := note("the first claim"), note("the second claim")
	head1, head2 := commit(first), commit(second)

	return revisions{first: first.ID(), second: second.ID(), head1: head1, head2: head2}
}
