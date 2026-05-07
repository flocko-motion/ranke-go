package ranke_test

import (
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

// Test helpers for the user-perspective test suite. Boilerplate that
// would otherwise be repeated in every scenario lives here, named so
// the calling test reads as a story.
//
// Naming convention: a `make…` helper builds and returns a value;
// any error fails the test (require, not return).

// makeContributor builds a root contribution/contributor claim with
// the given identifier as content. The claim self-attributes per §4.3.
func makeContributor(t *testing.T, id string) ranke.Contributor {
	t.Helper()
	c, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeContribution,
		TypeSub:       "contributor",
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(id),
	})
	require.NoError(t, err, "make contributor %q", id)
	contributor, err := c.AsContributor()
	require.NoError(t, err, "AsContributor")
	return contributor
}

// aliceEmailBytes is the canonical email from §1 of the paper —
// Alice writing Bob about apples. Used wherever a test wants the
// example source artifact.
var aliceEmailBytes = []byte(
	"From: alice@example.com\r\n" +
		"To: bob@example.com\r\n" +
		"Subject: I like apples\r\n" +
		"\r\n" +
		"Bob, just so you know, I really do like apples.\r\n" +
		"— Alice\r\n")

// makeAliceEmail builds the canonical source/email claim attributed
// to the given contributor.
func makeAliceEmail(t *testing.T, contributor ranke.Contributor) ranke.Claim {
	t.Helper()
	c, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeSource,
		TypeSub:       "email",
		EncodingClass: ranke.EncodingMessage,
		EncodingSub:   "rfc822",
		Content:       aliceEmailBytes,
		Contributor:   contributor,
	})
	require.NoError(t, err, "make alice email")
	return c
}

// TestMakeContributor pins down the contract of makeContributor so
// downstream tests can rely on it without re-checking these properties.
func TestMakeContributor(t *testing.T) {
	c := makeContributor(t, "operator@example.com")

	// Type and encoding are split into typed enums + open subtypes.
	require.Equal(t, ranke.NodeContribution, c.Node().TypeClass())
	require.Equal(t, "contributor", c.Node().TypeSub())
	require.Equal(t, ranke.EncodingText, c.Node().EncodingClass())
	require.Equal(t, "plain", c.Node().EncodingSub())

	// Combined accessor reconstructs the "class/subtype" string.
	require.Equal(t, "contribution/contributor", c.Node().Type())
	require.Equal(t, "text/plain", c.Node().Encoding())

	// Content round-trip.
	got, err := c.Node().Content(nil)
	require.NoError(t, err)
	require.Equal(t, []byte("operator@example.com"), got)

	// Root contributor identity: IsContributor, AsContributor, self-attribution.
	require.True(t, c.IsContributor(),
		"contribution/contributor claim should report IsContributor")
	require.True(t, c.Contributor().ID().Equal(c.ID()),
		"root contributor self-attributes (§4.3)")

	// AsContributor returns the same id (typed view of the same claim).
	view, err := c.AsContributor()
	require.NoError(t, err)
	require.True(t, view.ID().Equal(c.ID()),
		"AsContributor preserves identity")
}

// TestMakeAliceEmail pins down the contract of makeAliceEmail so
// downstream tests can rely on it without re-checking these properties.
func TestMakeAliceEmail(t *testing.T) {
	operator := makeContributor(t, "operator@example.com")
	email := makeAliceEmail(t, operator)

	// Type and encoding.
	require.Equal(t, ranke.NodeSource, email.Node().TypeClass())
	require.Equal(t, "email", email.Node().TypeSub())
	require.Equal(t, ranke.EncodingMessage, email.Node().EncodingClass())
	require.Equal(t, "rfc822", email.Node().EncodingSub())

	// Content round-trip.
	got, err := email.Node().Content(nil)
	require.NoError(t, err)
	require.Equal(t, aliceEmailBytes, got)

	// The email is not a contributor.
	require.False(t, email.IsContributor())
	_, err = email.AsContributor()
	require.Error(t, err,
		"AsContributor must error for non-contributor claims")

	// NewClaim auto-built the contribution/contributor edge.
	require.True(t, email.Contributor().ID().Equal(operator.ID()),
		"email's contributor is the operator passed to makeAliceEmail")
}
