package dev_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"github.com/flocko-motion/ranke-go"
	historymem "github.com/flocko-motion/ranke-go/adapter/history/mem"
	devseq "github.com/flocko-motion/ranke-go/adapter/sequencer/dev"
	"github.com/flocko-motion/ranke-go/tests/helpers"
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

// head resolves the archive head k through a fresh snapshot.
func head(t *testing.T, ctx context.Context, seq ranke.Sequencer) ranke.Id {
	t.Helper()
	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err)
	return arc.Head()
}

// TestInvalidContributionDenied is the guarantee the Sequencer must uphold:
// valid(A) → valid(A'). A contribution referencing a claim that does not exist
// is structurally invalid; the Sequencer must verify (paper 2 §Sequencer step 4)
// and reject it, leaving the archive head unchanged so no invalid state is ever
// published.
func TestInvalidContributionDenied(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)
	headBefore := head(t, ctx, seq)

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

	_, err = helpers.Contribute(ctx, seq, "main", []ranke.Claim{bad})
	require.Error(t, err, "the Sequencer must deny a contribution that references a non-existent claim")

	require.True(t, headBefore.Equal(head(t, ctx, seq)),
		"a denied contribution must not advance the archive head")
}

// TestValidContributionAccepted is the positive control: a well-formed
// contribution whose closure resolves is verified and merged, and the head
// advances. Without this, TestInvalidContributionDenied could pass by denying
// everything.
func TestValidContributionAccepted(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)
	headBefore := head(t, ctx, seq)

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

	merged, err := helpers.Contribute(ctx, seq, "main", []ranke.Claim{good})
	require.NoError(t, err, "a valid contribution must be accepted")
	require.False(t, headBefore.Equal(merged), "a merged contribution must advance the archive head")
	require.True(t, merged.Equal(head(t, ctx, seq)), "the receipt names the snapshot's k")
}

// limiting builds a claim of a type reserved to the Sequencer: it names the target it
// restricts, which is what makes it limiting (paper 2 §Deletion).
func limiting(t *testing.T, op ranke.Contributor, target ranke.Claim, at time.Time) ranke.Claim {
	t.Helper()
	e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: target.ID(), Type: ranke.NodeDelete})
	require.NoError(t, err)
	c, err := ranke.NewClaim(ranke.NodeDelete, op).
		WithEdges(e).
		WithHeight(2).
		WithCreatedAt(at).
		Sign()
	require.NoError(t, err)
	return c
}

// TestReservedTypeDenied: limiting and branch-table claims are the Sequencer's alone
// (step 2), so a contribution carrying one is refused however well-formed it is.
func TestReservedTypeDenied(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	note, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte("the claim a delete would name")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)
	headBefore := head(t, ctx, seq)

	_, err = helpers.Contribute(ctx, seq, "main",
		[]ranke.Claim{note, limiting(t, op, note, clk.Tick())})
	require.ErrorIs(t, err, ranke.ErrReservedType, "a client may not create a limiting claim")
	require.True(t, headBefore.Equal(head(t, ctx, seq)), "a denied contribution must not advance the head")
}

// TestReservedTypeLifted is the control: the rule is a constraint, not a prohibition,
// so a caller holding the Sequencer can admit the type — which is how a fixture
// builder stands in for the Sequencer that would mint it.
func TestReservedTypeLifted(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	note, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte("the claim the delete names")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)

	merged, err := helpers.Contribute(ctx, seq, "main",
		[]ranke.Claim{note, limiting(t, op, note, clk.Tick())},
		ranke.WithLiftedTypes(ranke.NodeDelete))
	require.NoError(t, err, "the lifted type is admitted")
	require.True(t, merged.Equal(head(t, ctx, seq)), "the contribution merged")
}

// TestLiftIsPerType: lifting one reserved type says nothing about the others, so a
// contribution cannot smuggle a branch table in behind a delete.
func TestLiftIsPerType(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	table, err := ranke.NewClaim(ranke.NodeBranches, op).
		WithHeight(1).
		WithCreatedAt(clk.Tick()).
		Sign()
	require.NoError(t, err)

	_, err = helpers.Contribute(ctx, seq, "main", []ranke.Claim{table},
		ranke.WithLiftedTypes(ranke.NodeDelete))
	require.ErrorIs(t, err, ranke.ErrReservedType, "the branch table is still the Sequencer's")
}

// TestAddWireAdvancesDeclaredBranches drives a contribution the way a server does:
// read the declaration, check it, then hand the same reader on for one advance.
func TestAddWireAdvancesDeclaredBranches(t *testing.T) {
	ctx := context.Background()
	seq, op, clk := newSequencer(t, ctx)

	note := func(body string) ranke.Claim {
		c, err := ranke.NewClaim(ranke.TypeSource("note"), op).
			WithInlineContent([]byte(body)).
			WithEncoding(ranke.EncodingPlain).
			WithHeight(1).
			WithCreatedAt(clk.Tick()).
			Sign()
		require.NoError(t, err)
		return c
	}
	onAlpha, onBeta := note("for alpha"), note("for beta")

	var buf bytes.Buffer
	w := ranke.NewWireWriter(&buf, "alpha", "beta")
	require.NoError(t, w.WriteClaim("alpha", onAlpha))
	require.NoError(t, w.WriteClaim("beta", onBeta))

	// What a server does before accepting: learn the branches from the header alone,
	// check them against the caller's grants, then drain with the same reader.
	wr := ranke.NewWireReader(bytes.NewReader(buf.Bytes()))
	branches, err := wr.Branches()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"alpha", "beta"}, branches)

	c, err := seq.NewContribution(ctx)
	require.NoError(t, err)
	require.NoError(t, c.AddWire(ctx, wr), "the reader goes on from where the check left it")

	v, err := c.CompleteAndVerify(ctx)
	require.NoError(t, err)
	m, err := v.Persist(ctx)
	require.NoError(t, err)
	require.Len(t, m.Heads(), 2, "one head per branch the stream named")
	_, err = seq.Merge(ctx, m)
	require.NoError(t, err)

	// Both branches exist at the merged head, each reaching its own claim.
	arc, err := seq.GetArchive(ctx)
	require.NoError(t, err)
	for branch, want := range map[string]ranke.Claim{"alpha": onAlpha, "beta": onBeta} {
		br, err := arc.GetBranch(ctx, branch)
		require.NoErrorf(t, err, "branch %q exists", branch)
		ok, err := br.HasClaim(ctx, want.ID())
		require.NoError(t, err)
		require.Truef(t, ok, "branch %q holds the claim the stream named for it", branch)
	}
}

// TestAddWireAbortsOnViolation: a stream that breaks its own rules stops the fill
// and stages nothing, so a server cannot half-apply a bad contribution.
func TestAddWireAbortsOnViolation(t *testing.T) {
	ctx := context.Background()
	seq, _, _ := newSequencer(t, ctx)

	// The blob does not hash to the key it is filed under.
	hash, err := ranke.HashContent([]byte("the real bytes"))
	require.NoError(t, err)
	var buf bytes.Buffer
	w := ranke.NewWireWriter(&buf, "alpha")
	require.NoError(t, w.WriteContent(ranke.ContentBlob{Hash: hash, Content: []byte("tampered")}))

	c, err := seq.NewContribution(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, c.AddWire(ctx, ranke.NewWireReader(&buf)), ranke.ErrIntegrity,
		"AddWire surfaces the stream's own violation")

	_, err = c.CompleteAndVerify(ctx)
	require.Error(t, err, "the aborted fill staged nothing")
}
