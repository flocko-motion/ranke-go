package ranke

import (
	"encoding/json"
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
		_, err := NewClaim(typ, alice).WithInlineContent([]byte("x")).WithHeight(HeightOf(alice)).Sign()
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
		WithHeight(HeightOf(alice)).
		Sign()
	require.Error(t, err, "encoding without content must be rejected")
}

// TestContentRequiresEncoding: encoding (the content media type) is MANDATORY
// whenever a claim carries content — the paper lists it in every content-bearing
// node schema and commits it into the id preimage (a raw pubkey is an encoding
// too). A content claim without an encoding is rejected; an explicit encoding is
// preserved.
func TestContentRequiresEncoding(t *testing.T) {
	alice := contributor(t)

	_, err := NewClaim(TypeSource("note"), alice).WithInlineContent([]byte("x")).WithHeight(HeightOf(alice)).Sign()
	require.ErrorIs(t, err, errContentWithoutEncoding, "content without an encoding must be rejected")

	typed, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("x")).
		WithEncoding("text/plain").
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)
	require.Equal(t, "text/plain", typed.Node().Encoding(), "an explicit encoding is kept")
}

// TestClaimHeightRoundTrip: height is part of the canonical node encoding, so
// it survives Encode/DecodeClaim (and, being in S(node), the id commits to it).
func TestClaimHeightRoundTrip(t *testing.T) {
	alice := contributor(t)
	src, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("body")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)
	require.Equal(t, uint64(1), src.Node().Height())
	require.Equal(t, uint64(1), roundTrip(t, src).Node().Height(), "height survives the codec")
}

// --- EncodeJSON: the same information as CBOR --------------------------

// TestEncodeJSONCarriesEveryEdgeSlot: spec §Output says JSON and CBOR carry the
// same information, so an edge renders every slot its record holds — its fields,
// its relation direction, and its own content. Dropping any of them makes the
// projection a summary rather than the claim.
func TestEncodeJSONCarriesEveryEdgeSlot(t *testing.T) {
	alice := contributor(t)
	target, err := NewClaim(TypeSource("register"), alice).
		WithInlineContent([]byte("a parish register")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)

	// A relation claim rests on its provenance (§3.5), so it cites the source it
	// was read from as well as the entity it relates.
	prov, err := NewEdge(EdgeConfig{Reference: target.ID(), Type: TypeDerivation("register")})
	require.NoError(t, err)
	person, err := NewClaim(TypeEntity("person"), alice).
		WithEdges(prov).
		WithHeight(HeightOf(alice, target)).
		Sign()
	require.NoError(t, err)

	e, err := NewEdge(EdgeConfig{
		Reference:         person.ID(),
		TypeClass:         EdgeClassRelation,
		TypeSub:           "family",
		RelationDirection: RelationFrom,
		Fields:            map[string]string{FieldName: "mother", "certainty": "high"},
		InlineContent:     []byte("why"),
		Encoding:          EncodingPlain,
	})
	require.NoError(t, err)

	c, err := NewClaim(TypeRelation("family"), alice).
		WithEdges(prov, e).
		WithField("topic", "kinship").
		WithHeight(HeightOf(alice, target, person)).
		Sign()
	require.NoError(t, err)

	raw, err := c.EncodeJSON(FormOriginal)
	require.NoError(t, err)
	var got struct {
		Type   string            `json:"type"`
		Fields map[string]string `json:"fields"`
		Edges  []struct {
			Type              string            `json:"type"`
			Reference         string            `json:"reference"`
			RelationDirection int8              `json:"relation_direction"`
			Encoding          string            `json:"encoding"`
			ContentSize       uint64            `json:"content_size"`
			Content           []byte            `json:"content"`
			Fields            map[string]string `json:"fields"`
		} `json:"edges"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))

	require.Equal(t, "relation/family", got.Type)
	require.Equal(t, map[string]string{"topic": "kinship"}, got.Fields, "node fields nest")

	var rel int
	for _, je := range got.Edges {
		if je.Type != "relation/family" {
			continue
		}
		rel++
		require.Equal(t, person.ID().String(), je.Reference)
		require.Equal(t, int8(RelationFrom), je.RelationDirection, "direction survives")
		require.Equal(t, "mother", je.Fields[FieldName], "an edge's name survives")
		require.Equal(t, "high", je.Fields["certainty"], "every edge field survives")
		require.Equal(t, EncodingPlain, je.Encoding, "an edge's own content is declared")
		require.Equal(t, uint64(3), je.ContentSize)
		require.Equal(t, []byte("why"), je.Content, "content is base64 in JSON")
	}
	require.Equal(t, 1, rel, "the relation edge is in the projection")
}

// TestEncodeJSONFieldCannotMaskAStructuralKey: a field name is [a-z0-9_], so
// "type" is a legal one. Nesting the fields keeps it from overwriting the
// structural key, which would lose both values.
func TestEncodeJSONFieldCannotMaskAStructuralKey(t *testing.T) {
	alice := contributor(t)
	c, err := NewClaim(TypeSource("note"), alice).
		WithField("type", "not the claim type").
		WithField("height", "not the height").
		WithHeight(HeightOf(alice)).
		Sign()
	require.NoError(t, err)

	raw, err := c.EncodeJSON(FormOriginal)
	require.NoError(t, err)
	var got struct {
		Type   string            `json:"type"`
		Height uint64            `json:"height"`
		Fields map[string]string `json:"fields"`
	}
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, "source/note", got.Type, "the structural type is the claim's")
	require.Equal(t, uint64(1), got.Height)
	require.Equal(t, "not the claim type", got.Fields["type"], "and the field is still readable")
	require.Equal(t, "not the height", got.Fields["height"])
}
