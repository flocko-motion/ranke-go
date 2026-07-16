package ranke

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the Node — the structural component of a claim
// (§4.1) — focused on its content surface (§4.4): the inline/external rule,
// the content hash, the size pairing, and retrieval. Other node accessors
// (type, encoding, fields, title, pubkey) are exercised through the claim
// codec round-trip in claim_test.go.

// stubUniverse is a throwaway test DOUBLE — not a real adapter, and not the
// adaptertest conformance suite:
//
//   - adapter/storage/{mem,fs,…}   real Universe implementations.
//   - adapter/storage/adaptertest  the conformance SUITE that drives a real
//     implementation through the whole Universe contract to prove it conforms.
//   - stubUniverse (here)          a fake implementing just enough (content
//     Put + Stream) to back the external-content path in a package-ranke
//     unit test.
//
// It lives in-package because a real adapter can't be imported here (they
// import ranke → cycle) — the same reason adaptertest carries its own
// throwaway memHead. The embedded Universe is nil, so any method beyond the
// two below panics if called: this stub is deliberately NOT a Universe.
// Shared with edge_test.go (same package).
type stubUniverse struct {
	Universe
	content map[string][]byte
}

func newStubUniverse() *stubUniverse {
	return &stubUniverse{content: map[string][]byte{}}
}

func (u *stubUniverse) PutContents(_ context.Context, blobs []ContentBlob) error {
	for _, b := range blobs {
		u.content[b.Hash.String()] = b.Content
	}
	return nil
}

func (u *stubUniverse) StreamContent(_ context.Context, hash Id, _ uint64) (io.ReadCloser, error) {
	b, ok := u.content[hash.String()]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// readContent drains a node's content reader to bytes.
func readContent(t *testing.T, n Node, u Universe) []byte {
	t.Helper()
	r, err := n.GetContent(context.Background(), u)
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return b
}

// TestNodeInlineContent: inline content is held on the node, addressed by
// its own hash, with the byte length recorded — reachable without a Universe.
func TestNodeInlineContent(t *testing.T) {
	alice := contributor(t)
	body := []byte("From: alice\r\n\r\nI like apples.")
	c, err := NewClaim(TypeSource("email"), alice).WithInlineContent(body).WithEncoding(EncodingMessage("rfc822")).WithHeight(HeightOf(alice)).Sign()
	require.NoError(t, err)

	n := c.Node()
	require.False(t, n.IsContentExternal(), "inline content is not external")
	require.Equal(t, uint64(len(body)), n.GetContentSize(), "size tracks byte length")

	require.Nil(t, n.GetContentHash(), "inline content has no content_hash (§Content) — the id commits to the bytes")

	got, err := n.GetInlineContent()
	require.NoError(t, err)
	require.Equal(t, body, got, "inline bytes round-trip through the accessor")

	require.Equal(t, body, readContent(t, n, nil), "GetContent streams inline bytes with a nil Universe")
}

// TestNodeExternalContent: external content is referenced by hash; the bytes
// live in the Universe. GetInlineContent errors, GetContent with a nil
// Universe errors, and GetContent with the Universe streams the bytes.
func TestNodeExternalContent(t *testing.T) {
	alice := contributor(t)
	blob := []byte("a large external payload kept out of the node")
	hash, err := hashContent(blob)
	require.NoError(t, err)

	c, err := NewClaim(TypeSource("blob"), alice).
		WithExternalContent(hash, uint64(len(blob))). // hash+size, no inline bytes (XOR, §4.4)
		WithEncoding(EncodingOctetStream).
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)

	n := c.Node()
	require.True(t, n.IsContentExternal(), "content referenced by hash is external")
	require.True(t, hash.Equal(n.GetContentHash()), "content hash is preserved")
	require.Equal(t, uint64(len(blob)), n.GetContentSize(), "external size is recorded on the node")

	_, err = n.GetInlineContent()
	require.ErrorIs(t, err, errContentExternal, "inline accessor rejects external content")
	_, err = n.GetContent(context.Background(), nil)
	require.ErrorIs(t, err, errNoUniverseForContent, "external content needs a Universe")

	u := newStubUniverse()
	require.NoError(t, u.PutContents(context.Background(), []ContentBlob{{Hash: hash, Content: blob}}))
	require.Equal(t, blob, readContent(t, n, u), "external bytes stream from the Universe")
}

// TestNodeContentXOR: a node's inline content and external hash are mutually
// exclusive (§4.4) — supplying both is rejected at build time.
func TestNodeContentXOR(t *testing.T) {
	alice := contributor(t)
	hash, err := hashContent([]byte("bytes"))
	require.NoError(t, err)

	_, err = NewClaim(TypeSource("blob"), alice).
		WithInlineContent([]byte("bytes")).
		WithExternalContent(hash, 5). // both inline and external — the builder must refuse
		WithHeight(HeightOf(alice)).
		Sign()
	require.ErrorIs(t, err, errClaimContentXOR, "node content XOR is enforced")
}

// TestNodeNoContent: a node may carry no content at all — no hash, zero
// size, empty reader.
func TestNodeNoContent(t *testing.T) {
	c, err := NewClaim(NodeContributor, nil).Sign() // root contributor, no content
	require.NoError(t, err)
	n := c.Node()
	require.Nil(t, n.GetContentHash(), "no-content node has no hash")
	require.Equal(t, uint64(0), n.GetContentSize())
	require.False(t, n.IsContentExternal(), "no content is not external content")
	require.Empty(t, readContent(t, n, nil), "no-content node streams empty")
}

// --- height (§4.1) ------------------------------------------------------

// TestNodeHeightInitialIsZero: an initial node (a root contributor, no edges)
// is height 0 automatically.
func TestNodeHeightInitialIsZero(t *testing.T) {
	root := contributor(t)
	require.Equal(t, uint64(0), root.Node().Height(), "an initial node is height 0")
}

// TestNodeHeightIncrementsDownChain: height is 1 + max(reference heights), so
// it grows by one along a derivation chain root(0) ← a(1) ← b(2).
func TestNodeHeightIncrementsDownChain(t *testing.T) {
	root := contributor(t)
	a := srcClaim(t, root, "a")
	b := entityClaim(t, root, "person", "b", a)
	require.Equal(t, uint64(0), root.Node().Height())
	require.Equal(t, uint64(1), a.Node().Height(), "source over the height-0 contributor")
	require.Equal(t, uint64(2), b.Node().Height(), "entity over the height-1 source")
}

// TestEncodingConstructors covers the media-type constructor family: each
// builds the canonical "<top-level>/<sub>" string for its IANA type.
func TestEncodingConstructors(t *testing.T) {
	cases := []struct {
		got  string
		want string
	}{
		{EncodingApplication("cbor"), "application/cbor"},
		{EncodingAudio("mpeg"), "audio/mpeg"},
		{EncodingExample("sample"), "example/sample"},
		{EncodingFont("woff2"), "font/woff2"},
		{EncodingImage("png"), "image/png"},
		{EncodingMessage("rfc822"), "message/rfc822"},
		{EncodingModel("gltf-binary"), "model/gltf-binary"},
		{EncodingMultipart("mixed"), "multipart/mixed"},
		{EncodingText("plain"), "text/plain"},
		{EncodingVideo("mp4"), "video/mp4"},
	}
	for _, c := range cases {
		require.Equal(t, c.want, c.got)
	}
}
