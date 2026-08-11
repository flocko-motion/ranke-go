package matrix_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/tests/backends"
	"github.com/flocko-motion/ranke-go/tests/generator"
	"github.com/flocko-motion/ranke-go/tests/matrix"
)

// generator.ToyDiff is the smallest archive with a content-inheriting overlay: a
// delta whose effective content is its base's. A layer may drop the bytes but
// content_size must survive, or the claim reads content-less instead of
// content-held-elsewhere.

// TestToyDiffByID reads the delta back by id (GetClaims) on every backend.
func TestToyDiffByID(t *testing.T) {
	eachBackend(t, func(t *testing.T, u ranke.Universe, want uint64, wantEnc string, delta ranke.Id) {
		got, err := ranke.GetClaim(context.Background(), u, delta)
		require.NoError(t, err)
		assertInheritedContent(t, got, want, wantEnc, "GetClaims")
	})
}

// TestToyDiffByQuery reaches the same delta through RQL. A query's meaning is
// unchanged by which layer answers it, so it must agree with TestToyDiffByID.
func TestToyDiffByQuery(t *testing.T) {
	eachBackend(t, func(t *testing.T, u ranke.Universe, want uint64, wantEnc string, delta ranke.Id) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchUniverse, Head: delta}}
		rs, err := u.Query(context.Background(), q, ranke.Scope{Branch: ranke.BranchUniverse})
		require.NoError(t, err)
		var got ranke.Claim
		for rs.Next() {
			if r := rs.Result(); r.ClaimId.String() == delta.String() {
				got = r.ClaimNative
			}
		}
		require.NoError(t, rs.Err())
		require.NoError(t, rs.Close())
		require.NotNil(t, got, "the delta was not among the query results")
		assertInheritedContent(t, got, want, wantEnc, "Query")
	})
}

// assertInheritedContent checks the delta reports its inherited content. Bytes are
// optional; the size is not — it is what marks content as existing.
func assertInheritedContent(t *testing.T, c ranke.Claim, want uint64, wantEnc, via string) {
	t.Helper()
	n := c.Node()
	require.Equalf(t, want, n.GetContentSize(),
		"via %s: the delta must report its inherited content_size (%d)", via, want)
	require.Equalf(t, wantEnc, n.Encoding(), "via %s: and its inherited encoding", via)
}

// eachBackend runs check over every row with the content the delta inherits (read off
// the base via mem) — matrix.Each, plus that expectation.
func eachBackend(t *testing.T, check func(t *testing.T, u ranke.Universe, want uint64, wantEnc string, delta ranke.Id)) {
	t.Helper()
	spec := generator.ToyDiff(1)
	want, wantEnc, _ := toyExpectation(t, spec)

	matrix.Each(t, matrix.FromSpec(spec), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		check(t, u, want, wantEnc, m.DiffChainHead)
	})
}

// toyExpectation builds the toy into mem — the reference executor — and reads the
// content the delta inherits off its base, so no size is hard-coded here.
func toyExpectation(t *testing.T, spec generator.Spec) (size uint64, encoding string, delta ranke.Id) {
	t.Helper()
	ctx := context.Background()
	u, cleanup, err := backends.Reference().Open()
	require.NoError(t, err)
	defer cleanup()

	m, err := generator.Generate(ctx, u, spec)
	require.NoError(t, err)
	c, err := ranke.GetClaim(ctx, u, m.DiffChainHead)
	require.NoError(t, err)
	n := c.Node()
	require.NotZero(t, n.GetContentSize(), "the toy's delta must inherit content from its base")
	return n.GetContentSize(), n.Encoding(), m.DiffChainHead
}
