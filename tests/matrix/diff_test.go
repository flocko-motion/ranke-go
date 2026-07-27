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
	"github.com/flocko-motion/ranke-go/generator"
	"github.com/flocko-motion/ranke-go/tests/backends"
)

// The toy archive: the smallest graph that contains a diff overlay.
//
//	contributor             an initial node carrying its own pubkey
//	S  source/note          the artifact the summaries cite
//	A  derivation/summary   inline text content, "the base text"
//	B  derivation/summary   contribution/diff -> A, restating one field only
//
// plus the branch table the Sequencer mints to advance "main". S is not
// decoration: a derivation/* claim must carry at least one derivation/* edge
// (the §3.5 provenance invariant), so the base needs something to cite.
//
// B carries no content of its own, so its effective content is A's — that is
// what a delta means. Per §Content a storage layer may drop the content BYTES
// (they are served from a durable tier), but content_size must survive, so the
// gap is visible as content-held-elsewhere rather than content-less. These
// tests assert exactly that, on every backend, through both read paths.
const (
	toyBaseText = "the base text"
	toyEncoding = "text/plain"
)

// TestToyDiffByID reads the delta back by id (GetClaims) on every backend.
func TestToyDiffByID(t *testing.T) {
	eachBackend(t, func(t *testing.T, u ranke.Universe, _ ranke.Id, delta ranke.Id) {
		got, err := ranke.GetClaim(context.Background(), u, delta)
		require.NoError(t, err)
		assertInheritedContent(t, got, "GetClaims")
	})
}

// TestToyDiffByQuery reaches the same delta through RQL (Query) on every
// backend. A query's meaning is fixed by the ADT and unchanged by which layer
// answers it (universe.go, Query), so this must agree with TestToyDiffByID —
// the same claim, reached two ways.
func TestToyDiffByQuery(t *testing.T) {
	eachBackend(t, func(t *testing.T, u ranke.Universe, _ ranke.Id, delta ranke.Id) {
		q := ranke.Query{Select: ranke.Select{Branch: ranke.BranchUniverse, Claim: delta}}
		rs, err := u.Query(context.Background(), q, ranke.Scope{Branch: ranke.BranchUniverse})
		require.NoError(t, err)
		var got ranke.Claim
		for rs.Next() {
			if r := rs.Result(); r.Id.String() == delta.String() {
				got = r.Claim
			}
		}
		require.NoError(t, rs.Err())
		require.NoError(t, rs.Close())
		require.NotNil(t, got, "the delta was not among the query results")
		assertInheritedContent(t, got, "Query")
	})
}

// assertInheritedContent checks the delta reports the content it inherits from
// its predecessor. The bytes themselves are optional — a layer may hold none —
// but the size must be there, because that is what marks the content as existing
// and stored elsewhere rather than absent.
func assertInheritedContent(t *testing.T, c ranke.Claim, via string) {
	t.Helper()
	n := c.Node()
	require.Equalf(t, uint64(len(toyBaseText)), n.GetContentSize(),
		"via %s: the delta must report its inherited content_size (%d). A layer may drop the "+
			"content bytes, but dropping content_size makes the claim read as content-LESS "+
			"instead of content-held-elsewhere", via, len(toyBaseText))
	require.Equalf(t, toyEncoding, n.Encoding(),
		"via %s: the delta must report its inherited encoding", via)
}

// eachBackend builds the toy archive into every backend that can run here and
// runs check against it. Rows are opened the same way the matrix opens them.
func eachBackend(t *testing.T, check func(t *testing.T, u ranke.Universe, base, delta ranke.Id)) {
	t.Helper()
	for _, row := range backends.All() {
		t.Run(row.Name, func(t *testing.T) {
			u, cleanup, err := row.Open()
			if errors.Is(err, backends.ErrUnavailable) {
				t.Skipf("unavailable here: %v", err)
			}
			require.NoError(t, err)
			defer cleanup()

			base, delta := buildToyArchive(t, u)
			check(t, u, base, delta)
		})
	}
}

// buildToyArchive writes the three-claim archive into u via the reference dev
// Sequencer and returns the base and delta ids. Deterministic: a fixed key and
// a fixed clock, so both ids are identical in every backend.
func buildToyArchive(t *testing.T, u ranke.Universe) (base, delta ranke.Id) {
	t.Helper()
	ctx := context.Background()
	clock := generator.NewClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), time.Second)

	priv := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)
	opClaim, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pubkey).
		WithEncoding(ranke.EncodingOctetStream).
		WithField(ranke.FieldName, "toy").
		WithCreatedAt(clock.Tick()).
		Sign(priv)
	require.NoError(t, err)
	op, err := opClaim.AsContributor(ctx, nil, priv)
	require.NoError(t, err)

	seq, err := devseq.NewSequencer(ctx, u, devhist.New(clock), op, clock)
	require.NoError(t, err)

	// S: the artifact the summaries derive from.
	s, err := ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte("the source artifact")).
		WithEncoding(ranke.EncodingPlain).
		WithCreatedAt(clock.Tick()).
		WithHeight(ranke.HeightOf(op)).
		Sign()
	require.NoError(t, err)

	// A: the base — carries the content the delta will inherit.
	srcEdge, err := ranke.NewEdge(ranke.EdgeConfig{Reference: s.ID(), Type: ranke.TypeDerivation("source")})
	require.NoError(t, err)
	a, err := ranke.NewClaim(ranke.TypeDerivation("summary"), op).
		WithInlineContent([]byte(toyBaseText)).
		WithEncoding(ranke.EncodingPlain).
		WithEdges(srcEdge).
		WithCreatedAt(clock.Tick()).
		WithHeight(ranke.HeightOf(op, s)).
		Sign()
	require.NoError(t, err)

	// B: the delta — restates one field and re-affirms its provenance, nothing
	// else. No content and no encoding: both are inherited from A through the
	// contribution/diff edge. The provenance edge is NAMED, so it stays one edge
	// through the chain rather than accumulating a second.
	reEdge, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: s.ID(),
		Type:      ranke.TypeDerivation("source"),
		Fields:    map[string]string{ranke.FieldName: "source"},
	})
	require.NoError(t, err)
	b, err := ranke.NewClaim(ranke.TypeDerivation("summary"), op).
		WithDiff(a.ID()).
		WithEdges(reEdge).
		WithField("rev", "1").
		WithCreatedAt(clock.Tick()).
		WithHeight(ranke.HeightOf(op, a, s)).
		Sign()
	require.NoError(t, err)

	_, err = seq.AddClaims(ctx, []ranke.Claim{s, a, b})
	require.NoError(t, err)
	return a.ID(), b.ID()
}
