package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// V-DIFF and V-DIFFEDGE checked against the code they were read from. The rules
// were written by reading computeDiffFields, computeDiffEdges and contentSource,
// so a bug there would have been written into the spec — these assert the rules as
// stated, and disagreements are reported rather than reconciled.

// diffChain stores each claim and returns the last one materialised, so its diff
// overlay is resolved the way a read presents it.
func diffChain(t *testing.T, cs ...Claim) Claim {
	t.Helper()
	ctx := context.Background()
	u := NewMemoryUniverse()
	for _, c := range cs {
		require.NoError(t, PutClaim(ctx, u, c))
	}
	got, err := GetClaim(ctx, u, cs[len(cs)-1].ID())
	require.NoError(t, err)
	return got
}

// noteWith builds a source/note carrying fields, optionally diffing over prev.
func noteWith(t *testing.T, ctr Contributor, prev Claim, body string, fields map[string]string) Claim {
	t.Helper()
	b := NewClaim(TypeSource("note"), ctr)
	if body != "" {
		b = b.WithInlineContent([]byte(body)).WithEncoding(EncodingPlain)
	}
	for k, v := range fields {
		b = b.WithField(k, v)
	}
	if prev != nil {
		b = b.WithDiff(prev.ID()).WithHeight(HeightOf(ctr, prev))
	} else {
		b = b.WithHeight(HeightOf(ctr))
	}
	c, err := b.Sign()
	require.NoError(t, err)
	return c
}

// TestDiffOmitThenRestateKeepsRestated is V-DIFF's precedence: inherit → omit →
// overlay. A name in both the omit list and the claim's own fields keeps the
// RESTATED value, because the overlay runs after the drop. Reverse the two and this
// field would read as absent.
func TestDiffOmitThenRestateKeepsRestated(t *testing.T) {
	ctr := contributor(t)
	base := noteWith(t, ctr, nil, "base", map[string]string{"colour": "red"})
	delta := noteWith(t, ctr, base, "", map[string]string{
		FieldFieldsDiffOmit: "colour",
		"colour":            "blue",
	})

	got := diffChain(t, base, delta)
	v, err := got.Node().GetField("colour")
	require.NoError(t, err, "a name both omitted and restated is present, not dropped")
	require.Equal(t, "blue", v, "the restatement wins over the omit")
}

// TestDiffOmitDropsWhenNotRestated is the control: the same omit without a
// restatement does drop the inherited value, so the precedence test above is not
// passing because omit is inert.
func TestDiffOmitDropsWhenNotRestated(t *testing.T) {
	ctr := contributor(t)
	base := noteWith(t, ctr, nil, "base", map[string]string{"colour": "red"})
	delta := noteWith(t, ctr, base, "", map[string]string{FieldFieldsDiffOmit: "colour"})

	got := diffChain(t, base, delta)
	require.False(t, got.Node().HasField("colour"), "an omitted name with no restatement is gone")
}

// TestDiffContentInheritsWholeAndCannotBeOmitted is V-DIFF's content half: content
// is inherited entire, and there is no omit for it. contentSource never reads the
// omit list, so naming content in one changes nothing.
func TestDiffContentInheritsWholeAndCannotBeOmitted(t *testing.T) {
	ctr := contributor(t)
	base := noteWith(t, ctr, nil, "the payload", nil)
	// Every plausible spelling of "drop the content", to show none is a path.
	delta := noteWith(t, ctr, base, "", map[string]string{
		FieldFieldsDiffOmit: "content\ncontent_hash\ncontent_size\nencoding",
	})

	got := diffChain(t, base, delta)
	require.Equal(t, ContentInline, got.Node().ContentKind())
	body, err := got.Node().GetInlineContent()
	require.NoError(t, err)
	require.Equal(t, "the payload", string(body), "content is inherited whole")
	require.Equal(t, EncodingPlain, got.Node().Encoding(), "and its encoding with it")
	require.Equal(t, uint64(len("the payload")), got.Node().GetContentSize())
}

// TestDiffContentInheritsAcrossTwoLinks: inheritance follows the chain, so a middle
// link stating no content of its own does not interrupt it.
func TestDiffContentInheritsAcrossTwoLinks(t *testing.T) {
	ctr := contributor(t)
	base := noteWith(t, ctr, nil, "the payload", nil)
	mid := noteWith(t, ctr, base, "", map[string]string{"step": "mid"})
	last := noteWith(t, ctr, mid, "", map[string]string{"step": "last"})

	got := diffChain(t, base, mid, last)
	body, err := got.Node().GetInlineContent()
	require.NoError(t, err)
	require.Equal(t, "the payload", string(body))
}

// TestDiffOmitEffectDoesNotInheritDownAChain is V-DIFF's third property: the omit
// list applies only where the claim itself states it. The middle link omits and
// restates "x", so "x" is present in its view and its omit list names a live field;
// the last link states no omit of its own and must keep "x".
//
// The last link is what makes this bite — with the drop reading from the inherited
// list rather than the claim's own, "x" would vanish here.
func TestDiffOmitEffectDoesNotInheritDownAChain(t *testing.T) {
	ctr := contributor(t)
	base := noteWith(t, ctr, nil, "base", map[string]string{"x": "from base"})
	mid := noteWith(t, ctr, base, "", map[string]string{
		FieldFieldsDiffOmit: "x",
		"x":                 "from mid",
	})
	last := noteWith(t, ctr, mid, "", map[string]string{"z": "from last"})

	got := diffChain(t, base, mid, last)
	v, err := got.Node().GetField("x")
	require.NoError(t, err, "the predecessor's omit list must not drop x here")
	require.Equal(t, "from mid", v)
}

// TestDiffOmitListIsItselfAnInheritedField is V-DIFF's "inherited as data" half, and
// the surprising one: a materialised claim reports an omit list it never stated. So
// reading fields_diff_omit off a materialised claim says nothing about what was
// dropped there — that is the predecessor's list, and its effect stayed behind
// (TestDiffOmitEffectDoesNotInheritDownAChain).
func TestDiffOmitListIsItselfAnInheritedField(t *testing.T) {
	ctr := contributor(t)
	base := noteWith(t, ctr, nil, "base", map[string]string{"x": "from base"})
	mid := noteWith(t, ctr, base, "", map[string]string{
		FieldFieldsDiffOmit: "x",
		"x":                 "from mid",
	})
	last := noteWith(t, ctr, mid, "", map[string]string{"z": "from last"})

	got := diffChain(t, base, mid, last)
	v, err := got.Node().GetField(FieldFieldsDiffOmit)
	require.NoError(t, err, "the omit list inherits as an ordinary field value")
	require.Equal(t, "x", v)
}

// --- V-DIFFEDGE ---

// namedEdge builds a derivation edge to ref carrying a name, so it has an identity
// to inherit under.
func namedEdge(t *testing.T, ref Claim, name string) Edge {
	t.Helper()
	e, err := NewEdge(EdgeConfig{
		Reference: ref.ID(), Type: TypeDerivation("source"),
		Fields: map[string]string{FieldName: name},
	})
	require.NoError(t, err)
	return e
}

// derivationBase builds a claim carrying one named and one unnamed derivation edge
// to two sources, plus the sources themselves.
func derivationBase(t *testing.T, ctr Contributor) (base, kept, dropped Claim) {
	t.Helper()
	kept = srcClaim(t, ctr, "reached by the named edge")
	dropped = srcClaim(t, ctr, "reached by the unnamed edge")
	c, err := NewClaim(TypeDerivation("summary"), ctr).
		WithInlineContent([]byte("base")).
		WithEncoding(EncodingPlain).
		WithEdges(namedEdge(t, kept, "keep"), mustDerivEdge(t, dropped)).
		WithHeight(HeightOf(ctr, kept, dropped)).
		Sign()
	require.NoError(t, err)
	return c, kept, dropped
}

// refs is the set of ids a claim's edges point at.
func refs(c Claim) map[string]bool {
	out := map[string]bool{}
	for _, e := range c.Edges() {
		out[e.Reference().String()] = true
	}
	return out
}

// TestDiffEdgeUnnamedDoesNotInherit is V-DIFFEDGE: a name is an edge's identity for
// inheritance, so only named edges cross the diff. The predecessor's unnamed edge is
// not inherited; its named one is.
func TestDiffEdgeUnnamedDoesNotInherit(t *testing.T) {
	ctr := contributor(t)
	base, kept, dropped := derivationBase(t, ctr)
	other := srcClaim(t, ctr, "cited by the delta itself")
	delta, err := NewClaim(TypeDerivation("summary"), ctr).
		WithDiff(base.ID()).
		WithEdges(namedEdge(t, other, "extra")).
		WithHeight(HeightOf(ctr, base, other)).
		Sign()
	require.NoError(t, err)

	got := diffChain(t, kept, dropped, other, base, delta)
	r := refs(got)
	require.True(t, r[kept.ID().String()], "the named edge inherits")
	require.False(t, r[dropped.ID().String()], "the unnamed edge does not")
	require.True(t, r[other.ID().String()], "the delta's own named edge is there")
}

// TestDiffClaimRefusesUnnamedEdge is where the other half of V-DIFFEDGE is actually
// enforced: a diff claim may not carry an unnamed edge at all, so the builder
// refuses one before computeDiffEdges is ever reached. Only the contributor and the
// diff edge itself take that path.
func TestDiffClaimRefusesUnnamedEdge(t *testing.T) {
	ctr := contributor(t)
	base, _, _ := derivationBase(t, ctr)
	other := srcClaim(t, ctr, "cited by the delta itself")

	_, err := NewClaim(TypeDerivation("summary"), ctr).
		WithDiff(base.ID()).
		WithEdges(mustDerivEdge(t, other)). // unnamed
		WithHeight(HeightOf(ctr, base, other)).
		Sign()
	require.ErrorIs(t, err, errDiffEdgeUnnamed)
}

// TestDiffEdgeOmitCannotReachTheStructuralEdges: omission deletes from the by-name
// map, so the contributor and diff edges — the only unnamed edges a diff claim may
// carry — are beyond its reach whatever the list names.
func TestDiffEdgeOmitCannotReachTheStructuralEdges(t *testing.T) {
	ctr := contributor(t)
	base, kept, _ := derivationBase(t, ctr)
	delta, err := NewClaim(TypeDerivation("summary"), ctr).
		WithDiff(base.ID()).
		WithEdges(namedEdge(t, kept, "keep")).
		WithField(FieldEdgesDiffOmit, "contributor\ndiff\nkeep").
		WithHeight(HeightOf(ctr, base, kept)).
		Sign()
	require.NoError(t, err)

	got := diffChain(t, kept, base, delta)
	r := refs(got)
	require.True(t, r[ctr.ID().String()], "the contributor edge is not omittable")
	require.True(t, r[base.ID().String()], "nor is the diff edge")
	require.True(t, r[kept.ID().String()], "and a restated name outlives its own omit")
}

// TestDiffEdgeNamedOverlayReplacesByName: the overlay is keyed by name, so restating
// a name replaces the inherited edge rather than adding beside it.
func TestDiffEdgeNamedOverlayReplacesByName(t *testing.T) {
	ctr := contributor(t)
	base, kept, _ := derivationBase(t, ctr)
	replacement := srcClaim(t, ctr, "the replacement target")
	delta, err := NewClaim(TypeDerivation("summary"), ctr).
		WithDiff(base.ID()).
		WithEdges(namedEdge(t, replacement, "keep")).
		WithHeight(HeightOf(ctr, base, replacement)).
		Sign()
	require.NoError(t, err)

	got := diffChain(t, kept, replacement, base, delta)
	r := refs(got)
	require.True(t, r[replacement.ID().String()], "the restated name points at the new target")
	require.False(t, r[kept.ID().String()], "and the inherited edge of that name is replaced")
}
