package client

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
)

// envelopes renders claims as the stored bytes a cbor read returns.
func envelopes(t *testing.T, claims ...ranke.Claim) [][]byte {
	t.Helper()
	out := make([][]byte, 0, len(claims))
	for _, c := range claims {
		env, err := c.Envelope()
		require.NoError(t, err)
		out = append(out, env)
	}
	return out
}

// tagged mints a source claim carrying field=value, which is what a find matches on.
func (f *fixture) tagged(t *testing.T, field, value string, at time.Time) ranke.Claim {
	t.Helper()
	c, err := ranke.NewClaim(ranke.TypeSource("note"), f.who).
		WithInlineContent([]byte(value)).
		WithEncoding(ranke.EncodingPlain).
		WithField(field, value).
		WithHeight(ranke.HeightOf(f.who)).
		WithCreatedAt(at).
		Sign()
	require.NoError(t, err)
	return c
}

// TestFindOneReusesTheClaimItFinds is the read half of find-or-create: found means
// the caller mints nothing and reuses the id and height already in the archive.
func TestFindOneReusesTheClaimItFinds(t *testing.T) {
	f := newFixture(t)
	want := f.tagged(t, "path", "src/main.go", epoch.Add(time.Second))

	var seen ranke.Query
	srv := seqServer(t, MediaCBORSeq, envelopes(t, want), &seen)
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	got, found, err := c.FindOne(context.Background(),
		Lookup{Branch: "main", Types: []string{"source/note"}, Field: "path"}, "src/main.go")
	require.NoError(t, err)
	require.True(t, found)
	require.True(t, got.ID().Equal(want.ID()))
	require.Equal(t, "main", seen.Select.Branch)
	require.NotNil(t, seen.Where, "the type and the field are both tested")
}

// TestFindOneAbsentLeavesTheMintingToTheCaller: nothing found is not an error, since
// the caller's next step is to build the claim.
func TestFindOneAbsentLeavesTheMintingToTheCaller(t *testing.T) {
	srv := seqServer(t, MediaCBORSeq, nil, nil)
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	got, found, err := c.FindOne(context.Background(),
		Lookup{Branch: "main", Types: []string{"source/note"}, Field: "path"}, "nope")
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, got)
}

// TestFindOneRefusesTwoMatches: returning either would make which id a re-run reuses
// depend on result order, so the ambiguity is reported instead of resolved.
func TestFindOneRefusesTwoMatches(t *testing.T) {
	f := newFixture(t)
	a := f.tagged(t, "path", "dup", epoch.Add(time.Second))
	b := f.tagged(t, "path", "dup", epoch.Add(2*time.Second))

	var seen ranke.Query
	srv := seqServer(t, MediaCBORSeq, envelopes(t, a, b), &seen)
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	_, _, err = c.FindOne(context.Background(),
		Lookup{Branch: "main", Types: []string{"source/note"}, Field: "path"}, "dup")
	require.ErrorIs(t, err, ErrAmbiguous)
	require.Equal(t, 2, seen.Limit.Results, "one past the wanted count, so a second match is visible")
}

// TestFindOneNeedsATypeAndAField: without both it would read the branch and match on
// nothing.
func TestFindOneNeedsATypeAndAField(t *testing.T) {
	c, err := New("http://x")
	require.NoError(t, err)
	_, _, err = c.FindOne(context.Background(), Lookup{Branch: "main"}, "v")
	require.ErrorIs(t, err, ErrIncompleteLookup)
}

// TestFindByFieldKeysWhatItFinds: the bulk form pays for one read where a run would
// otherwise pay per value, and a claim without the field answers no key.
func TestFindByFieldKeysWhatItFinds(t *testing.T) {
	f := newFixture(t)
	a := f.tagged(t, "content_hash", "h1", epoch.Add(time.Second))
	b := f.tagged(t, "content_hash", "h2", epoch.Add(2*time.Second))
	bare := f.inline(t, "no field at all", epoch.Add(3*time.Second))

	var seen ranke.Query
	srv := seqServer(t, MediaCBORSeq, envelopes(t, a, b, bare), &seen)
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	got, err := c.FindByField(context.Background(), Lookup{
		Branch: "main", Types: []string{"source/note", "source/blob"}, Field: "content_hash",
	})
	require.NoError(t, err)
	require.Len(t, got, 2, "the claim carrying no such field keys nothing")
	require.True(t, got["h1"][0].ID().Equal(a.ID()))
	require.True(t, got["h2"][0].ID().Equal(b.ID()))
}

// TestFindByFieldShowsCollisions: several claims under one value all appear, so a
// caller wanting uniqueness sees the collision rather than a chosen winner.
func TestFindByFieldShowsCollisions(t *testing.T) {
	f := newFixture(t)
	a := f.tagged(t, "k", "same", epoch.Add(time.Second))
	b := f.tagged(t, "k", "same", epoch.Add(2*time.Second))

	srv := seqServer(t, MediaCBORSeq, envelopes(t, a, b), nil)
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	got, err := c.FindByField(context.Background(),
		Lookup{Branch: "main", Types: []string{"source/note"}, Field: "k"})
	require.NoError(t, err)
	require.Len(t, got["same"], 2)
}

// TestDevClockAdvances: the dev route moves the clock and reports where it stands.
func TestDevClockAdvances(t *testing.T) {
	at := epoch.Add(time.Hour)
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/dev/clock", r.URL.Path)
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"time":"` + at.Format(time.RFC3339Nano) + `"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	got, err := c.Dev().AdvanceClock(context.Background(), at)
	require.NoError(t, err)
	require.True(t, got.Available)
	require.True(t, got.Time.Equal(at))
	require.Contains(t, string(body), at.Format(time.RFC3339Nano))
}

// TestDevClockAbsentIsNotAFailure: a production stack mounts no dev routes and
// answers 501, which a caller running against both kinds should not have to branch on.
func TestDevClockAbsentIsNotAFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"code":"unimplemented","error":"dev routes are not mounted"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	got, err := c.Dev().AdvanceClock(context.Background(), epoch)
	require.NoError(t, err)
	require.False(t, got.Available)
}

// TestMaxCreatedAtIsTheBatchsOwnLatest: the dev clock must reach a batch's latest
// created_at before it lands, and only the batch knows where that is.
func TestMaxCreatedAtIsTheBatchsOwnLatest(t *testing.T) {
	f := newFixture(t)
	a := f.inline(t, "first", epoch.Add(time.Second))
	b := f.inline(t, "last", epoch.Add(9*time.Second))

	require.True(t, MaxCreatedAt([]ranke.Claim{a, b}).Equal(epoch.Add(9*time.Second)))
	require.True(t, MaxCreatedAt(nil).IsZero())
}

// TestAdvanceClockPastReachesTheBatch: the convenience form a dev contribution uses.
func TestAdvanceClockPastReachesTheBatch(t *testing.T) {
	f := newFixture(t)
	last := epoch.Add(9 * time.Second)
	claims := []ranke.Claim{f.inline(t, "first", epoch.Add(time.Second)), f.inline(t, "last", last)}

	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"time":"` + last.Format(time.RFC3339Nano) + `"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	got, err := c.Dev().AdvanceClockPast(context.Background(), claims)
	require.NoError(t, err)
	require.True(t, got.Time.Equal(last))
	require.Contains(t, string(body), last.Format(time.RFC3339Nano))
}
