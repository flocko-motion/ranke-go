// Scenario tests — user-flow stories drawn from the paper.
//
// Each test reads as a small narrative: actors do things, the graph
// reflects the result. They exercise the public API the way a real
// user would, building on the helpers in helpers_test.go and the
// invariants pinned by guarantees_test.go.
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
	email := makeEmail(t, operator,
		"alice@example.com",
		"bob@example.com",
		"Bob, just so you know, I really do like apples.\r\n— Alice\r\n",
	)

	g := ranke.NewGraph(operator)

	emailID, err := g.AddClaim(email)
	require.NoError(t, err)

	// Idempotent (§4.3).
	dupID, err := g.AddClaim(email)
	require.NoError(t, err)
	require.True(t, dupID.Equal(emailID))

	require.True(t, g.ContainsClaim(operator.ID()))
	require.True(t, g.ContainsClaim(emailID))

	// Single open head — the email. The operator is referenced by
	// the email's contribution/contributor edge (§4.5).
	heads := g.Heads()
	require.Len(t, heads, 1)
	require.True(t, heads[0].Equal(emailID))
	require.True(t, g.IsConsolidated())

	require.NoError(t, g.Validate())
}

// TestAgentAnalyzesEmails follows the broader story sketched in §3.5
// (taxonomy) and §4.7 (semantic relations): an agent contributor
// analyzes source emails to extract derivations, entities, and
// semantic relations.
//
// The story exercises:
//
//   - a second contributor (agent) attributed to the operator
//   - source claims (two emails)
//   - a derivation/* claim (summary) referencing its source
//   - entity/* claims with the same surface name but distinct ids
//     (two Bobs from different sources)
//   - relation/* claims with relation_direction
//     · Alice—knows→Bob_Sr together with Bob_Sr—ignores→Alice make
//     a cycle in the *semantic reading* (§4.7); the *structural
//     reading* stays acyclic.
//     · Bob_Sr ↔ Bob_Jr family is symmetric (all-RelationFrom).
func TestAgentAnalyzesEmails(t *testing.T) {
	operator := makeContributor(t, "operator@example.com")
	agent := makeAgent(t, operator, "extraction-agent-v1")

	g := ranke.NewGraph(operator)

	// The agent itself is a claim in the graph, contributed by the
	// operator.
	_, err := g.AddClaim(agent)
	require.NoError(t, err)

	// Two emails, both ingested by the operator (sources).
	emailApples := makeEmail(t, operator,
		"alice@example.com",
		"bob@example.com",
		"Bob, just so you know, I really do like apples.\r\n— Alice\r\n",
	)
	emailFamily := makeEmail(t, operator,
		"alice@example.com",
		"bob@example.com",
		"Bob, please tell Bob Jr. I miss him.\r\n— Alice\r\n",
	)
	_, err = g.AddClaim(emailApples)
	require.NoError(t, err)
	_, err = g.AddClaim(emailFamily)
	require.NoError(t, err)

	// Agent extracts a summary of the apples email.
	summary := makeSummary(t, agent, emailApples,
		"Alice expresses preference for apples.",
	)
	_, err = g.AddClaim(summary)
	require.NoError(t, err)

	// Agent extracts entities. Two Bobs from different sources are
	// distinct entity claims — content-addressed ids differ because
	// their derivation/source edges differ.
	alice := makeEntity(t, agent, "person", "Alice", emailApples)
	apples := makeEntity(t, agent, "object", "apples", emailApples)
	bobSr := makeEntity(t, agent, "person", "Bob", emailApples)
	bobJr := makeEntity(t, agent, "person", "Bob Jr.", emailFamily)
	for _, e := range []ranke.Claim{alice, apples, bobSr, bobJr} {
		_, err := g.AddClaim(e)
		require.NoError(t, err)
	}
	require.False(t, bobSr.ID().Equal(bobJr.ID()),
		"two Bob entities from different sources have distinct ids")

	// Agent extracts relations. Every relation cites its source(s)
	// via derivation/source edges (§3.5: derivations cite their
	// sources — that is the project's whole point). Each call also
	// passes the from- and to-side entities (§4.7). The content
	// carries the agent's assertion / reasoning / conviction (§3.4).
	likes := makeRelation(t, agent, "likes",
		"Alice expresses preference for apples in the email.",
		[]ranke.Claim{emailApples},
		[]ranke.Claim{alice}, []ranke.Claim{apples})
	knows := makeRelation(t, agent, "knows",
		"Alice addresses Bob directly, implying they are acquainted.",
		[]ranke.Claim{emailApples},
		[]ranke.Claim{alice}, []ranke.Claim{bobSr})
	ignores := makeRelation(t, agent, "ignores",
		"Bob does not respond to Alice (inferred; low conviction).",
		[]ranke.Claim{emailApples},
		[]ranke.Claim{bobSr}, []ranke.Claim{alice})
	family := makeSymmetricRelation(t, agent, "family",
		"Bob Sr. and Bob Jr. share a surname; Alice's email implies kinship.",
		[]ranke.Claim{emailFamily},
		bobSr, bobJr)
	for _, r := range []ranke.Claim{likes, knows, ignores, family} {
		_, err := g.AddClaim(r)
		require.NoError(t, err)
	}

	// Structural integrity holds — the structural reading is
	// acyclic (§4.7), even though the semantic reading admits the
	// Alice ↔ Bob_Sr cycle through knows + ignores.
	require.NoError(t, g.Validate())

	// The graph is now multi-headed: each top-level derivation,
	// entity, and relation is a leaf (nothing references it). It
	// is NOT consolidated until a contribution/head claim unifies
	// the open heads (§4.5). Consolidation is the next scenario.
	require.False(t, g.IsConsolidated())
	require.Greater(t, len(g.Heads()), 1)
}
