package neo4j

// orderTerm's compare: temporal branch: a pure function, no neo4j needed.

import (
	"testing"

	"github.com/rankegraph/ranke-go"
	"github.com/stretchr/testify/require"
)

// TestOrderTermTemporalUsesTheProjection: compare: temporal on dated sorts by the
// reserved datedMidProperty, projected at write time (R-QTEMPORAL) — not by
// toFloat(n.dated), which cannot read EDTF.
func TestOrderTermTemporalUsesTheProjection(t *testing.T) {
	got := orderTerm(ranke.OrderKey{Field: "dated", Compare: ranke.CompareTemporal}, "n")
	require.Equal(t, "n."+datedMidProperty+" IS NULL, n."+datedMidProperty, got)

	gotDesc := orderTerm(ranke.OrderKey{Field: "dated", Compare: ranke.CompareTemporal, Dir: ranke.SortDesc}, "n")
	require.Equal(t, "n."+datedMidProperty+" IS NULL, n."+datedMidProperty+" DESC", gotDesc)
}

// TestDatedMidPropertyIsReserved: the projection can never collide with a user's
// own extension field of the same name.
func TestDatedMidPropertyIsReserved(t *testing.T) {
	require.True(t, len(datedMidProperty) > 0 && datedMidProperty[0:1] == ranke.ReservedPrefix)
}
