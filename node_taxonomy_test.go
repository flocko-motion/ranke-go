package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the node wire aliases (§5.1). To
// optimise encoding size the reserved vocabulary has one-character short
// forms, and "an alias is semantically identical to its long form." That
// holds only if the mapping is a bijection, aliases don't collide (a
// duplicate would make decoding ambiguous), and open-vocabulary values
// pass through untouched. checkAliasRoundTrip pins all three and is shared
// with edge_taxonomy_test.go (same package).

// checkAliasRoundTrip asserts, for a closed alias namespace: each long
// form maps to its stated alias and back; the round-trip is identity; an
// open/unknown value passes through both directions unchanged; and no two
// long forms share an alias.
func checkAliasRoundTrip[T ~string](
	t *testing.T,
	pairs map[T]T, // long form -> expected alias
	toAlias, fromAlias func(T) T,
	openValue T, // an open-vocabulary / unknown value, expected to pass through
) {
	t.Helper()
	seen := map[T]T{}
	for long, short := range pairs {
		require.Equal(t, short, toAlias(long), "toAlias(%q)", long)
		require.Equal(t, long, fromAlias(short), "fromAlias(%q)", short)
		require.Equal(t, long, fromAlias(toAlias(long)), "round-trip of %q", long)

		require.NotEqual(t, long, short, "alias must differ from the long form for %q", long)
		if prev, dup := seen[short]; dup {
			t.Fatalf("alias %q is shared by %q and %q — decoding would be ambiguous", short, prev, long)
		}
		seen[short] = long
	}
	require.Equal(t, openValue, toAlias(openValue), "open value passes through toAlias")
	require.Equal(t, openValue, fromAlias(openValue), "open value passes through fromAlias")
}

func TestNodeClassAliases(t *testing.T) {
	checkAliasRoundTrip(t, map[NodeClass]NodeClass{
		NodeClassContribution: NodeClassContributionAlias,
		NodeClassSource:       NodeClassSourceAlias,
		NodeClassDerivation:   NodeClassDerivationAlias,
		NodeClassEntity:       NodeClassEntityAlias,
		NodeClassRelation:     NodeClassRelationAlias,
	}, nodeClassToAlias, nodeClassFromAlias, NodeClass("madeupclass"))
}

func TestNodeSubtypeAliases(t *testing.T) {
	checkAliasRoundTrip(t, map[NodeSubtype]NodeSubtype{
		NodeSubtypeContributor: NodeSubtypeContributorAlias,
		NodeSubtypeBranches:    NodeSubtypeBranchesAlias,
		NodeSubtypeHead:        NodeSubtypeHeadAlias,
		NodeSubtypeDelete:      NodeSubtypeDeleteAlias,
		NodeSubtypeExpiry:      NodeSubtypeExpiryAlias,
	}, nodeSubtypeToAlias, nodeSubtypeFromAlias, NodeSubtype("email")) // open vocabulary
}

// TestSubtypeAliasTablesAgree: @tbl:aliases is ONE "type subtype" column that nodes
// and edges share, which ranke-go splits across node_taxonomy.go and
// edge_taxonomy.go. Wherever both halves know a name or a letter they must say the
// same thing, or one claim's node and edge abbreviate the same subtype differently.
//
// A subtype only one half knows is legitimate — the caption names those exceptions,
// and "branch" and "diff" are edge-only — so the check applies where they overlap.
// It catches a letter reused for two meanings, which is what splitting one table
// into two makes possible.
func TestSubtypeAliasTablesAgree(t *testing.T) {
	// Every subtype either table declares, so a name added to one and forgotten in
	// the other is still probed here.
	names := []string{"contributor", "head", "branches", "branch", "diff", "prune", "delete", "expiry"}
	for _, name := range names {
		nodeAlias := string(nodeSubtypeToAlias(NodeSubtype(name)))
		edgeAlias := string(edgeSubtypeToAlias(EdgeSubtype(name)))
		if nodeAlias == name || edgeAlias == name {
			continue // one half does not declare it, and passes the name through
		}
		require.Equalf(t, nodeAlias, edgeAlias, "subtype %q is abbreviated two ways", name)
	}

	// The same agreement read back: a letter both halves decode must decode alike.
	for c := byte('A'); c <= 'z'; c++ {
		letter := string(c)
		nodeName := string(nodeSubtypeFromAlias(NodeSubtype(letter)))
		edgeName := string(edgeSubtypeFromAlias(EdgeSubtype(letter)))
		if nodeName == letter || edgeName == letter {
			continue
		}
		require.Equalf(t, nodeName, edgeName, "alias %q means two things", letter)
	}
}

// TestEdgeOnlySubtypesKeepTheirLetters: removing the node-side "branch" and "diff"
// left @tbl:aliases untouched, so the edge side must still hold b and d.
func TestEdgeOnlySubtypesKeepTheirLetters(t *testing.T) {
	require.Equal(t, "b", string(edgeSubtypeToAlias(EdgeSubtypeBranch)))
	require.Equal(t, "d", string(edgeSubtypeToAlias(EdgeSubtypeDiff)))
	require.Equal(t, "branch", string(edgeSubtypeFromAlias(EdgeSubtypeBranchAlias)))
	require.Equal(t, "diff", string(edgeSubtypeFromAlias(EdgeSubtypeDiffAlias)))
}

// TestNodeAliasesAreSingleCharacter: node aliases are one character — the size
// optimisation §5.1 promises. (Encoding aliases: encoding_taxonomy_test.go.)
func TestNodeAliasesAreSingleCharacter(t *testing.T) {
	for long, short := range map[string]string{
		string(NodeClassContribution): string(nodeClassToAlias(NodeClassContribution)),
		string(NodeClassSource):       string(nodeClassToAlias(NodeClassSource)),
		string(NodeSubtypeBranches):   string(nodeSubtypeToAlias(NodeSubtypeBranches)),
	} {
		require.Len(t, short, 1, "alias for %q must be one character", long)
	}
}
