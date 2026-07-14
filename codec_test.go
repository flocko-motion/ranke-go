package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the decode error paths and type/encoding
// parsing at construction. The happy-path claim codec round-trip lives in
// claim_test.go; this file pins what must be REJECTED.

// --- DecodeClaim on bad input ------------------------------------------

// TestDecodeClaimRejectsGarbage: bytes that aren't a canonical claim are
// rejected — adapters rely on this to tell claim bytes from content bytes.
func TestDecodeClaimRejectsGarbage(t *testing.T) {
	id, err := HashContent([]byte("k"))
	require.NoError(t, err)
	_, err = DecodeClaim(id, []byte("not cbor at all"))
	require.Error(t, err)
}

// TestDecodeClaimRejectsEmpty: empty input is not a claim.
func TestDecodeClaimRejectsEmpty(t *testing.T) {
	id, err := HashContent([]byte("k"))
	require.NoError(t, err)
	_, err = DecodeClaim(id, nil)
	require.Error(t, err)
}

// --- type parsing at construction --------------------------------------

// TestClaimTypeParsing: the Type must be a non-empty "class/sub" with a
// known class; malformed or unknown-class types are rejected.
func TestClaimTypeParsing(t *testing.T) {
	alice := contributor(t)
	for _, typ := range []string{
		"",        // missing
		"noslash", // no separator
		"/sub",    // empty class
		"source/", // empty sub
		"bogus/x", // unknown class
	} {
		_, err := NewClaim(typ, alice).WithInlineContent([]byte("x")).Sign()
		require.Error(t, err, "type %q must be rejected", typ)
	}
}

// TestEncodingRequiresContent: an encoding (content media type) is only
// meaningful with content; setting it on a content-less claim is rejected.
func TestEncodingRequiresContent(t *testing.T) {
	alice := contributor(t)
	_, err := NewClaim(TypeSource("note"), alice).
		WithEncoding("text/plain").
		// no content
		Sign()
	require.Error(t, err, "encoding without content must be rejected")
}

// TestEncodingOptional: encoding is optional and has NO default — a content
// claim with no encoding builds fine and reports an empty encoding, so
// binary content (e.g. a contributor's multikey pubkey) needn't carry a
// bogus media type. An explicit encoding is preserved.
func TestEncodingOptional(t *testing.T) {
	alice := contributor(t)

	none, err := NewClaim(TypeSource("note"), alice).WithInlineContent([]byte("x")).Sign()
	require.NoError(t, err)
	require.Equal(t, "", none.Node().Encoding(), "no encoding by default")

	typed, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("x")).
		WithEncoding("text/plain").
		Sign()
	require.NoError(t, err)
	require.Equal(t, "text/plain", typed.Node().Encoding(), "an explicit encoding is kept")
}
