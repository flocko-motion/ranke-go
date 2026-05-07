// Package ranke_test exercises the public API of the ranke package
// from a user's perspective — only exported symbols, no internals.
//
// Test helpers in helpers_test.go provide the canonical actors and
// artifacts (operator, Alice's email) and pin down their contracts
// in their own tests (TestMakeContributor, TestMakeAliceEmail). The
// scenario tests in this file build on those guarantees and focus
// on the integration story.
package ranke_test

import (
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

// TestAliceEmailFromIntroduction follows the canonical example from
// §1 of the paper:
//
//	"A file exists, attributed to Alice by its headers, that appears
//	to be a copy of an email to Bob in which Alice claims to like
//	apples."
//
// We build the minimum Ranke-Graph that records this third-layer
// observation. §3.5 distinction: the root contributor is operational
// (the operator running this Ranke-Graph instance), not Alice the
// entity. The email is a source; what the email *says* (Alice likes
// apples) is a derivation that would be added later.
func TestAliceEmailFromIntroduction(t *testing.T) {
	operator := makeContributor(t, "operator@example.com")
	email := makeAliceEmail(t, operator)

	// NewGraph seeds the graph with its root contributor — the only
	// no-edge claim a graph may contain (§4.3).
	g := ranke.NewGraph(operator)

	emailID, err := g.AddClaim(email)
	require.NoError(t, err)

	// Idempotent: adding the same claim again returns the same id,
	// no error (§4.3).
	dupID, err := g.AddClaim(email)
	require.NoError(t, err)
	require.True(t, dupID.Equal(emailID))

	// Both claims are in the graph: the root operator (from NewGraph)
	// and the email (from AddClaim).
	require.True(t, g.ContainsClaim(operator.ID()))
	require.True(t, g.ContainsClaim(emailID))

	// Single open head — the email. The operator is referenced by
	// the email's contribution/contributor edge, so it is no longer
	// open (§4.5).
	heads := g.Heads()
	require.Len(t, heads, 1)
	require.True(t, heads[0].Equal(emailID))
	require.True(t, g.IsConsolidated())

	// Integrity holds.
	require.NoError(t, g.Validate())
}
