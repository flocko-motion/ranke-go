package ranke

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// Wire-format tests: a contribution stream is a CBOR sequence of records, so it
// round-trips claims verbatim, carries each one's branch, and proves its own blobs.

// stream writes blobs then claims (content first, so a claim citing it follows) and
// returns the encoded contribution.
func stream(t *testing.T, blobs []ContentBlob, branch string, claims ...Claim) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWireWriter(&buf)
	for _, b := range blobs {
		require.NoError(t, w.WriteContent(b))
	}
	for _, c := range claims {
		require.NoError(t, w.WriteClaim(branch, c))
	}
	return buf.Bytes()
}

// TestWireRoundTrip: both record kinds read back in order, each claim under its
// branch, with its bytes the ones its id was signed over.
func TestWireRoundTrip(t *testing.T) {
	root := contributor(t)
	a := srcClaim(t, root, "aardvark")
	blob := []byte("externalized bytes")
	hash, err := HashContent(blob)
	require.NoError(t, err)

	var claims []WireRecord
	var blobs []ContentBlob
	r := NewWireReader(bytes.NewReader(stream(t, []ContentBlob{{Hash: hash, Content: blob}}, "main", root, a)))
	for r.Next() {
		switch rec := r.Record(); rec.Kind {
		case WireClaim:
			claims = append(claims, rec)
		case WireContent:
			blobs = append(blobs, rec.Blob)
		}
	}
	require.NoError(t, r.Err())

	require.Len(t, blobs, 1)
	require.True(t, blobs[0].Hash.Equal(hash))
	require.Equal(t, blob, blobs[0].Content)

	require.Len(t, claims, 2)
	require.True(t, claims[0].Claim.ID().Equal(root.ID()), "ids survive the round trip")
	require.Equal(t, "main", claims[1].Branch, "each claim names its branch")

	// The record is the bytes the id derives from, so re-encoding what was decoded
	// reproduces them.
	want, err := a.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	got, err := claims[1].Claim.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	require.Equal(t, want, got, "the canonical record crosses the wire unchanged")
}

// TestWireSpansBranches is why the branch rides on the record: one stream carries
// claims for several branches, which a bulk contribution needs.
func TestWireSpansBranches(t *testing.T) {
	root := contributor(t)
	alpha, beta := srcClaim(t, root, "for alpha"), srcClaim(t, root, "for beta")

	var buf bytes.Buffer
	w := NewWireWriter(&buf)
	require.NoError(t, w.WriteClaim("alpha", alpha))
	require.NoError(t, w.WriteClaim("beta", beta))

	got := map[string]string{}
	r := NewWireReader(bytes.NewReader(buf.Bytes()))
	for r.Next() {
		rec := r.Record()
		got[rec.Branch] = rec.Claim.ID().String()
	}
	require.NoError(t, r.Err())
	require.Equal(t, map[string]string{
		"alpha": alpha.ID().String(),
		"beta":  beta.ID().String(),
	}, got, "one stream, two branches")
}

// TestWireClaimNeedsBranch: there is no default branch, so an unnamed one is
// refused on the way out and a record missing it is refused on the way in.
func TestWireClaimNeedsBranch(t *testing.T) {
	root := contributor(t)

	var buf bytes.Buffer
	require.ErrorIs(t, NewWireWriter(&buf).WriteClaim("", root), errWireNoBranch)

	// A claim record built without the branch element is refused when read.
	b, err := encodingMode.Marshal([]any{uint64(WireClaim), idBytes(root.ID()), []byte("x")})
	require.NoError(t, err)

	r := NewWireReader(bytes.NewReader(b))
	require.False(t, r.Next())
	require.ErrorIs(t, r.Err(), errWireNoBranch)
}

// TestWireContentProvesItself: content is addressed by its hash, so a record whose
// bytes do not hash to the key it names is refused.
func TestWireContentProvesItself(t *testing.T) {
	honest, err := HashContent([]byte("the real bytes"))
	require.NoError(t, err)

	var buf bytes.Buffer
	w := NewWireWriter(&buf)
	require.NoError(t, w.WriteContent(ContentBlob{Hash: honest, Content: []byte("tampered")}))

	r := NewWireReader(bytes.NewReader(buf.Bytes()))
	require.False(t, r.Next(), "a blob that does not match its hash stops the stream")
	require.ErrorIs(t, r.Err(), ErrIntegrity)
}

// TestWireEmptyStream: an empty stream ends without an error, so a contribution
// carrying nothing is not a failure.
func TestWireEmptyStream(t *testing.T) {
	r := NewWireReader(bytes.NewReader(nil))
	require.False(t, r.Next())
	require.NoError(t, r.Err())
}
