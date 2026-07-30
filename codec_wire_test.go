package ranke

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// Wire-format tests: a contribution stream is a CBOR sequence of records, so it
// declares its branches up front, round-trips claims verbatim, and proves its blobs.

// stream writes blobs then claims (content first, so a claim citing it follows) and
// returns the encoded contribution.
func stream(t *testing.T, blobs []ContentBlob, branch string, claims ...Claim) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWireWriter(&buf, branch)
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
	w := NewWireWriter(&buf, "alpha", "beta")
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
	require.ErrorIs(t, NewWireWriter(&buf, "main").WriteClaim("", root), ErrWireNoBranch)

	// A claim record built without the branch element is refused when read.
	r := NewWireReader(bytes.NewReader(records(t,
		[]any{uint64(WireHeader), []string{"main"}},
		[]any{uint64(WireClaim), idBytes(root.ID()), []byte("x")},
	)))
	require.False(t, r.Next())
	require.ErrorIs(t, r.Err(), ErrWireNoBranch)
}

// records concatenates hand-built records, for streams a WireWriter would refuse.
func records(t *testing.T, recs ...[]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, rec := range recs {
		b, err := encodingMode.Marshal(rec)
		require.NoError(t, err)
		buf.Write(b)
	}
	return buf.Bytes()
}

// TestWireDeclaresBranchesUpFront is what the header is for: a server learns which
// branches a contribution touches from the first record, before taking the rest.
func TestWireDeclaresBranchesUpFront(t *testing.T) {
	root := contributor(t)

	var buf bytes.Buffer
	w := NewWireWriter(&buf, "alpha", "beta")
	require.NoError(t, w.WriteClaim("alpha", srcClaim(t, root, "for alpha")))

	// The tail is unreadable, so branches answered from the header alone or not at all.
	tail := append(buf.Bytes(), 0xff, 0xff, 0xff)
	branches, err := NewWireReader(bytes.NewReader(tail)).Branches()
	require.NoError(t, err, "the header decodes without the records behind it")
	require.Equal(t, []string{"alpha", "beta"}, branches)
}

// TestWireHeaderBindsTheStream: the header is worth checking only if the records
// cannot exceed it, so a claim naming an undeclared branch is refused either way.
func TestWireHeaderBindsTheStream(t *testing.T) {
	root := contributor(t)
	c := srcClaim(t, root, "smuggled")

	var buf bytes.Buffer
	require.ErrorIs(t, NewWireWriter(&buf, "alpha").WriteClaim("beta", c), ErrWireUndeclared)

	raw, err := c.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	r := NewWireReader(bytes.NewReader(records(t,
		[]any{uint64(WireHeader), []string{"alpha"}},
		[]any{uint64(WireClaim), idBytes(c.ID()), raw, "beta"},
	)))
	require.False(t, r.Next(), "a claim outside the declared branches stops the stream")
	require.ErrorIs(t, r.Err(), ErrWireUndeclared)
}

// TestWireNeedsHeader: a stream opening with anything else is refused, since a
// caller must not have to drain a contribution to learn what it touches.
func TestWireNeedsHeader(t *testing.T) {
	root := contributor(t)
	raw, err := root.EncodeCBOR(FormOriginal)
	require.NoError(t, err)

	r := NewWireReader(bytes.NewReader(records(t,
		[]any{uint64(WireClaim), idBytes(root.ID()), raw, "main"},
	)))
	require.False(t, r.Next())
	require.ErrorIs(t, r.Err(), ErrWireNoHeader)

	_, err = NewWireReader(bytes.NewReader(records(t, []any{uint64(WireContent)}))).Branches()
	require.ErrorIs(t, err, ErrWireNoHeader)
}

// TestWireContentNeedsNoBranch: content lives in the Universe unbranched, so a
// stream carrying only blobs declares no branches and is still well-formed.
func TestWireContentNeedsNoBranch(t *testing.T) {
	blob := []byte("unbranched bytes")
	hash, err := HashContent(blob)
	require.NoError(t, err)

	var buf bytes.Buffer
	require.NoError(t, NewWireWriter(&buf).WriteContent(ContentBlob{Hash: hash, Content: blob}))

	r := NewWireReader(bytes.NewReader(buf.Bytes()))
	branches, err := r.Branches()
	require.NoError(t, err)
	require.Empty(t, branches, "content touches no branch")
	require.True(t, r.Next())
	require.Equal(t, blob, r.Record().Blob.Content)
	require.False(t, r.Next())
	require.NoError(t, r.Err())
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
