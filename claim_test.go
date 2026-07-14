package ranke

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the atom of Ranke: the claim (§4.3 — a node
// together with the edges in its edges set, created atomically, immutable
// after).
//
// The centrepiece is a codec round-trip: a claim serialized to its
// canonical storage bytes and decoded back must reproduce those bytes
// exactly AND every field, content included — an archive loses nothing
// (D2 immutability, D5 verifiability). Because the encoding is CBOR
// Deterministic (§5.1), byte-identity of a re-encode is the strongest
// single witness that every field survived; each claim shape below is
// pushed through the round-trip and then compared field-by-field.
//
// The planned harness is claim → cbor → claim → json → claim → cbor →
// claim, comparing the initial to the final claim and the first cbor to
// the second for identity. Only the CBOR leg exists today; roundTrip is
// structured so the JSON leg slots in unchanged once the JSON codec lands
// (see the TODO in roundTrip).
//
// Scope (agreed): the claim atom — construction, encoding/decoding, and
// content (inline / external / none). NOT here: Graph, Archive, and
// content-placement POLICY. Whether to store a blob inline or external is
// an application-layer decision ("the Ranke-Graph documents; it does not
// decide"), so the auto/threshold + manual-override tests sketched in the
// original file live at a higher test layer, not in this ADT unit suite.

// --- round-trip harness -------------------------------------------------

// roundTrip pushes c through the canonical codec and back, asserting the
// decoded claim re-encodes to byte-identical bytes and is structurally
// equal to c. Returns the decoded claim so callers can chain further
// assertions on the far side of the codec.
func roundTrip(t *testing.T, c Claim) Claim {
	t.Helper()

	// claim → cbor
	encoded, err := c.Encode()
	require.NoError(t, err, "Encode")

	// cbor → claim
	decoded, err := DecodeClaim(c.ID(), encoded)
	require.NoError(t, err, "DecodeClaim")

	// claim → cbor again — canonical CBOR is deterministic, so a faithful
	// decode must reproduce the exact input bytes.
	reencoded, err := decoded.Encode()
	require.NoError(t, err, "re-Encode")
	require.Equal(t, encoded, reencoded,
		"canonical re-encode must be byte-identical to the original bytes")

	// TODO(json): once a JSON codec exists, extend the round-trip to
	// claim → json → claim → cbor and assert byte-identity across that
	// leg too. Add it here so every case below exercises it for free.

	require.True(t, c.ID().Equal(decoded.ID()), "id preserved across codec")
	assertClaimEqual(t, c, decoded)
	return decoded
}

// assertClaimEqual compares two claims field-by-field through the public
// accessor surface: the node, then the edge set in canonical order.
func assertClaimEqual(t *testing.T, want, got Claim) {
	t.Helper()
	assertNodeEqual(t, want.Node(), got.Node())

	we, ge := want.Edges(), got.Edges()
	require.Len(t, ge, len(we), "edge count")
	for i := range we {
		assertEdgeEqual(t, we[i], ge[i])
	}
}

func assertNodeEqual(t *testing.T, want, got Node) {
	t.Helper()
	require.Equal(t, want.Type(), got.Type(), "node type")
	require.Equal(t, want.Encoding(), got.Encoding(), "node encoding")
	require.True(t, want.CreatedAt().Equal(got.CreatedAt()),
		"node created_at: want %s got %s", want.CreatedAt(), got.CreatedAt())
	assertContentEqual(t, want, got, "node")
	require.Equal(t, want.Fields(), got.Fields(), "node field names")
	for _, name := range want.Fields() {
		wv, err := want.GetField(name)
		require.NoError(t, err)
		gv, err := got.GetField(name)
		require.NoError(t, err)
		require.Equal(t, wv, gv, "node field %q", name)
	}
}

func assertEdgeEqual(t *testing.T, want, got Edge) {
	t.Helper()
	require.True(t, want.Reference().Equal(got.Reference()), "edge reference")
	require.Equal(t, want.Type(), got.Type(), "edge type")
	require.Equal(t, want.RelationDirection(), got.RelationDirection(), "edge relation_direction")
	assertContentEqual(t, want, got, "edge")
	require.Equal(t, want.Fields(), got.Fields(), "edge field names")
	for _, name := range want.Fields() {
		wv, err := want.GetField(name)
		require.NoError(t, err)
		gv, err := got.GetField(name)
		require.NoError(t, err)
		require.Equal(t, wv, gv, "edge field %q", name)
	}
	require.True(t, want.ID().Equal(got.ID()), "edge id")
}

// contentCarrier is the content surface common to Node and Edge (§4.4).
type contentCarrier interface {
	IsContentExternal() bool
	GetContentHash() Id
	GetContentSize() uint64
	GetInlineContent() ([]byte, error)
}

// assertContentEqual checks the inline/external content rule survives: the
// external flag, the content hash, the byte length, and — when inline —
// the bytes themselves.
func assertContentEqual(t *testing.T, want, got contentCarrier, what string) {
	t.Helper()
	require.Equal(t, want.IsContentExternal(), got.IsContentExternal(),
		"%s content external flag", what)
	require.Equal(t, want.GetContentSize(), got.GetContentSize(), "%s content length", what)
	if wh := want.GetContentHash(); wh != nil {
		require.NotNil(t, got.GetContentHash(), "%s content hash present", what)
		require.True(t, wh.Equal(got.GetContentHash()), "%s content hash", what)
	} else {
		require.Nil(t, got.GetContentHash(), "%s content hash absent", what)
	}
	if !want.IsContentExternal() {
		wb, err := want.GetInlineContent()
		require.NoError(t, err)
		gb, err := got.GetInlineContent()
		require.NoError(t, err)
		require.Equal(t, wb, gb, "%s inline content bytes", what)
	}
}

// --- construction helpers ----------------------------------------------

// contributor builds a signed root contributor with a fresh Ed25519 key —
// the generic test contributor. Per §4.1/§5.7 the pubkey IS the
// contributor's content; the matching private key travels with the returned
// Contributor (AsContributor → WithSigningKey) so claims attributed to it
// sign automatically. A contributor is identified by its key, not a name.
func contributor(t *testing.T) Contributor {
	c, _ := newSignedContributor(t)
	return c
}

// newSignedContributor is contributor with the raw private key exposed, for
// tests that sign explicitly or check key mismatch.
func newSignedContributor(t *testing.T) (Contributor, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubkey, err := EncodePublicKey(priv.Public())
	require.NoError(t, err)
	c, err := NewClaim(NodeContributor, nil).WithInlineContent(pubkey).Sign(priv)
	require.NoError(t, err)
	view, err := c.AsContributor(priv)
	require.NoError(t, err)
	return view, priv
}

// --- tests: the simplest claims ----------------------------------------

// TestClaimRoundTrip_IdentityContributor is the atom at its smallest: an
// identity-Sign root contributor — empty content (no pubkey), no key, so
// id reduces to the plain hash H(S(v)) (§5.7). The only claim that stands
// alone (no contributor edge, §4.3).
func TestClaimRoundTrip_IdentityContributor(t *testing.T) {
	claim, err := NewClaim(NodeContributor, nil).Sign() // no content, no key
	require.NoError(t, err)
	c, err := claim.AsContributor()
	require.NoError(t, err)

	require.True(t, c.IsContributor(), "must be a contributor claim")
	require.Empty(t, c.Edges(), "root contributor has no edges (§4.3)")
	require.Empty(t, mustInline(t, c.Node()), "identity-Sign contributor has empty content (no pubkey)")
	require.Equal(t, "sha2-256", c.ID().Algorithm(), "identity-Sign id is a plain hash")
	roundTrip(t, c)
}

// TestClaimRoundTrip_SelfSignedContributor covers the signed root: the
// pubkey IS the content (§5.7) and the id is an Ed25519 signature. Both
// must survive the codec since they define the claim.
func TestClaimRoundTrip_SelfSignedContributor(t *testing.T) {
	c, _ := newSignedContributor(t)
	require.True(t, c.IsContributor())
	require.NotEmpty(t, mustInline(t, c.Node()), "a signed contributor's content is its pubkey")
	require.Equal(t, "ed25519-pub", c.ID().Algorithm(), "signed id names the sign scheme")
	roundTrip(t, c)
}

// --- tests: content claims with the auto contributor edge --------------

// TestClaimRoundTrip_SourceInlineContent is a source/email claim attributed
// to a contributor: one auto-built contribution/contributor edge, inline
// content, an explicit encoding.
func TestClaimRoundTrip_SourceInlineContent(t *testing.T) {
	alice := contributor(t)
	c, err := NewClaim(TypeSource("email"), alice).
		WithEncoding(EncodingMessage("rfc822")).
		WithInlineContent([]byte("From: alice\r\nTo: bob\r\n\r\nI like apples.\r\n")).
		Sign()
	require.NoError(t, err)

	require.Equal(t, "source/email", c.Node().Type())
	require.False(t, c.Node().IsContentExternal(), "content is inline")
	require.Len(t, c.Edges(), 1, "one auto-built contribution/contributor edge")
	require.Equal(t, EdgeTypeContributor, c.Edges()[0].Type())
	require.True(t, c.Edges()[0].Reference().Equal(alice.ID()), "edge references the contributor")
	roundTrip(t, c)
}

// TestClaimRoundTrip_InlineContentNodeAndEdges pushes inline content onto
// both the node and a hand-built edge, verifying the codec carries edge
// content as faithfully as node content.
func TestClaimRoundTrip_InlineContentNodeAndEdges(t *testing.T) {
	alice := contributor(t)
	source, err := NewClaim(TypeSource("email"), alice).
		WithInlineContent([]byte("From: alice\r\n\r\nhello")).
		Sign()
	require.NoError(t, err)

	// A derivation/source edge that itself carries inline content — the
	// provenance edge an entity claim requires (§3.5).
	provEdge, err := NewEdge(EdgeConfig{
		Reference:     source.ID(),
		Type:          TypeDerivation("source"),
		InlineContent: []byte("extracted from the greeting line"),
	})
	require.NoError(t, err)
	require.False(t, provEdge.IsContentExternal())

	entity, err := NewClaim(TypeEntity("person"), alice).
		WithInlineContent([]byte("Alice")).
		WithEdges(provEdge).
		Sign()
	require.NoError(t, err)

	// contributor edge + the derivation edge.
	require.Len(t, entity.Edges(), 2)
	roundTrip(t, entity)
}

// TestClaimRoundTrip_ExternalContentNodeAndEdges references external
// content by hash on both the node and an edge. Content and ContentHash
// are mutually exclusive (§4.4), so external content is expressed by
// ContentHash alone — never alongside Content. The claim codec moves the
// hash (and, for edges, the size) only; it never touches the external
// bytes, so the claim round-trips without a Universe. (Retrieving the
// bytes through a Universe is exercised by the content tests below.)
func TestClaimRoundTrip_ExternalContentNodeAndEdges(t *testing.T) {
	alice := contributor(t)

	edgeBlob := []byte("a large external edge payload")
	edgeHash, err := hashContent(edgeBlob)
	require.NoError(t, err)
	nodeHash, err := hashContent([]byte("a large external node payload"))
	require.NoError(t, err)

	source, err := NewClaim(TypeSource("blob"), alice).WithInlineContent([]byte("seed")).Sign()
	require.NoError(t, err)

	extEdge, err := NewEdge(EdgeConfig{
		Reference:   source.ID(),
		Type:        TypeDerivation("source"),
		ContentHash: edgeHash,
		ContentSize: uint64(len(edgeBlob)),
	})
	require.NoError(t, err)
	require.True(t, extEdge.IsContentExternal(), "edge content is external")
	require.Equal(t, uint64(len(edgeBlob)), extEdge.GetContentSize(), "edge external size recorded")

	nodeBlobLen := uint64(len("a large external node payload"))
	c, err := NewClaim(TypeEntity("object"), alice).
		WithExternalContent(nodeHash, nodeBlobLen). // hash+size, no inline bytes (XOR, §4.4)
		WithEdges(extEdge).
		Sign()
	require.NoError(t, err)
	require.True(t, c.Node().IsContentExternal(), "node content is external")
	require.Equal(t, nodeBlobLen, c.Node().GetContentSize(), "external node size recorded")
	roundTrip(t, c)
}

// TestClaimRoundTrip_Fields exercises the optional node fields map. CBOR
// Deterministic sorts map keys, so multi-field claims must round-trip stably
// regardless of insertion order. "title" is included as an ordinary field —
// it carries no special meaning (Name is the only reserved label).
func TestClaimRoundTrip_Fields(t *testing.T) {
	alice := contributor(t)
	c, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("body")).
		WithField("zeta", "26").
		WithField("alpha", "1").
		WithField("title", "just a plain field").
		Sign()
	require.NoError(t, err)

	require.Equal(t, []string{"alpha", "title", "zeta"}, c.Node().Fields(), "fields returned sorted")
	v, err := c.Node().GetField("title")
	require.NoError(t, err)
	require.Equal(t, "just a plain field", v, "title is an ordinary field")
	roundTrip(t, c)
}

// TestClaimRoundTrip_RelationDirections builds a relation claim with a
// from-edge and a to-edge (§4.7), confirming relation_direction survives
// the codec on each edge.
func TestClaimRoundTrip_RelationDirections(t *testing.T) {
	alice := contributor(t)
	source, err := NewClaim(TypeSource("doc"), alice).WithInlineContent([]byte("src")).Sign()
	require.NoError(t, err)
	person, err := NewClaim(TypeEntity("person"), alice).
		WithInlineContent([]byte("Alice")).
		WithEdges(mustDerivEdge(t, source)).
		Sign()
	require.NoError(t, err)
	object, err := NewClaim(TypeEntity("object"), alice).
		WithInlineContent([]byte("apples")).
		WithEdges(mustDerivEdge(t, source)).
		Sign()
	require.NoError(t, err)

	fromEdge, err := NewEdge(EdgeConfig{
		Reference: person.ID(), Type: "relation/likes", RelationDirection: RelationFrom,
	})
	require.NoError(t, err)
	toEdge, err := NewEdge(EdgeConfig{
		Reference: object.ID(), Type: "relation/likes", RelationDirection: RelationTo,
	})
	require.NoError(t, err)

	rel, err := NewClaim(TypeRelation("likes"), alice).
		WithInlineContent([]byte("likes")).
		WithEdges(fromEdge, toEdge, mustDerivEdge(t, source)).
		Sign()
	require.NoError(t, err)

	// Confirm both directions are present before the round-trip.
	var haveFrom, haveTo bool
	for _, e := range rel.Edges() {
		switch e.RelationDirection() {
		case RelationFrom:
			haveFrom = true
		case RelationTo:
			haveTo = true
		}
	}
	require.True(t, haveFrom && haveTo, "relation claim carries both a from and a to edge")
	roundTrip(t, rel)
}

// TestClaimIdReflectsContent: a claim's id is content-addressed — two
// claims identical but for one content byte get different ids. This is the
// collision-sensitivity tamper detection leans on (§5.2); relocated from
// the old tests/ signing suite. Timestamp pinned so only content differs.
func TestClaimIdReflectsContent(t *testing.T) {
	alice := contributor(t)
	at := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	mk := func(body string) Claim {
		c, err := NewClaim(TypeSource("note"), alice).
			WithInlineContent([]byte(body)).
			WithCreatedAt(at).
			Sign()
		require.NoError(t, err)
		return c
	}
	require.False(t, mk("hello").ID().Equal(mk("hellO").ID()),
		"a one-byte content change must yield a different claim id")
}

func mustDerivEdge(t *testing.T, source Claim) Edge {
	t.Helper()
	e, err := NewEdge(EdgeConfig{Reference: source.ID(), Type: TypeDerivation("source")})
	require.NoError(t, err)
	return e
}
