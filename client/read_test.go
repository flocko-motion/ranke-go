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

	"github.com/rankegraph/ranke-go"
)

// seqServer answers /query with the records given, framed as contentType says.
func seqServer(t *testing.T, contentType string, records [][]byte, seen *ranke.Query) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/query", r.URL.Path)
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		if seen != nil {
			q, err := ranke.DecodeQuery(body)
			require.NoError(t, err)
			*seen = q
		}
		w.Header().Set("Content-Type", contentType)
		var out bytes.Buffer
		for _, rec := range records {
			if contentType == MediaJSONSeq {
				out.WriteByte(0x1e)
			}
			out.Write(rec)
			if contentType == MediaJSONSeq {
				out.WriteByte('\n')
			}
		}
		_, _ = w.Write(out.Bytes())
	}))
}

// TestQuerySplitsJSONSeq: RFC 7464 records are delimited by the separator alone, so
// one spanning lines still arrives whole.
func TestQuerySplitsJSONSeq(t *testing.T) {
	records := [][]byte{[]byte(`{"id":"a"}`), []byte("{\n  \"id\": \"b\"\n}")}
	srv := seqServer(t, MediaJSONSeq, records, nil)
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	got, err := c.Query(context.Background(), ranke.Query{Select: ranke.Select{Branch: "main"}})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.JSONEq(t, `{"id":"a"}`, string(got[0]))
	require.JSONEq(t, `{"id":"b"}`, string(got[1]))
}

// TestQuerySplitsCBORSeq: RFC 8742 items are self-delimiting, so the decoder's
// position after one is where the next begins.
func TestQuerySplitsCBORSeq(t *testing.T) {
	a, err := ranke.MarshalCBOR("one")
	require.NoError(t, err)
	b, err := ranke.MarshalCBOR(map[string]string{"two": "x"})
	require.NoError(t, err)

	srv := seqServer(t, MediaCBORSeq, [][]byte{a, b}, nil)
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	got, err := c.Query(context.Background(), ranke.Query{Select: ranke.Select{Branch: "main"}})
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, a, got[0])
	require.Equal(t, b, got[1])
}

// TestQueryRefusesUnknownFraming: a body whose media type names no sequence is not
// silently read as one framing or the other.
func TestQueryRefusesUnknownFraming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	_, err = c.Query(context.Background(), ranke.Query{Select: ranke.Select{Branch: "main"}})
	require.ErrorIs(t, err, ErrUnknownFraming)
}

// TestQueryClaimsAsksForTheVerifiableShaping: only original form, uninlined content
// and CBOR re-hash to the id, so QueryClaims fixes those rather than trusting what a
// caller happened to set.
func TestQueryClaimsAsksForTheVerifiableShaping(t *testing.T) {
	f := newFixture(t)
	claim := f.inline(t, "a note", epoch.Add(time.Second))
	env, err := claim.Envelope()
	require.NoError(t, err)

	var seen ranke.Query
	srv := seqServer(t, MediaCBORSeq, [][]byte{env}, &seen)
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	got, err := c.QueryClaims(context.Background(), ranke.Query{
		Select: ranke.Select{Branch: "main"},
		Output: ranke.Output{Encoding: ranke.ResultJSON, Form: ranke.FormMaterialized},
	})
	require.NoError(t, err)

	require.Equal(t, ranke.ResultCBOR, seen.Output.Encoding, "a JSON rendering does not re-hash")
	require.Equal(t, ranke.FormOriginal, seen.Output.Form)
	require.Equal(t, ranke.DetailClaims, seen.Output.Detail)

	require.Len(t, got, 1)
	require.True(t, got[0].ID().Equal(claim.ID()), "the id derives from the bytes themselves")
}

// TestGetClaimRoutesByScope: each scope has exactly one route, and a branch name is
// escaped into it.
func TestGetClaimRoutesByScope(t *testing.T) {
	f := newFixture(t)
	claim := f.inline(t, "a note", epoch.Add(time.Second))
	env, err := claim.Envelope()
	require.NoError(t, err)
	id := claim.ID().String()

	for name, tc := range map[string]struct {
		scope Scope
		want  string
	}{
		"branch":   {Scope("main"), "/branches/main/claims/" + id},
		"archive":  {ScopeArchive, "/archive/claims/" + id},
		"universe": {ScopeUniverse, "/universe/claims/" + id},
	} {
		t.Run(name, func(t *testing.T) {
			var path string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path = r.URL.Path
				w.Header().Set("Content-Type", "application/cbor")
				_, _ = w.Write(env)
			}))
			defer srv.Close()

			c, err := New(srv.URL)
			require.NoError(t, err)
			got, err := c.GetClaim(context.Background(), tc.scope, claim.ID())
			require.NoError(t, err)
			require.Equal(t, tc.want, path)
			require.True(t, got.ID().Equal(claim.ID()))
		})
	}
}

// TestGetClaimOutsideTheClosure: a claim the scope does not reach is answered as
// absent, which is the same answer as one that never existed.
func TestGetClaimOutsideTheClosure(t *testing.T) {
	f := newFixture(t)
	claim := f.inline(t, "a note", epoch.Add(time.Second))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","error":"absent"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	_, err = c.GetClaim(context.Background(), Scope("main"), claim.ID())
	require.True(t, IsNotFound(err))
}
