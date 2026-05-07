// Guarantee tests — confirm the structural invariants the library
// must enforce. Each test names an invariant from the paper and
// exercises the failure mode: we try to construct something the
// library should reject, and assert that NewClaim / NewEdge / etc.
// say no. Internal _test.go file: uses the private mk* helpers from
// integration.go.
package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProvenanceRequired confirms that NewClaim refuses to build
// derivation/*, entity/*, and relation/* claims without at least one
// derivation/* edge — the §3.5 invariant that every interpretation
// of existing claims cites its sources. source/* and contribution/*
// claims have no such requirement.
func TestProvenanceRequired(t *testing.T) {
	operator := mkContributor(t, "operator@example.com")
	agent := mkAgent(t, operator, "extraction-agent")
	// We need entities to anchor the relation case — use ones that
	// already have provenance, so only the relation under test is
	// missing it.
	emailApples := mkEmail(t, operator,
		"alice@example.com", "bob@example.com", "I like apples.")
	alice := mkEntity(t, agent, "person", "Alice", emailApples)
	apples := mkEntity(t, agent, "object", "apples", emailApples)

	// Build the relation/* edges that the relation node would carry.
	// These are NOT derivation edges — the rule we're testing is
	// that relation/* edges alone are insufficient; a derivation/*
	// edge to the source must also be present.
	fromEdge, err := NewEdge(EdgeConfig{
		Reference:         alice.ID(),
		TypeClass:         EdgeRelation,
		TypeSub:           "likes",
		RelationDirection: RelationFrom,
	})
	require.NoError(t, err)
	toEdge, err := NewEdge(EdgeConfig{
		Reference:         apples.ID(),
		TypeClass:         EdgeRelation,
		TypeSub:           "likes",
		RelationDirection: RelationTo,
	})
	require.NoError(t, err)

	cases := []struct {
		name      string
		typeClass NodeClass
		typeSub   string
		edges     []Edge
	}{
		{
			name:      "derivation without source",
			typeClass: NodeDerivation,
			typeSub:   "summary",
		},
		{
			name:      "entity without source",
			typeClass: NodeEntity,
			typeSub:   "person",
		},
		{
			name:      "relation without source — relation edges alone are not provenance",
			typeClass: NodeRelation,
			typeSub:   "likes",
			edges:     []Edge{fromEdge, toEdge},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewClaim(ClaimConfig{
				TypeClass:     tc.typeClass,
				TypeSub:       tc.typeSub,
				EncodingClass: EncodingText,
				EncodingSub:   "plain",
				Content:       []byte("..."),
				Contributor:   agent,
				Edges:         tc.edges,
			})
			require.Error(t, err,
				"NewClaim must reject %s/%s with no derivation/* edge",
				tc.typeClass, tc.typeSub)
		})
	}
}
