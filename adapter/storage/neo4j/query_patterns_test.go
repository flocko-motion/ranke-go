package neo4j

// Pattern-lowering tests: pure functions, no neo4j needed. They cover the
// multi-pattern alternation, which the shared RQL corpus does not reach — every
// corpus step carries a single edge/node pattern.

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSplitPatternsPolarity: a leading "-" selects the negative regex, everything
// else the positive one, and an absent polarity is empty rather than a match-all.
func TestSplitPatternsPolarity(t *testing.T) {
	pos, neg := splitPatterns([]string{"relation/*"})
	require.Equal(t, "relation/.*", pos)
	require.Empty(t, neg, "no negative pattern must yield no condition, not one that matches everything")

	pos, neg = splitPatterns([]string{"-contribution/*"})
	require.Empty(t, pos)
	require.Equal(t, "contribution/.*", neg)

	pos, neg = splitPatterns(nil)
	require.Empty(t, pos)
	require.Empty(t, neg)
}

// TestSplitPatternsAlternation: several patterns of one polarity become a single
// regex, not a list. The flat form is what keeps neo4j from mis-binding the
// predicate when it inlines it into a shortest-path expansion.
func TestSplitPatternsAlternation(t *testing.T) {
	pos, neg := splitPatterns([]string{"relation/*", "derivation/*", "-contribution/*", "-source/note"})
	require.Equal(t, "(?:relation/.*)|(?:derivation/.*)", pos)
	require.Equal(t, "(?:contribution/.*)|(?:source/note)", neg)
}

// TestAlternationMatchesEachBranch: the joined regex accepts exactly the union of
// its branches under full-match semantics (Cypher's =~), so grouping each branch
// keeps one alternative from reaching across the join.
func TestAlternationMatchesEachBranch(t *testing.T) {
	pos, _ := splitPatterns([]string{"relation/*", "entity/person"})
	re := regexp.MustCompile("^(?:" + pos + ")$") // =~ is a full match

	for _, want := range []string{"relation/knows", "relation/", "entity/person"} {
		require.Truef(t, re.MatchString(want), "%q must match %q", pos, want)
	}
	for _, reject := range []string{"entity/place", "source/email", "xrelation/knows", "entity/personx"} {
		require.Falsef(t, re.MatchString(reject), "%q must not match %q", pos, reject)
	}
}

// TestGlobToRegexEscapesMetacharacters: a type pattern is literal but for "*" and
// "?", so a regex metacharacter in one must not smuggle in wildcard behaviour.
func TestGlobToRegexEscapesMetacharacters(t *testing.T) {
	dot := regexp.MustCompile("^" + globToRegex("source/a.b") + "$")
	require.True(t, dot.MatchString("source/a.b"))
	require.False(t, dot.MatchString("source/axb"), "'.' must stay a literal dot, not any-char")

	// "?" is the glob's own single-char wildcard, so it does lower to ".".
	quest := regexp.MustCompile("^" + globToRegex("source/a?b") + "$")
	require.True(t, quest.MatchString("source/axb"), "'?' matches exactly one character")
	require.False(t, quest.MatchString("source/ab"), "and not zero")
	require.False(t, quest.MatchString("source/axxb"), "and not two")
}
