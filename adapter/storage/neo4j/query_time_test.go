package neo4j

// Timestamp-lowering tests: pure functions, no neo4j needed. Cypher compares
// created_at as a string while the reference engine compares instants, so what a
// bound is spelled as decides whether the two agree (`R-QCYPHER`).

import (
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	"github.com/stretchr/testify/require"
)

// TestCreatedAtBoundBindsAtStoredPrecision: `V-TIME` fixes the stored form at nine
// fractional digits, and RFC 3339 omits trailing zeros, so an operand naming an exact
// second arrives as "…:02Z" — above the stored "…:02.000000000Z", since "." precedes
// "Z". Spelling every operand as the property is stored is what makes a bound mean
// the instant it names.
func TestCreatedAtBoundBindsAtStoredPrecision(t *testing.T) {
	const stored = "2026-01-01T00:00:02.000000000Z"
	for _, spelling := range []any{
		"2026-01-01T00:00:02Z",                      // RFC3339, no fraction at all
		"2026-01-01T00:00:02.000000000Z",            // already at stored precision
		"2026-01-01T01:00:02+01:00",                 // same instant, another zone
		time.Date(2026, 1, 1, 0, 0, 2, 0, time.UTC), // a time.Time operand
	} {
		p, ctr := map[string]any{}, 0
		clause := cmpClause("created_at", ranke.Comparison{Ge: spelling}, "n", p, &ctr)
		require.Equal(t, "n.created_at >= $w0", clause)
		require.Equal(t, stored, p["w0"], "operand %v must bind at the stored precision", spelling)
	}
}

// TestCreatedAtBindsEveryOperator: the damage was asymmetric — Eq matched nothing,
// Ne and Lt over-included, Ge under-included, while Le and Gt happened to be right.
// All six are pinned, since which ones survive is an accident of the orderings.
func TestCreatedAtBindsEveryOperator(t *testing.T) {
	const loose, stored = "2026-01-01T00:00:02Z", "2026-01-01T00:00:02.000000000Z"
	for _, tc := range []struct {
		name string
		cmp  ranke.Comparison
	}{
		{"eq", ranke.Comparison{Eq: loose}},
		{"ne", ranke.Comparison{Ne: loose}},
		{"lt", ranke.Comparison{Lt: loose}},
		{"le", ranke.Comparison{Le: loose}},
		{"gt", ranke.Comparison{Gt: loose}},
		{"ge", ranke.Comparison{Ge: loose}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, ctr := map[string]any{}, 0
			cmpClause("created_at", tc.cmp, "n", p, &ctr)
			require.Equal(t, stored, p["w0"])
		})
	}

	p, ctr := map[string]any{}, 0
	cmpClause("created_at", ranke.Comparison{In: []any{loose, "not a time"}}, "n", p, &ctr)
	require.Equal(t, []any{stored, "not a time"}, p["w0"],
		"a timestamp member is respelled; a member that is no timestamp passes through")
}

// TestNonTimeFieldsAreLeftAlone: only the properties stored as timestamps are
// respelled. A string field holding something time-shaped is compared as written.
func TestNonTimeFieldsAreLeftAlone(t *testing.T) {
	p, ctr := map[string]any{}, 0
	cmpClause("encoding", ranke.Comparison{Eq: "2026-01-01T00:00:02Z"}, "n", p, &ctr)
	require.Equal(t, "2026-01-01T00:00:02Z", p["w0"])

	// dated is EDTF, which is not an instant and has no stored precision to reach.
	p, ctr = map[string]any{}, 0
	cmpClause("dated", ranke.Comparison{Ge: "2026-01-01"}, "n", p, &ctr)
	require.Equal(t, "2026-01-01", p["w0"])
}
