package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the edge wire aliases (§5.1). Uses the shared
// checkAliasRoundTrip helper (node_taxonomy_test.go, same package) to pin
// the bijection / no-collision / pass-through properties for edge classes
// and the closed contribution/* edge subtypes.

func TestEdgeClassAliases(t *testing.T) {
	checkAliasRoundTrip(t, map[EdgeClass]EdgeClass{
		EdgeClassContribution: EdgeClassContributionAlias,
		EdgeClassDerivation:   EdgeClassDerivationAlias,
		EdgeClassRelation:     EdgeClassRelationAlias,
	}, edgeClassToAlias, edgeClassFromAlias, EdgeClass("madeupclass"))
}

func TestEdgeSubtypeAliases(t *testing.T) {
	checkAliasRoundTrip(t, map[EdgeSubtype]EdgeSubtype{
		EdgeSubtypeContributor: EdgeSubtypeContributorAlias,
		EdgeSubtypeHead:        EdgeSubtypeHeadAlias,
		EdgeSubtypeBranches:    EdgeSubtypeBranchesAlias,
		EdgeSubtypeBranch:      EdgeSubtypeBranchAlias,
		EdgeSubtypePrune:       EdgeSubtypePruneAlias,
		EdgeSubtypeDiff:        EdgeSubtypeDiffAlias,
		EdgeSubtypeDelete:      EdgeSubtypeDeleteAlias,
		EdgeSubtypeExpiry:      EdgeSubtypeExpiryAlias,
	}, edgeSubtypeToAlias, edgeSubtypeFromAlias, EdgeSubtype("transcription")) // open vocabulary
}

// TestEdgeAliasesAreSingleCharacter: edge aliases are one character (§5.1).
func TestEdgeAliasesAreSingleCharacter(t *testing.T) {
	for long, short := range map[string]string{
		string(EdgeClassDerivation): string(edgeClassToAlias(EdgeClassDerivation)),
		string(EdgeSubtypePrune):    string(edgeSubtypeToAlias(EdgeSubtypePrune)),
		string(EdgeSubtypeDelete):   string(edgeSubtypeToAlias(EdgeSubtypeDelete)),
		string(EdgeSubtypeExpiry):   string(edgeSubtypeToAlias(EdgeSubtypeExpiry)),
	} {
		require.Len(t, short, 1, "alias for %q must be one character", long)
	}
}

// TestLimitingEdgeTypesMatchTheirClaims: a limiting claim and the edge naming its
// target share a type string (paper 1 §Type Vocabulary), so one constant cannot
// drift from the other.
func TestLimitingEdgeTypesMatchTheirClaims(t *testing.T) {
	require.Equal(t, NodeDelete, EdgeTypeDelete)
	require.Equal(t, NodeExpiry, EdgeTypeExpiry)
}
