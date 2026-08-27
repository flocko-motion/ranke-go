package client

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
)

// captured is what a contribution actually put on the wire, read back through the
// library's own reader so the test judges the stream a server would see.
type captured struct {
	claims  []ranke.Claim
	content map[string][]byte
	branch  string
}

// contributeServer accepts one contribution and decodes it.
func contributeServer(t *testing.T, got *captured) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/contribute", r.URL.Path)
		require.Equal(t, "application/cbor-seq", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)

		rd := ranke.NewWireReader(bytes.NewReader(body))
		cons, err := rd.Constraints()
		require.NoError(t, err)
		require.Len(t, cons.Branches, 1)
		got.branch = cons.Branches[0]
		got.content = map[string][]byte{}
		for rd.Next() {
			rec := rd.Record()
			switch rec.Kind {
			case ranke.WireClaim:
				got.claims = append(got.claims, rec.Claim)
			case ranke.WireContent:
				got.content[rec.Blob.Hash.String()] = rec.Blob.Content
			}
		}
		require.NoError(t, rd.Err())
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"head":"bhead","ids":["b1"]}`))
	}))
}

// TestContributeSendsTheContentItsClaimsName is the regression that motivates this
// package. A claim with external content carries only the hash, so a stream of
// claims alone looks complete — the ids are right, and a re-run's dedup reads the
// hash rather than fetching what it names, so nothing downstream notices the bytes
// never arrived. Contribute takes the Universe precisely so it cannot happen.
func TestContributeSendsTheContentItsClaimsName(t *testing.T) {
	f := newFixture(t)
	blobClaim, hash := f.external(t, "the externalized bytes", epoch.Add(time.Second))
	plain := f.inline(t, "an inline note", epoch.Add(2*time.Second))

	var got captured
	srv := contributeServer(t, &got)
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	res, err := c.Contribute(context.Background(), f.u, "main", []ranke.Claim{blobClaim, plain})
	require.NoError(t, err)
	require.Equal(t, "bhead", res.Head)

	require.Len(t, got.claims, 2, "both claims travel")
	require.Equal(t, "main", got.branch)
	require.Contains(t, got.content, hash.String(), "the blob its content_hash names must travel too")
	require.Equal(t, "the externalized bytes", string(got.content[hash.String()]))
	require.Len(t, got.content, 1, "an inline claim needs no separate blob")
}

// TestContributeSendsEdgeContent: an edge carries content of its own, so sweeping
// nodes alone leaves a whole class of blob behind.
func TestContributeSendsEdgeContent(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	src := f.inline(t, "a source", epoch.Add(time.Second))
	require.NoError(t, f.u.PutClaims(ctx, []ranke.Claim{src}))

	body := []byte("an edge's externalized bytes")
	hash, err := ranke.HashContent(body)
	require.NoError(t, err)
	require.NoError(t, ranke.PutContent(ctx, f.u, hash, body))

	e, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: src.ID(), Type: ranke.TypeDerivation("source"),
		ContentHash: hash, ContentSize: uint64(len(body)),
		Encoding: ranke.EncodingOctetStream,
	})
	require.NoError(t, err)
	derived, err := ranke.NewClaim(ranke.TypeDerivation("note"), f.who).
		WithInlineContent([]byte("cites a source through an edge with content")).
		WithEncoding(ranke.EncodingPlain).
		WithEdges(e).
		WithHeight(ranke.HeightOf(f.who, src)).
		WithCreatedAt(epoch.Add(3 * time.Second)).
		Sign()
	require.NoError(t, err)

	var got captured
	srv := contributeServer(t, &got)
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	_, err = c.Contribute(ctx, f.u, "main", []ranke.Claim{derived})
	require.NoError(t, err)
	require.Contains(t, got.content, hash.String(), "an edge's content travels as a node's does")
}

// TestContributeDeduplicatesContent: two claims naming one blob send it once.
func TestContributeDeduplicatesContent(t *testing.T) {
	f := newFixture(t)
	a, hash := f.external(t, "shared bytes", epoch.Add(time.Second))
	b, hash2 := f.external(t, "shared bytes", epoch.Add(2*time.Second))
	require.True(t, hash.Equal(hash2), "same bytes, same address")

	var got captured
	srv := contributeServer(t, &got)
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	_, err = c.Contribute(context.Background(), f.u, "main", []ranke.Claim{a, b})
	require.NoError(t, err)
	require.Len(t, got.content, 1, "one blob, sent once")
}

// TestContributeRefusesUnbackedContent: a hash the Universe cannot answer for is the
// failure this package exists to surface, so it stops the contribution rather than
// sending a claim whose content is nowhere.
func TestContributeRefusesUnbackedContent(t *testing.T) {
	f := newFixture(t)
	body := "bytes that were never stored"
	hash, err := ranke.HashContent([]byte(body))
	require.NoError(t, err)
	orphan, err := ranke.NewClaim(ranke.TypeSource("blob"), f.who).
		WithExternalContent(hash, uint64(len(body))).
		WithEncoding(ranke.EncodingOctetStream).
		WithHeight(ranke.HeightOf(f.who)).
		WithCreatedAt(epoch.Add(time.Second)).
		Sign()
	require.NoError(t, err)

	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	_, err = c.Contribute(context.Background(), f.u, "main", []ranke.Claim{orphan})
	require.ErrorIs(t, err, ErrContentMissing)
	require.False(t, reached, "nothing is sent when its content cannot be")
}

// TestContributeNeedsABranch: the header settles the right on every declared branch
// before the body is read, so there is nothing to declare without one.
func TestContributeNeedsABranch(t *testing.T) {
	f := newFixture(t)
	c, err := New("http://x")
	require.NoError(t, err)
	_, err = c.Contribute(context.Background(), f.u, "", []ranke.Claim{f.inline(t, "x", epoch)})
	require.Error(t, err)
}

// TestExternalContentOrderIsStable: the sweep follows first appearance, so a stream
// built from it reproduces byte for byte.
func TestExternalContentOrderIsStable(t *testing.T) {
	f := newFixture(t)
	a, ha := f.external(t, "first", epoch.Add(time.Second))
	b, hb := f.external(t, "second", epoch.Add(2*time.Second))

	refs, err := ExternalContent([]ranke.Claim{a, b, a})
	require.NoError(t, err)
	require.Len(t, refs, 2)
	require.True(t, refs[0].Hash.Equal(ha))
	require.True(t, refs[1].Hash.Equal(hb))
}
