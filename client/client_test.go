package client

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
)

// epoch fixes every timestamp, so a fixture reproduces.
var epoch = time.Unix(1700000000, 0).UTC()

// fixture is a memory Universe with a contributor to sign against, which is the
// smallest thing a contribution needs.
type fixture struct {
	u   ranke.Universe
	who ranke.Contributor
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	h := sha256.Sum256([]byte("ranke-go/client test"))
	priv := ed25519.NewKeyFromSeed(h[:])
	pub, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err)

	root, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pub).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(epoch).
		Sign(priv)
	require.NoError(t, err)

	u := ranke.NewMemoryUniverse()
	require.NoError(t, u.PutClaims(ctx, []ranke.Claim{root}))
	who, err := root.AsContributor(ctx, u, priv)
	require.NoError(t, err)
	return &fixture{u: u, who: who}
}

// inline mints a source claim carrying its content in the record.
func (f *fixture) inline(t *testing.T, body string, at time.Time) ranke.Claim {
	t.Helper()
	c, err := ranke.NewClaim(ranke.TypeSource("note"), f.who).
		WithInlineContent([]byte(body)).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(ranke.HeightOf(f.who)).
		WithCreatedAt(at).
		Sign()
	require.NoError(t, err)
	return c
}

// external mints a source claim addressing content by hash, and stores those bytes
// in the Universe — the arrangement whose blob used to be left behind.
func (f *fixture) external(t *testing.T, body string, at time.Time) (ranke.Claim, ranke.Id) {
	t.Helper()
	hash, err := ranke.HashContent([]byte(body))
	require.NoError(t, err)
	require.NoError(t, ranke.PutContent(context.Background(), f.u, hash, []byte(body)))
	c, err := ranke.NewClaim(ranke.TypeSource("blob"), f.who).
		WithExternalContent(hash, uint64(len(body))).
		WithEncoding(ranke.EncodingOctetStream).
		WithHeight(ranke.HeightOf(f.who)).
		WithCreatedAt(at).
		Sign()
	require.NoError(t, err)
	return c, hash
}

// TestCredentialsAreExclusive: the endpoint routes on the scheme presented, so two
// credentials name two adapters and the request has no single answer.
func TestCredentialsAreExclusive(t *testing.T) {
	_, err := New("http://x", WithToken("t"), WithAPIKey("k"))
	require.ErrorIs(t, err, ErrCredentials)

	_, err = New("")
	require.ErrorIs(t, err, ErrNoBaseURL)
}

// TestCredentialPresentation: each credential rides in its own header, and neither
// appears when the other was given.
func TestCredentialPresentation(t *testing.T) {
	for name, tc := range map[string]struct {
		opt        Option
		wantAuth   string
		wantAPIKey string
	}{
		"token":  {WithToken("jwt"), "Bearer jwt", ""},
		"apikey": {WithAPIKey("k"), "", "k"},
	} {
		t.Run(name, func(t *testing.T) {
			var gotAuth, gotKey string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth, gotKey = r.Header.Get("Authorization"), r.Header.Get("X-API-Key")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			}))
			defer srv.Close()

			c, err := New(srv.URL, tc.opt)
			require.NoError(t, err)
			_, err = c.Health(context.Background())
			require.NoError(t, err)
			require.Equal(t, tc.wantAuth, gotAuth)
			require.Equal(t, tc.wantAPIKey, gotKey)
		})
	}
}

// TestWithHTTPClientIsUsed: the transport is the caller's, for timeouts, proxies or
// a test server standing in for the stack.
func TestWithHTTPClientIsUsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","signer":"did:key:zAbc"}`))
	}))
	defer srv.Close()

	own := &http.Client{Timeout: 2 * time.Second}
	c, err := New(srv.URL, WithHTTPClient(own))
	require.NoError(t, err)
	require.Same(t, own, c.http)

	h, err := c.Health(context.Background())
	require.NoError(t, err)
	require.Equal(t, "did:key:zAbc", h.Signer)
}

// TestErrorCarriesTheServersCategory: a caller branches on the stable code, not on
// the message or the status.
func TestErrorCarriesTheServersCategory(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","error":"no such branch"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	_, err = c.Health(context.Background())
	require.True(t, IsNotFound(err), "got %v", err)
	require.Contains(t, err.Error(), "no such branch")
}

// TestWaitReadyPollsUntilServing: the point is to wait for the fact rather than for
// a guessed interval, so a stack that answers on the third try is waited out.
func TestWaitReadyPollsUntilServing(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	require.NoError(t, c.WaitReady(context.Background(), 5*time.Second))
	require.GreaterOrEqual(t, calls, 3)
}

// TestWaitReadyGivesUp returns the last failure rather than blocking forever.
func TestWaitReadyGivesUp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c, err := New(srv.URL)
	require.NoError(t, err)
	require.Error(t, c.WaitReady(context.Background(), 120*time.Millisecond))
}
