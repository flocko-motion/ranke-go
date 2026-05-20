// Guarantee tests — confirm the structural invariants the library
// must enforce. Each test names an invariant from the paper and
// exercises the failure mode: we try to construct something the
// library should reject, and assert that NewClaim / NewEdge / etc.
// say no.
package tests

import (
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

// TestProvenanceRequired confirms that ranke.NewClaim refuses to
// build derivation/*, entity/*, and relation/* claims without at
// least one derivation/* edge — the §3.5 invariant that every
// interpretation of existing claims cites its sources. source/* and
// contribution/* claims have no such requirement.
func TestProvenanceRequired(t *testing.T) {
	operator := contributor(t, "operator@example.com")
	ag := agent(t, operator, "extraction-agent")
	// We need entities to anchor the relation case — use ones that
	// already have provenance, so only the relation under test is
	// missing it.
	emailApples := email(t, operator,
		"alice@example.com", "bob@example.com", "I like apples.")
	alice := entity(t, ag, "person", "Alice", emailApples)
	apples := entity(t, ag, "object", "apples", emailApples)

	// Build the relation/* edges that the relation node would carry.
	// These are NOT derivation edges — the rule we're testing is
	// that relation/* edges alone are insufficient; a derivation/*
	// edge to the source must also be present.
	fromEdge, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference:         alice.ID(),
		TypeClass:         ranke.EdgeRelation,
		TypeSub:           "likes",
		RelationDirection: ranke.RelationFrom,
	})
	require.NoError(t, err)
	toEdge, err := ranke.NewEdge(ranke.EdgeConfig{
		Reference:         apples.ID(),
		TypeClass:         ranke.EdgeRelation,
		TypeSub:           "likes",
		RelationDirection: ranke.RelationTo,
	})
	require.NoError(t, err)

	cases := []struct {
		name      string
		typeClass ranke.NodeClass
		typeSub   string
		edges     []ranke.Edge
	}{
		{
			name:      "derivation without source",
			typeClass: ranke.NodeDerivation,
			typeSub:   "summary",
		},
		{
			name:      "entity without source",
			typeClass: ranke.NodeEntity,
			typeSub:   "person",
		},
		{
			name:      "relation without source — relation edges alone are not provenance",
			typeClass: ranke.NodeRelation,
			typeSub:   "likes",
			edges:     []ranke.Edge{fromEdge, toEdge},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ranke.ClaimBuilder{
				TypeClass:   tc.typeClass,
				TypeSub:     tc.typeSub,
				Content:     []byte("..."),
				Contributor: ag,
				Edges:       tc.edges,
			}.Sign()
			require.Error(t, err,
				"Sign must reject %s/%s with no derivation/* edge",
				tc.typeClass, tc.typeSub)
		})
	}
}
