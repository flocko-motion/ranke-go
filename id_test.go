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

// --- payloads that are not multihashes ---------------------------------

// TestIdRejectsNonMultihash: with the signature moved into the envelope, every id is a
// multihash (`V-ID`, `V-HASH`), so bytes of another shape are refused at construction
// rather than carried as an Id that names nothing.
func TestIdRejectsNonMultihash(t *testing.T) {
	for name, raw := range map[string][]byte{
		"arbitrary bytes":    {0xed, 0x01, 0x02, 0x03},
		"a bare multicodec":  {0xed, 0x01},
		"an ed25519 pubkey":  append([]byte{0xed, 0x01}, make([]byte, ed25519.PublicKeySize)...),
		"a truncated digest": {0x12, 0x20, 0x01, 0x02},
		"empty":              {},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := idFromBytes(raw)
			require.ErrorIs(t, err, errID, "only a multihash is an id")
		})
	}
}

// TestHashIdStillNamesSha256: the fix refuses an UNNAMED multihash, so a real one must
// still name itself.
func TestHashIdStillNamesSha256(t *testing.T) {
	h, err := HashContent([]byte("content"))
	require.NoError(t, err)
	require.Equal(t, "sha2-256", h.Algorithm())
}
