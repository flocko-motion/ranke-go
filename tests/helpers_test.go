package ranke_test

import (
	"fmt"
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

// --- Contributors ---

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

// makeAgent builds a contribution/contributor claim attributed to the
// given operator — i.e. a downstream contributor (e.g. an LLM agent)
// whose own contributor is the operator.
func makeAgent(t *testing.T, operator ranke.Contributor, name string) ranke.Contributor {
	t.Helper()
	c, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeContribution,
		TypeSub:       "contributor",
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(name),
		Contributor:   operator,
	})
	require.NoError(t, err, "make agent %q", name)
	agent, err := c.AsContributor()
	require.NoError(t, err, "AsContributor")
	return agent
}

// --- Sources ---

// makeEmail builds a source/email claim with the given headers and
// body, attributed to the given contributor. Bytes are an RFC 822
// rendering of the from/to/content triple.
func makeEmail(t *testing.T, contributor ranke.Contributor, from, to, content string) ranke.Claim {
	t.Helper()
	bytes := []byte(fmt.Sprintf(
		"From: %s\r\nTo: %s\r\n\r\n%s",
		from, to, content,
	))
	c, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeSource,
		TypeSub:       "email",
		EncodingClass: ranke.EncodingMessage,
		EncodingSub:   "rfc822",
		Content:       bytes,
		Contributor:   contributor,
	})
	require.NoError(t, err, "make email")
	return c
}

// --- Derivations ---

// derivationSourceEdge builds a derivation/source edge from a derived
// claim back to the source it was derived from.
func derivationSourceEdge(t *testing.T, source ranke.Claim) ranke.Edge {
	t.Helper()
	e, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: source.ID(),
		TypeClass: ranke.EdgeDerivation,
		TypeSub:   "source",
	})
	require.NoError(t, err, "build derivation/source edge")
	return e
}

// makeSummary builds a derivation/summary claim — text condensing
// the given source. The claim has a derivation/source edge back to
// the source.
func makeSummary(t *testing.T, contributor ranke.Contributor, source ranke.Claim, text string) ranke.Claim {
	t.Helper()
	c, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeDerivation,
		TypeSub:       "summary",
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(text),
		Contributor:   contributor,
		Edges:         []ranke.Edge{derivationSourceEdge(t, source)},
	})
	require.NoError(t, err, "make summary")
	return c
}

// makeEntity builds an entity/<sub> claim with the given label as
// content and a derivation/source edge to the source the entity was
// extracted from. Two entities with the same label but different
// sources have distinct ids by content addressing.
func makeEntity(t *testing.T, contributor ranke.Contributor, sub, label string, source ranke.Claim) ranke.Claim {
	t.Helper()
	c, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeEntity,
		TypeSub:       sub,
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(label),
		Contributor:   contributor,
		Edges:         []ranke.Edge{derivationSourceEdge(t, source)},
	})
	require.NoError(t, err, "make entity %s/%s", sub, label)
	return c
}

// --- Relations ---

// makeRelation builds a relation/<sub> claim with full provenance.
// Edges include:
//   - one relation/<sub> edge per entity in froms (RelationFrom) and
//     in tos (RelationTo), tagging each entity's role in the relation
//     (§4.7);
//   - one derivation/source edge per source the relation was extracted
//     from — the §3.5 provenance commitment: every derivation cites
//     its sources;
//   - the contribution/contributor edge (§4.3), auto-built by NewClaim.
//
// A relation may carry any number on either side: 1-1 (Alice likes
// apples), N-1 (Alice and Bob co-authored a paper), N-M (a meeting
// involving these people on these dates), etc. For symmetric
// relations (no role distinction), use makeSymmetricRelation.
//
// content is the relation node's content — the assertion itself,
// confidence/conviction, reasoning, anything the application wants
// to attach to *this* reified relation (§3.4 mentions conviction
// values and reasoning content as level-of-detail attributes). Free
// text here; encoded as text/plain.
func makeRelation(t *testing.T, contributor ranke.Contributor, sub, content string, sources, froms, tos []ranke.Claim) ranke.Claim {
	t.Helper()
	edges := make([]ranke.Edge, 0, len(sources)+len(froms)+len(tos))
	for _, src := range sources {
		edges = append(edges, derivationSourceEdge(t, src))
	}
	for _, f := range froms {
		e, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference:         f.ID(),
			TypeClass:         ranke.EdgeRelation,
			TypeSub:           sub,
			RelationDirection: ranke.RelationFrom,
		})
		require.NoError(t, err, "from edge for relation/%s", sub)
		edges = append(edges, e)
	}
	for _, target := range tos {
		e, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference:         target.ID(),
			TypeClass:         ranke.EdgeRelation,
			TypeSub:           sub,
			RelationDirection: ranke.RelationTo,
		})
		require.NoError(t, err, "to edge for relation/%s", sub)
		edges = append(edges, e)
	}
	c, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeRelation,
		TypeSub:       sub,
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(content),
		Contributor:   contributor,
		Edges:         edges,
	})
	require.NoError(t, err, "make relation/%s", sub)
	return c
}

// makeSymmetricRelation builds a relation/<sub> claim where every
// member edge carries RelationFrom — no role distinction (§4.7,
// e.g. "are_friends", "are_family"). Provenance: a derivation/source
// edge to each source (§3.5). content is the relation node's content
// (assertion text, conviction, reasoning); same role as in
// makeRelation.
func makeSymmetricRelation(t *testing.T, contributor ranke.Contributor, sub, content string, sources []ranke.Claim, members ...ranke.Claim) ranke.Claim {
	t.Helper()
	edges := make([]ranke.Edge, 0, len(sources)+len(members))
	for _, src := range sources {
		edges = append(edges, derivationSourceEdge(t, src))
	}
	for _, m := range members {
		e, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference:         m.ID(),
			TypeClass:         ranke.EdgeRelation,
			TypeSub:           sub,
			RelationDirection: ranke.RelationFrom,
		})
		require.NoError(t, err, "edge for symmetric relation/%s", sub)
		edges = append(edges, e)
	}
	c, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeRelation,
		TypeSub:       sub,
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(content),
		Contributor:   contributor,
		Edges:         edges,
	})
	require.NoError(t, err, "make symmetric relation/%s", sub)
	return c
}

// --- Helper-contract tests ---

// TestMakeContributor pins down the contract of makeContributor so
// downstream tests can rely on it without re-checking these properties.
func TestMakeContributor(t *testing.T) {
	c := makeContributor(t, "operator@example.com")

	require.Equal(t, ranke.NodeContribution, c.Node().TypeClass())
	require.Equal(t, "contributor", c.Node().TypeSub())
	require.Equal(t, ranke.EncodingText, c.Node().EncodingClass())
	require.Equal(t, "plain", c.Node().EncodingSub())
	require.Equal(t, "contribution/contributor", c.Node().Type())
	require.Equal(t, "text/plain", c.Node().Encoding())

	got, err := c.Node().Content()
	require.NoError(t, err)
	require.Equal(t, []byte("operator@example.com"), got)

	require.True(t, c.IsContributor())
	require.True(t, c.Contributor().ID().Equal(c.ID()),
		"root contributor self-attributes (§4.3)")

	view, err := c.AsContributor()
	require.NoError(t, err)
	require.True(t, view.ID().Equal(c.ID()))
}

// TestMakeAgent pins down that an agent is a contribution/contributor
// claim whose contributor is the operator, not itself.
func TestMakeAgent(t *testing.T) {
	operator := makeContributor(t, "operator@example.com")
	agent := makeAgent(t, operator, "extraction-agent-v1")

	require.Equal(t, ranke.NodeContribution, agent.Node().TypeClass())
	require.Equal(t, "contributor", agent.Node().TypeSub())
	require.True(t, agent.IsContributor())
	require.True(t, agent.Contributor().ID().Equal(operator.ID()),
		"agent's contributor is the operator, not itself")
	require.False(t, agent.ID().Equal(operator.ID()))
}

// TestMakeEmail pins down the email helper's claim shape.
func TestMakeEmail(t *testing.T) {
	operator := makeContributor(t, "operator@example.com")
	email := makeEmail(t, operator,
		"alice@example.com",
		"bob@example.com",
		"hello",
	)

	require.Equal(t, ranke.NodeSource, email.Node().TypeClass())
	require.Equal(t, "email", email.Node().TypeSub())
	require.Equal(t, ranke.EncodingMessage, email.Node().EncodingClass())
	require.Equal(t, "rfc822", email.Node().EncodingSub())

	got, err := email.Node().Content()
	require.NoError(t, err)
	require.Contains(t, string(got), "From: alice@example.com")
	require.Contains(t, string(got), "To: bob@example.com")
	require.Contains(t, string(got), "hello")

	require.False(t, email.IsContributor())
	_, err = email.AsContributor()
	require.Error(t, err)
	require.True(t, email.Contributor().ID().Equal(operator.ID()))
}
