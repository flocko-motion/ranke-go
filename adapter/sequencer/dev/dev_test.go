package dev_test

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/flocko-motion/ranke-go"
	historymem "github.com/flocko-motion/ranke-go/adapter/history/mem"
	devseq "github.com/flocko-motion/ranke-go/adapter/sequencer/dev"
	"github.com/stretchr/testify/require"
)

// clock is a deterministic monotonic time source satisfying devseq.Clock.
type clock struct{ t time.Time }

func (c *clock) Tick() time.Time {
	out := c.t
	c.t = c.t.Add(time.Second)
	return out
}

// operator builds a signed root contributor carrying its own signing key, so
// the Sequencer can attribute and sign with it. It is an initial node (height 0).
func operator(t *testing.T, ctx context.Context, at time.Time) ranke.Contributor {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	pub, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)
	cc, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pub).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(at).
		Sign(priv)
	require.NoError(t, err)
	op, err := cc.AsContributor(ctx, nil, priv)
	require.NoError(t, err)
	return op
}

func newSequencer(t *testing.T, ctx context.Context) (*devseq.Sequencer, ranke.Contributor, *clock) {
	t.Helper()
	u := ranke.NewMemoryUniverse()
	clk := &clock{t: time.Unix(1000, 0).UTC()}
	op := operator(t, ctx, clk.Tick())
	seq, err := devseq.NewSequencer(ctx, u, historymem.New(), op, clk)
	require.NoError(t, err)
	return seq, op, clk
}

// TestInvalidContributionDenied is the guarantee the Sequencer must uphold:
// valid(A) → valid(A'). A contribution referencing a claim that does not exist
// is structurally invalid; the Sequencer must verify (paper 2 §Sequencer step 4)
// and reject it, leaving the archive head unchanged so no invalid state is ever
// published.
func TestInvalidContributionDenied(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)
	headBefore := seq.Head()

	// A well-formed, signed claim whose one derivation edge references an id
	// that was never contributed — so its closure cannot be verified.
	bogus, err := ranke.HashContent([]byte("no-such-claim"))
	require.NoError(t, err)
	de, err := ranke.NewEdge(ranke.EdgeConfig{Reference: bogus, Type: ranke.TypeDerivation("note")})
	require.NoError(t, err)
	bad, err := ranke.NewClaim(ranke.TypeDerivation("note"), op).
		WithInlineContent([]byte("derived from nothing")).
		WithEncoding(ranke.EncodingPlain).
		WithEdges(de).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err, "the claim itself is well-formed; only its reference dangles")

	_, err = seq.AddClaimsToBranch(ctx, "main", []ranke.Claim{bad})
	require.Error(t, err, "the Sequencer must deny a contribution that references a non-existent claim")

	require.True(t, headBefore.Equal(seq.Head()),
		"a denied contribution must not advance the archive head")
}

// TestValidContributionAccepted is the positive control: a well-formed
// contribution whose closure resolves is verified and merged, and the head
// advances. Without this, TestInvalidContributionDenied could pass by denying
// everything.
func TestValidContributionAccepted(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)
	headBefore := seq.Head()

	// A source note attributed to the operator: its only reference is the
	// auto-added contribution/contributor edge to the operator (height 0, already
	// in the Universe), so the claim is height 1 and its closure resolves.
	good, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte("a real source note")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)

	head, err := seq.AddClaimsToBranch(ctx, "main", []ranke.Claim{good})
	require.NoError(t, err, "a valid contribution must be accepted")
	require.False(t, headBefore.Equal(head), "a merged contribution must advance the archive head")
	require.True(t, head.Equal(seq.Head()))
}
