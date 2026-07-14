package ranke

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for claim content (§4.4): the inline/external
// rule, the content hash, the size pairing, and retrieval. Content is
// either inline (bytes held on the node/edge) or external (bytes in the
// Universe, addressed by hash); the two are mutually exclusive.
//
// External retrieval needs somewhere for the bytes to live, so these
// tests use stubUniverse (below).

// stubUniverse is a throwaway test DOUBLE — not a real adapter, and not
// the adaptertest conformance suite. The distinction matters:
//
//   - adapter/storage/{mem,fs,…}   real Universe implementations.
//   - adapter/storage/adaptertest  the conformance SUITE that drives a
//     real implementation through the whole
//     Universe contract to prove it conforms.
//   - stubUniverse (here)          a fake that implements just enough of
//     the contract (content Put + Stream) to
//     back the claim atom's external-content
//     path in a package-ranke unit test.
//
// It lives in-package because a real adapter can't be imported here (they
// import ranke → cycle) — the same reason adaptertest carries its own
// throwaway memHead. The embedded Universe is nil, so any method beyond
// the two implemented below panics if called: this stub is deliberately
// NOT a Universe implementation and must never be mistaken for one.
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

// readContent drains a claim node's content reader to bytes.
func readContent(t *testing.T, n Node, u Universe) []byte {
	t.Helper()
	r, err := n.GetContent(context.Background(), u)
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	return b
}

// --- inline content -----------------------------------------------------

// TestNodeInlineContent: inline content is held on the node, addressed by
// its own hash, with the byte length recorded — and it is reachable
// without a Universe.
func TestNodeInlineContent(t *testing.T) {
	alice := identityContributor(t, "alice@example.com")
	body := []byte("From: alice\r\n\r\nI like apples.")
	c, err := NewClaim(TypeSource("email"), alice).WithInlineContent(body).Sign()
	require.NoError(t, err)

	n := c.Node()
	require.False(t, n.IsContentExternal(), "inline content is not external")
	require.Equal(t, uint64(len(body)), n.GetContentSize(), "size tracks byte length")

	wantHash, err := hashContent(body)
	require.NoError(t, err)
	require.True(t, wantHash.Equal(n.GetContentHash()), "content hash is H(content)")

	got, err := n.GetInlineContent()
	require.NoError(t, err)
	require.Equal(t, body, got, "inline bytes round-trip through the accessor")

	// Inline content needs no Universe.
	require.Equal(t, body, readContent(t, n, nil), "GetContent streams inline bytes with a nil Universe")
}

// TestEdgeInlineContent: an edge carries content under the same rule as a
// node (§4.4).
func TestEdgeInlineContent(t *testing.T) {
	alice := identityContributor(t, "alice@example.com")
	source, err := NewClaim(TypeSource("doc"), alice).WithInlineContent([]byte("src")).Sign()
	require.NoError(t, err)

	payload := []byte("edge-borne annotation")
	e, err := NewEdge(EdgeConfig{Reference: source.ID(), Type: TypeDerivation("source"), InlineContent: payload})
	require.NoError(t, err)

	require.False(t, e.IsContentExternal())
	require.Equal(t, uint64(len(payload)), e.GetContentSize())
	got, err := e.GetInlineContent()
	require.NoError(t, err)
	require.Equal(t, payload, got)

	r, err := e.GetContent(context.Background(), nil)
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, payload, b, "inline edge content streams without a Universe")
}

// --- external content ---------------------------------------------------

// TestNodeExternalContent: external content is referenced by hash; the
// bytes live in the Universe. GetInlineContent errors, GetContent with a
// nil Universe errors, and GetContent with the Universe streams the bytes.
func TestNodeExternalContent(t *testing.T) {
	alice := identityContributor(t, "alice@example.com")
	blob := []byte("a large external payload kept out of the node")
	hash, err := hashContent(blob)
	require.NoError(t, err)

	c, err := NewClaim(TypeSource("blob"), alice).
		WithExternalContent(hash, uint64(len(blob))). // hash+size, no inline bytes (XOR, §4.4)
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

	// With the blob stored, the node streams it back through the Universe.
	u := newStubUniverse()
	require.NoError(t, u.PutContents(context.Background(), []ContentBlob{{Hash: hash, Content: blob}}))
	require.Equal(t, blob, readContent(t, n, u), "external bytes stream from the Universe")
}

// TestEdgeExternalContent: the edge external path — ContentHash+ContentSize
// on EdgeConfig, mirroring WithExternalContent on the claim builder.
func TestEdgeExternalContent(t *testing.T) {
	alice := identityContributor(t, "alice@example.com")
	source, err := NewClaim(TypeSource("doc"), alice).WithInlineContent([]byte("src")).Sign()
	require.NoError(t, err)

	blob := []byte("external edge blob")
	hash, err := hashContent(blob)
	require.NoError(t, err)

	e, err := NewEdge(EdgeConfig{
		Reference:   source.ID(),
		Type:        TypeDerivation("source"),
		ContentHash: hash,
		ContentSize: uint64(len(blob)),
	})
	require.NoError(t, err)

	require.True(t, e.IsContentExternal())
	require.Equal(t, uint64(len(blob)), e.GetContentSize(), "edge records external size")
	_, err = e.GetInlineContent()
	require.ErrorIs(t, err, errContentExternal)

	u := newStubUniverse()
	require.NoError(t, u.PutContents(context.Background(), []ContentBlob{{Hash: hash, Content: blob}}))
	r, err := e.GetContent(context.Background(), u)
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, blob, b, "external edge bytes stream from the Universe")
}

// --- the mutual-exclusion rule -----------------------------------------

// TestContentXOR: a node's Content and ContentHash are mutually exclusive
// (§4.4) — supplying both is rejected at build time.
func TestContentXOR(t *testing.T) {
	alice := identityContributor(t, "alice@example.com")
	hash, err := hashContent([]byte("bytes"))
	require.NoError(t, err)

	_, err = NewClaim(TypeSource("blob"), alice).
		WithInlineContent([]byte("bytes")).
		WithExternalContent(hash, 5). // both inline and external — the builder must refuse
		Sign()
	require.ErrorIs(t, err, errClaimContentXOR, "node content XOR is enforced")

	_, err = NewEdge(EdgeConfig{
		Reference:     alice.ID(),
		Type:          TypeDerivation("source"),
		InlineContent: []byte("bytes"),
		ContentHash:   hash,
	})
	require.ErrorIs(t, err, errEdgeContentXOR, "edge content XOR is enforced")
}

// TestNoContent: a claim may carry no content at all — the content hash is
// absent, the size is zero, and the reader is empty.
func TestNoContent(t *testing.T) {
	c, err := NewClaim(NodeContributor, nil).Sign() // root contributor, no content
	require.NoError(t, err)
	n := c.Node()
	require.Nil(t, n.GetContentHash(), "no-content node has no hash")
	require.Equal(t, uint64(0), n.GetContentSize())
	require.False(t, n.IsContentExternal(), "no content is not external content")
	require.Empty(t, readContent(t, n, nil), "no-content node streams empty")
}
