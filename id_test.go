package ranke

import (
	"crypto/ed25519"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the Id — the identity primitive the whole
// structure rests on: id(v) = Sign(H(S(v))) for nodes, H(S(e)) for edges
// (§4). An Id is self-describing (it names its own scheme) and
// content-addressed (same bytes → same id). Everything above — the
// Merkle-DAG, immutability, idempotent writes, verifiability — is only as
// trustworthy as these properties, so they are pinned here directly,
// below any claim/node/edge.

// --- content addressing: HashContent -----------------------------------

// TestHashContentDeterministic: identical bytes hash to an equal id,
// and the id is stable across repeated calls (idempotency, §5.4).
func TestHashContentDeterministic(t *testing.T) {
	a, err := HashContent([]byte("the same bytes"))
	require.NoError(t, err)
	b, err := HashContent([]byte("the same bytes"))
	require.NoError(t, err)
	require.True(t, a.Equal(b), "identical content must produce equal ids")
	require.Equal(t, a.String(), b.String(), "and equal string forms")
}

// TestHashContentCollisionSensitivity: a one-byte difference yields a
// different id — the content-addressing property (§5.2) the Merkle-DAG
// leans on.
func TestHashContentCollisionSensitivity(t *testing.T) {
	a, err := HashContent([]byte("hello"))
	require.NoError(t, err)
	b, err := HashContent([]byte("hellO")) // last byte differs
	require.NoError(t, err)
	require.False(t, a.Equal(b), "distinct content must produce distinct ids")
}

// TestHashContentEmpty: empty content is still addressable — a valid id,
// distinct from any non-empty content's id.
func TestHashContentEmpty(t *testing.T) {
	empty, err := HashContent([]byte{})
	require.NoError(t, err)
	require.NotEmpty(t, empty.String())

	nonEmpty, err := HashContent([]byte("x"))
	require.NoError(t, err)
	require.False(t, empty.Equal(nonEmpty), "empty and non-empty content differ")
}

// TestHashContentAlgorithm: HashContent commits to SHA2-256, and the id
// names that scheme itself (self-describing, §5.1).
func TestHashContentAlgorithm(t *testing.T) {
	id, err := HashContent([]byte("content"))
	require.NoError(t, err)
	require.Equal(t, "sha2-256", id.Algorithm())
}

// --- string form and parsing -------------------------------------------

// TestIdStringParseRoundTrip: String() and ParseId are inverses, so an id
// survives being written out and read back (the id alone recovers a
// closure, §5.9).
func TestIdStringParseRoundTrip(t *testing.T) {
	orig, err := HashContent([]byte("round-trip me"))
	require.NoError(t, err)

	parsed, err := ParseId(orig.String())
	require.NoError(t, err)
	require.True(t, orig.Equal(parsed), "ParseId(id.String()) must equal id")
	require.Equal(t, orig.String(), parsed.String())
	require.Equal(t, orig.Algorithm(), parsed.Algorithm(), "scheme survives the round-trip")
}

// TestIdStringMultibasePrefix: the string form is multibase base32, whose
// prefix byte is 'b'. Pinned so the wire form can't drift silently.
func TestIdStringMultibasePrefix(t *testing.T) {
	id, err := HashContent([]byte("prefix check"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(id.String(), "b"),
		"multibase base32 ids start with 'b', got %q", id.String())
}

// TestParseIdRejectsGarbage: a non-multibase string is rejected rather
// than yielding a bogus id.
func TestParseIdRejectsGarbage(t *testing.T) {
	_, err := ParseId("not a valid multibase id !!!")
	require.Error(t, err)
}

// --- equality ----------------------------------------------------------

// TestIdEqualReflexive: an id equals itself.
func TestIdEqualReflexive(t *testing.T) {
	id, err := HashContent([]byte("self"))
	require.NoError(t, err)
	require.True(t, id.Equal(id))
}

// TestIdEqualNil: Equal handles a nil counterpart without panicking — a
// present id never equals absence.
func TestIdEqualNil(t *testing.T) {
	id, err := HashContent([]byte("present"))
	require.NoError(t, err)
	require.False(t, id.Equal(nil), "a real id does not equal nil")
}

// TestIdEqualCrossConstruction: an id built by hashing content equals the
// same id reconstructed from its string form (ParseId) — the equality that
// matters across implementations, since ids exchanged as strings must compare
// equal to locally-computed ones. Id is sealed (unexported rawBytes), so both
// sides are the package's *id, matched by raw payload not string form.
func TestIdEqualCrossConstruction(t *testing.T) {
	original, err := HashContent([]byte("cross-impl"))
	require.NoError(t, err)

	reparsed, err := ParseId(original.String())
	require.NoError(t, err)
	require.True(t, original.Equal(reparsed), "hash and reparsed-from-string must compare equal")

	other, err := HashContent([]byte("different"))
	require.NoError(t, err)
	require.False(t, other.Equal(reparsed), "different content must not")
}

// --- Algorithm on non-hash payloads ------------------------------------

// TestAlgorithmNonMultihashPayload: Algorithm reads the leading
// multicodec varint when the payload is not a multihash (e.g. a signature
// id), and never panics on arbitrary bytes.
func TestAlgorithmNonMultihashPayload(t *testing.T) {
	// A payload whose leading varint is the ed25519-pub multicodec (0xed),
	// but which is not a valid multihash — exercises the varint branch.
	id, err := idFromBytes([]byte{0xed, 0x01, 0x02, 0x03})
	require.NoError(t, err)
	require.NotEqual(t, "unknown", id.Algorithm(),
		"a known multicodec varint should be named, got %q", id.Algorithm())
}

// TestSignatureIdNamesItsScheme: a signature id names its scheme whatever byte it opens
// with. Signatures frame under eddsa (0xd0ed, varint ed a1 03).
func TestSignatureIdNamesItsScheme(t *testing.T) {
	for _, first := range []byte{0, 1, 62, 63, 64, 255} {
		raw := make([]byte, 3+ed25519.SignatureSize)
		raw[0], raw[1], raw[2] = 0xed, 0xa1, 0x03
		raw[3] = first
		signature, err := idFromBytes(raw)
		require.NoError(t, err)
		require.Equal(t, "eddsa", signature.Algorithm(),
			"a signature names its scheme whatever byte it opens with (got first=%d)", first)
	}
}

// TestTwoBytePrefixIsNotAnUnnamedMultihash: Algorithm accepts only a NAMED multihash.
// multihash.Decode reads a code then a length, which a two-byte prefix supplies by
// accident — 0xed 0x01, then the payload's first byte as the length — so one payload in
// 256 decoded as a nameless multihash and Algorithm returned "". Any two-byte code can
// recreate it, so the guard outlives the signature framing that first hit it.
func TestTwoBytePrefixIsNotAnUnnamedMultihash(t *testing.T) {
	for _, first := range []byte{0, 1, 62, 63, 64, 255} {
		raw := make([]byte, 2+ed25519.SignatureSize)
		raw[0], raw[1] = 0xed, 0x01
		raw[2] = first
		payload, err := idFromBytes(raw)
		require.NoError(t, err)
		require.Equal(t, "ed25519-pub", payload.Algorithm(),
			"a two-byte code names itself rather than decoding as a nameless multihash (first=%d)", first)
	}
}

// TestHashIdStillNamesSha256: the fix refuses an UNNAMED multihash, so a real one must
// still name itself.
func TestHashIdStillNamesSha256(t *testing.T) {
	h, err := HashContent([]byte("content"))
	require.NoError(t, err)
	require.Equal(t, "sha2-256", h.Algorithm())
}
