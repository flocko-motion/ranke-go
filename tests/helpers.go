package tests

import (
	"fmt"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

// Test helpers — small builders for the canonical actors and
// artifacts that scenarios use. Lowercase because they live in the
// tests package and don't form part of any public API; 3rd parties
// who want to write their own scenarios can copy them.
//
// Each helper takes *testing.T and require's success — failures end
// the test on the spot, so call sites can ignore errors.

// mkContributor builds a root contribution/contributor claim with
// the given identifier as content. The claim self-attributes per §4.3.
func mkContributor(t *testing.T, id string) ranke.Contributor {
	t.Helper()
	c, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeContribution,
		TypeSub:       "contributor",
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(id),
	})
	require.NoError(t, err, "mkContributor %q", id)
	view, err := c.AsContributor()
	require.NoError(t, err)
	return view
}

// mkAgent builds a contribution/contributor claim attributed to the
// given operator — i.e. a downstream contributor (e.g. an LLM agent)
// whose own contributor is the operator.
func mkAgent(t *testing.T, operator ranke.Contributor, name string) ranke.Contributor {
	t.Helper()
	c, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeContribution,
		TypeSub:       "contributor",
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(name),
		Contributor:   operator,
	})
	require.NoError(t, err, "mkAgent %q", name)
	view, err := c.AsContributor()
	require.NoError(t, err)
	return view
}

// mkEmail builds a source/email claim with the given headers and
// body, attributed to the given contributor. Bytes are an RFC 822
// rendering of the from/to/content triple.
func mkEmail(t *testing.T, contributor ranke.Contributor, from, to, content string) ranke.Claim {
	t.Helper()
	body := fmt.Sprintf("From: %s\r\nTo: %s\r\n\r\n%s", from, to, content)
	c, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeSource,
		TypeSub:       "email",
		EncodingClass: ranke.EncodingMessage,
		EncodingSub:   "rfc822",
		Content:       []byte(body),
		Contributor:   contributor,
	})
	require.NoError(t, err, "mkEmail")
	return c
}

// derivationSourceEdge builds a derivation/source edge from a
// derived claim back to the source it was derived from.
func derivationSourceEdge(t *testing.T, source ranke.Claim) ranke.Edge {
	t.Helper()
	e, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference: source.ID(),
		TypeClass: ranke.EdgeDerivation,
		TypeSub:   "source",
	})
	require.NoError(t, err)
	return e
}

// mkSummary builds a derivation/summary claim — text condensing the
// given source. Has a derivation/source edge back to the source.
func mkSummary(t *testing.T, contributor ranke.Contributor, source ranke.Claim, text string) ranke.Claim {
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
	require.NoError(t, err, "mkSummary")
	return c
}

// mkEntity builds an entity/<sub> claim with the given label as
// content and a derivation/source edge to the source the entity was
// extracted from. Two entities with the same label but different
// sources have distinct ids by content addressing.
func mkEntity(t *testing.T, contributor ranke.Contributor, sub, label string, source ranke.Claim) ranke.Claim {
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
	require.NoError(t, err, "mkEntity %s/%s", sub, label)
	return c
}

// mkRelation builds a relation/<sub> claim with full provenance:
//
//   - one derivation/source edge per source (§3.5),
//   - one relation/<sub> edge per from-side entity (RelationFrom),
//   - one relation/<sub> edge per to-side entity (RelationTo),
//   - the contribution/contributor edge is auto-built by NewClaim.
func mkRelation(t *testing.T, contributor ranke.Contributor, sub, content string, sources, froms, tos []ranke.Claim) ranke.Claim {
	t.Helper()
	edges := make([]ranke.Edge, 0, len(sources)+len(froms)+len(tos))
	for _, s := range sources {
		edges = append(edges, derivationSourceEdge(t, s))
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
	require.NoError(t, err, "mkRelation %s", sub)
	return c
}

// mkSymmetricRelation builds a relation/<sub> claim where every
// member edge carries RelationFrom — no role distinction (§4.7).
func mkSymmetricRelation(t *testing.T, contributor ranke.Contributor, sub, content string, sources []ranke.Claim, members ...ranke.Claim) ranke.Claim {
	t.Helper()
	edges := make([]ranke.Edge, 0, len(sources)+len(members))
	for _, s := range sources {
		edges = append(edges, derivationSourceEdge(t, s))
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
	require.NoError(t, err, "mkSymmetricRelation %s", sub)
	return c
}
