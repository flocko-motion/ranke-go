package neo4j

// Cross-backend agreement for compare: temporal (R-QTEMPORAL): the native lowering's
// datedMidProperty push-down must return the same order DefaultQuery derives by parsing
// EDTF at read time. Exact answers on a small fixture, not just backend agreement, per
// CLAUDE.md's Tests section.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/rankegraph/ranke-go"
	"github.com/stretchr/testify/require"
)

// datedFixture builds one contributor and four independent source/note claims — dated
// "2005", "2010/2012", "2020", and one left absent — under a hub that derivation-
// references all four, so a single Head resolves the whole closure.
func datedFixture(t *testing.T) (mem ranke.Universe, hub ranke.Id, cEarly, cMid, cLate, cAbsent ranke.Id) {
	t.Helper()
	ctx := context.Background()
	mem = ranke.NewMemoryUniverse()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)
	ctrClaim, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pubkey).WithEncoding(ranke.EncodingOctetStream).Sign(priv)
	require.NoError(t, err)
	ctr, err := ctrClaim.AsContributor(ctx, nil, priv)
	require.NoError(t, err)

	mk := func(body, dated string) ranke.Claim {
		b := ranke.NewClaim(ranke.TypeSource("note"), ctr).
			WithInlineContent([]byte(body)).WithEncoding(ranke.EncodingPlain).
			WithHeight(ranke.HeightOf(ctr))
		if dated != "" {
			b = b.WithDatedEDTF(dated)
		}
		c, err := b.Sign()
		require.NoError(t, err)
		return c
	}
	early := mk("early", "2005")
	mid := mk("mid", "2010/2012")
	late := mk("late", "2020")
	absent := mk("absent", "")

	edge := func(source ranke.Claim) ranke.Edge {
		e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: source.ID(), Type: ranke.TypeDerivation("source")})
		require.NoError(t, err)
		return e
	}
	hubClaim, err := ranke.NewClaim(ranke.TypeDerivation("summary"), ctr).
		WithInlineContent([]byte("hub")).WithEncoding(ranke.EncodingPlain).
		WithEdges(edge(early), edge(mid), edge(late), edge(absent)).
		WithHeight(ranke.HeightOf(ctr, early, mid, late, absent)).
		Sign()
	require.NoError(t, err)

	require.NoError(t, mem.PutClaims(ctx, []ranke.Claim{ctrClaim, early, mid, late, absent, hubClaim}))
	return mem, hubClaim.ID(), early.ID(), mid.ID(), late.ID(), absent.ID()
}

// orderedIDs runs q against u and Head, returning the source/note claim ids in result
// order (the hub itself carries no dated, so it is filtered out).
func orderedIDs(t *testing.T, u ranke.Universe, head ranke.Id) []ranke.Id {
	t.Helper()
	q := ranke.Query{
		Select: ranke.Select{Branch: ranke.BranchUniverse, Head: head},
		Where:  &ranke.Where{Field: "type", Test: &ranke.Comparison{Eq: "source/note"}},
		Order:  []ranke.OrderKey{{Field: "dated", Compare: ranke.CompareTemporal}},
		Output: ranke.Output{Detail: ranke.DetailID},
	}
	rs, err := u.Query(context.Background(), q, ranke.Scope{Branch: ranke.BranchUniverse})
	require.NoError(t, err)
	defer rs.Close()
	var got []ranke.Id
	for rs.Next() {
		got = append(got, rs.Result().ClaimId)
	}
	require.NoError(t, rs.Err())
	return got
}

// TestCompareTemporalAgreesWithReference: the native neo4j lowering (datedMidProperty)
// and DefaultQuery (parsing EDTF at read time) return the identical, exact order:
// earliest midpoint first, the field-absent claim last regardless of direction.
func TestCompareTemporalAgreesWithReference(t *testing.T) {
	mem, head, early, mid, late, absent := datedFixture(t)
	want := []ranke.Id{early, mid, late, absent}
	require.Equal(t, want, orderedIDs(t, mem, head), "DefaultQuery (reference)")

	u, _ := connectTestNeo4j(t)
	ctx := context.Background()
	require.NoError(t, u.CopyClaims(ctx, mem, []ranke.Id{head}, ranke.WithClosure()))
	require.Equal(t, want, orderedIDs(t, u, head), "neo4j native lowering")
}
