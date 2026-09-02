package neo4j

// Timestamp-lowering tests: pure functions, no neo4j needed. `R-QTIMEOP` fixes a
// time operand to one spelling, so Cypher's string comparison over a `V-TIME`
// property is its instant comparison — these pin that nothing respells it back.

import (
	"testing"

	"github.com/rankegraph/ranke-go"
	"github.com/stretchr/testify/require"
)

// TestCreatedAtBoundBindsVerbatim: the operand arrives in `V-TIME` form, matching the
// stored property byte for byte, so a bound names its own instant. Respelling it here
// is what would accept the loose forms `R-QTIMEOP` rejects.
func TestCreatedAtBoundBindsVerbatim(t *testing.T) {
	const bound = "2026-01-01T00:00:02.000000000Z"
	for _, tc := range []struct {
		name   string
		cmp    ranke.Comparison
		clause string
	}{
		{"eq", ranke.Comparison{Eq: bound}, "n.created_at = $w0"},
		{"ne", ranke.Comparison{Ne: bound}, "n.created_at <> $w0"},
		{"lt", ranke.Comparison{Lt: bound}, "n.created_at < $w0"},
		{"le", ranke.Comparison{Le: bound}, "n.created_at <= $w0"},
		{"gt", ranke.Comparison{Gt: bound}, "n.created_at > $w0"},
		{"ge", ranke.Comparison{Ge: bound}, "n.created_at >= $w0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, ctr := map[string]any{}, 0
			require.Equal(t, tc.clause, cmpClause("created_at", tc.cmp, "n", p, &ctr))
			require.Equal(t, bound, p["w0"], "the operand is bound as written")
		})
	}
}

// TestCreatedAtOrderingIsNotNumeric: created_at is not an intField, so an ordering
// comparison must stay a string compare — coercing it with toFloat would collapse
// every timestamp to zero and make the whole field compare equal.
func TestCreatedAtOrderingIsNotNumeric(t *testing.T) {
	require.False(t, intField["created_at"])

	p, ctr := map[string]any{}, 0
	clause := cmpClause("created_at", ranke.Comparison{Ge: "2026-01-01T00:00:02.000000000Z"}, "n", p, &ctr)
	require.NotContains(t, clause, "toFloat", "a timestamp compares as text, at its stored width")
}
