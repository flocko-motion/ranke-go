package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Verifier coverage for the two exceptions a contribution/history claim carries
// (§Verifiability): V-TABLEREF's carve-out for its head edge, and V-ID's
// exemption for its id_seq(i,s) identity.

// TestVerifyAllowsHistoryClaimHeadEdge is the one exception `V-TABLEREF` carries
// for `V-HISTCLAIM`: a contribution/history claim's contribution/head edge may
// reach a branch table, where TestVerifyRejectsBranchTableReference shows any
// other edge may not.
func TestVerifyAllowsHistoryClaimHeadEdge(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)

	bt := branchTable(t, root, []Claim{root}, branchEdge(t, "main", root.ID()))
	require.NoError(t, g.AddClaims(context.Background(), bt))

	b, err := NewHistoryClaimBuilder(root, bt.ID(), 0, "verify-tableref-seed")
	require.NoError(t, err)
	hc, err := b.WithHeight(HeightOf(root, bt)).Sign()
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(context.Background(), hc))

	run := g.Verify()
	run.Wait()
	require.Empty(t, run.Failures(), "a history claim's head edge to a branch table must verify")
}

// TestVerifyExemptsHistoryClaimID is the one exception `V-ID` carries
// (§Verifiability): a history claim's id is id_seq(i,s), not H(S(env(v))), so
// its content hash intentionally disagrees with its id — and still verifies.
func TestVerifyExemptsHistoryClaimID(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)

	bt := branchTable(t, root, []Claim{root}, branchEdge(t, "main", root.ID()))
	require.NoError(t, g.AddClaims(context.Background(), bt))

	b, err := NewHistoryClaimBuilder(root, bt.ID(), 0, "verify-id-exempt-seed")
	require.NoError(t, err)
	hc, err := b.WithHeight(HeightOf(root, bt)).Sign()
	require.NoError(t, err)

	raw, err := hc.Envelope()
	require.NoError(t, err)
	hash, err := HashContent(raw)
	require.NoError(t, err)
	require.False(t, hash.Equal(hc.ID()), "a history claim's id is id_seq(i,s), never the envelope hash")

	require.NoError(t, g.AddClaims(context.Background(), hc))
	run := g.Verify()
	run.Wait()
	require.Empty(t, run.Failures(), "a history claim verifies despite its id not naming its content")
}
