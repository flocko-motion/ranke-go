package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the field-name / subtype / encoding charset +
// reserved-namespace validators in field_taxonomy.go (checkUserFieldName,
// validFieldChars, checkSubtype, checkEncodingSubtype), exercised through
// the public builder. The system owns a reserved namespace marked by a
// leading "." (the wire-alias namespace); user names must stay out of it.
// Each namespace has a charset:
//
//   - field names   [a-z0-9_], no leading "_", not a reserved structural name
//   - subtypes      the same strict charset — lowercase, digits, "_"
//   - encodings     MORE liberal: also allows uppercase, "+", and "-"
//                   (real MIME subtypes: svg+xml, x-foo, vnd.Bar)
//
// Driven through the public builder — the surface a caller actually uses.
// Field names, subtypes, and encoding subtypes are all validated at
// construction; encoding is the liberal one.

func nsContributor(t *testing.T) Contributor { return contributor(t) }

// buildWithType attempts to build a claim of the given "class/sub" type
// and returns the build error (nil on success).
func buildWithType(t *testing.T, typ string) error {
	t.Helper()
	ctr := nsContributor(t)
	_, err := NewClaim(typ, ctr).WithInlineContent([]byte("x")).WithHeight(HeightOf(ctr)).Sign()
	return err
}

// buildWithEncoding attempts to build a claim with the given encoding
// media type and returns the build error (nil on success).
func buildWithEncoding(t *testing.T, enc string) error {
	t.Helper()
	ctr := nsContributor(t)
	_, err := NewClaim(TypeSource("note"), ctr).
		WithEncoding(enc).
		WithInlineContent([]byte("x")).
		WithHeight(HeightOf(ctr)).
		Sign()
	return err
}

// --- field names --------------------------------------------------------

// TestFieldNameNamespace_Valid: an ordinary user field name is accepted.
func TestFieldNameNamespace_Valid(t *testing.T) {
	ctr := nsContributor(t)
	_, err := NewClaim(TypeSource("note"), ctr).
		WithInlineContent([]byte("x")).
		WithField("topic", "news").
		WithHeight(HeightOf(ctr)).
		Sign()
	require.NoError(t, err)
}

// TestFieldNameNamespace_Rejected: field names must stay in the plain
// charset — the reserved prefixes ("." alias namespace, leading "_") and
// characters outside [a-z0-9_] (e.g. uppercase) are refused at build time.
func TestFieldNameNamespace_Rejected(t *testing.T) {
	alice := nsContributor(t)
	for _, name := range []string{
		".n",      // "." alias namespace
		"_hidden", // leading "_" reserved
		"Topic",   // uppercase not allowed
		"a b",     // space not allowed
	} {
		_, err := NewClaim(TypeSource("note"), alice).
			WithInlineContent([]byte("x")).
			WithField(name, "v").
			WithHeight(HeightOf(alice)).
			Sign()
		require.Error(t, err, "field name %q is out of the allowed namespace and must be rejected", name)
	}
}

// TestFieldNameStructuralNamesAllowed: plain names that look structural
// ("content", "name", "edges") are now valid USER field names — structural
// fields live in the reserved "." alias namespace (.c/.n/.e), so there is
// no collision and no need to reserve the plain names.
func TestFieldNameStructuralNamesAllowed(t *testing.T) {
	alice := nsContributor(t)
	for _, name := range []string{"content", "content_size", "name", "edges"} {
		_, err := NewClaim(TypeSource("note"), alice).
			WithInlineContent([]byte("x")).
			WithField(name, "v").
			WithHeight(HeightOf(alice)).
			Sign()
		require.NoError(t, err, "plain user field name %q no longer collides with a structural field", name)
	}
}

// TestEdgeFieldNameNamespace_Rejected: the same restriction holds for edge
// fields.
func TestEdgeFieldNameNamespace_Rejected(t *testing.T) {
	alice := nsContributor(t)
	src, err := NewClaim(TypeSource("doc"), alice).WithInlineContent([]byte("s")).WithHeight(HeightOf(alice)).Sign()
	require.NoError(t, err)

	_, err = NewEdge(EdgeConfig{
		Reference: src.ID(),
		Type:      TypeDerivation("source"),
		Fields:    map[string]string{".n": "v"},
	})
	require.Error(t, err, "edge field in the reserved \".\" namespace must be rejected")
}

// --- subtypes (strict charset) -----------------------------------------

// TestSubtypeNamespace_Valid: open-vocabulary subtypes within the strict
// charset are accepted.
func TestSubtypeNamespace_Valid(t *testing.T) {
	// All source/* so no provenance edge is required (§3.5) — this test
	// isolates the subtype charset, nothing else.
	for _, typ := range []string{
		"source/email",
		"source/fact_extraction", // "_" allowed
		"source/rfc822",          // digits allowed
	} {
		require.NoError(t, buildWithType(t, typ), "subtype %q is valid", typ)
	}
}

// TestSubtypeNamespace_Rejected: subtypes must reject the reserved "."
// namespace and the characters the strict charset excludes (uppercase,
// "+", "-").
func TestSubtypeNamespace_Rejected(t *testing.T) {
	for _, typ := range []string{
		"source/.hidden", // reserved "." namespace
		"source/Email",   // uppercase not allowed in a subtype
		"source/a+b",     // "+" not allowed in a subtype
		"source/a-b",     // "-" not allowed in a subtype
	} {
		require.Error(t, buildWithType(t, typ), "subtype in %q must be rejected", typ)
	}
}

// TestEdgeSubtypeNamespace_Rejected: same strict rule on edge subtypes.
func TestEdgeSubtypeNamespace_Rejected(t *testing.T) {
	alice := nsContributor(t)
	src, err := NewClaim(TypeSource("doc"), alice).WithInlineContent([]byte("s")).WithHeight(HeightOf(alice)).Sign()
	require.NoError(t, err)

	for _, typ := range []string{"derivation/.hidden", "derivation/Source", "relation/a+b"} {
		_, err := NewEdge(EdgeConfig{Reference: src.ID(), Type: typ})
		require.Error(t, err, "edge subtype in %q must be rejected", typ)
	}
}

// --- encoding subtypes (liberal charset) -------------------------------

// TestEncodingNamespace_Valid: encoding subtypes are more liberal than
// plain subtypes — uppercase, "+", and "-" are all valid (real MIME
// subtypes look like this).
func TestEncodingNamespace_Valid(t *testing.T) {
	for _, enc := range []string{
		"text/plain",
		"image/svg+xml",     // "+" allowed
		"application/x-tar", // "-" allowed
		"text/UPPER",        // uppercase allowed
	} {
		require.NoError(t, buildWithEncoding(t, enc), "encoding %q is valid", enc)
	}
}

// TestEncodingNamespace_Rejected: the reserved "." namespace is still
// off-limits for encoding subtypes.
func TestEncodingNamespace_Rejected(t *testing.T) {
	require.Error(t, buildWithEncoding(t, "text/.x"),
		"an encoding subtype in the reserved \".\" namespace must be rejected")
}
