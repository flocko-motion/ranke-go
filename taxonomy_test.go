package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the wire aliases (§5.1). To optimise encoding
// size the reserved vocabulary has short forms — a class abbreviates to
// one character, a subtype likewise — and "an alias is semantically
// identical to its long form." That identity is only safe if the mapping
// is a bijection (every long form has exactly one alias and back), the
// aliases don't collide (a duplicate would make decoding ambiguous —
// silent corruption), and open-vocabulary values pass through untouched.
// These pin all three, per namespace, directly on the mapping tables.

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
		NodeSubtypeBranch:      NodeSubtypeBranchAlias,
		NodeSubtypeBranches:    NodeSubtypeBranchesAlias,
		NodeSubtypeDiff:        NodeSubtypeDiffAlias,
		NodeSubtypeHead:        NodeSubtypeHeadAlias,
	}, nodeSubtypeToAlias, nodeSubtypeFromAlias, NodeSubtype("email")) // open vocabulary
}

func TestEncodingClassAliases(t *testing.T) {
	checkAliasRoundTrip(t, map[EncodingClass]EncodingClass{
		encApplication: encApplicationAlias,
		encAudio:       encAudioAlias,
		encExample:     encExampleAlias,
		encFont:        encFontAlias,
		encImage:       encImageAlias,
		encMessage:     encMessageAlias,
		encModel:       encModelAlias,
		encMultipart:   encMultipartAlias,
		encText:        encTextAlias,
		encVideo:       encVideoAlias,
	}, encodingClassToAlias, encodingClassFromAlias, EncodingClass("chemical"))
}

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
	}, edgeSubtypeToAlias, edgeSubtypeFromAlias, EdgeSubtype("transcription")) // open vocabulary
}

// TestAliasesAreSingleCharacter: every reserved class/subtype alias is a
// single character — the size optimisation §5.1 promises. (Field aliases
// use the "." namespace and are a separate shape.)
func TestAliasesAreSingleCharacter(t *testing.T) {
	for long, short := range map[string]string{
		string(NodeClassContribution): string(nodeClassToAlias(NodeClassContribution)),
		string(NodeClassSource):       string(nodeClassToAlias(NodeClassSource)),
		string(NodeSubtypeBranches):   string(nodeSubtypeToAlias(NodeSubtypeBranches)),
		string(encMultipart):          string(encodingClassToAlias(encMultipart)),
		string(EdgeSubtypePrune):      string(edgeSubtypeToAlias(EdgeSubtypePrune)),
	} {
		require.Len(t, short, 1, "alias for %q must be one character", long)
	}
}
