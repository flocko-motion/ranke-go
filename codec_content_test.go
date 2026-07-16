package ranke

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestInlineContentInID: inline content is committed by the claim id (§Content),
// so two claims identical but for a single content byte have different ids —
// and an inline claim carries no content_hash.
func TestInlineContentInID(t *testing.T) {
	alice := contributor(t)
	at := time.Unix(1, 0).UTC() // same timestamp both builds → only content differs
	build := func(body []byte) Claim {
		c, err := NewClaim(TypeSource("note"), alice).
			WithInlineContent(body).
			WithEncoding(EncodingPlain).
			WithCreatedAt(at).
			WithHeight(HeightOf(alice)).
			Sign()
		require.NoError(t, err)
		return c
	}
	a := build([]byte("the quick brown fox"))
	b := build([]byte("the quick brown fpx")) // one byte differs
	require.False(t, a.ID().Equal(b.ID()), "a one-byte content change must change the id")
	require.Nil(t, a.Node().GetContentHash(), "inline content carries no content_hash")
}

// TestInlinePreimageConsistency: the bytes the id is signed over (encodeNode)
// equal the node preimage verification extracts from the stored claim, so an
// inline-content claim round-trips and verifies against its id.
func TestInlinePreimageConsistency(t *testing.T) {
	alice := contributor(t)
	c, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("hello world")).
		WithEncoding(EncodingPlain).
		WithCreatedAt(time.Unix(1, 0).UTC()).
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)

	raw, err := c.Encode()
	require.NoError(t, err)
	pre, err := nodePreimage(raw)
	require.NoError(t, err)
	enc, err := encodeNode(c.unwrap().node, c.unwrap().edges)
	require.NoError(t, err)
	require.True(t, bytes.Equal(pre, enc), "nodePreimage(Encode) must equal encodeNode (the signed preimage)")
}
