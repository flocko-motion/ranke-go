package ranke

import (
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// Confinement decided by branch tag rather than by sweeping the closure. The two
// must answer identically: which way membership was established is a storage
// tactic, and a capability that changed results would be the bug this introduces.

// untagged presents a Universe as one keeping no tags, so the same graph can be
// read both ways — by tag, and by the sweep a byte store falls back to.
type untagged struct{ Universe }

func (u untagged) Capabilities() Capabilities {
	c := u.Universe.Capabilities()
	c.Tags = false
	return c
}

// taggedBranch builds a branch over two revisions and stamps it as the tagger
// would — each member valued with the branch-table height it joined at:
//
//	rev 1 (table height 5): rootA(0) ← s1(1)
//	rev 2 (table height 9): s2(2) → s1
//
// It returns the Universe and the claims, so a test can scope to either revision.
func taggedBranch(t *testing.T) (Universe, Claim, Claim, Claim) {
	t.Helper()
	ctx := context.Background()
	u := NewMemoryUniverse()
	rootA := contributor(t)
	s1 := srcClaim(t, rootA, "joined at revision 1")
	s2 := entityClaim(t, rootA, "person", "joined at revision 2", s1)
	for _, c := range []Claim{rootA, s1, s2} {
		require.NoError(t, PutClaim(ctx, u, c))
	}

	key := BranchTagKey("a")
	tag := func(c Claim, at int) map[string]string { return map[string]string{key: strconv.Itoa(at)} }
	// The tagger prunes at an already-tagged claim, so the revision a member first
	// entered at is the one that sticks: rootA and s1 keep 5.
	require.NoError(t, u.SetClaimsTags(ctx, nil, map[string]map[string]string{
		rootA.ID().String(): tag(rootA, 5),
		s1.ID().String():    tag(s1, 5),
		s2.ID().String():    tag(s2, 9),
	}))
	return u, rootA, s1, s2
}

// TestConfineUsesTagsWhenAvailable: a Tags-capable Universe whose branch is tagged
// answers membership from the tag, and never sweeps. Asserted on the confinement
// itself, since both paths return the same claims — the point is which was built.
func TestConfineUsesTagsWhenAvailable(t *testing.T) {
	u, _, s1, _ := taggedBranch(t)
	scope := Scope{Head: s1.ID(), Branch: "a", Height: 5}

	conf, err := confine(context.Background(), u, scope)
	require.NoError(t, err)
	require.NotNil(t, conf)
	require.Equal(t, BranchTagKey("a"), conf.tagKey, "a tagged branch is confined by tag")
	require.Empty(t, conf.members, "and so never sweeps the closure")
}

// TestConfineSweepsWithoutTagCapability: the same graph behind a store reporting no
// tags falls back to the sweep. Confinement may not depend on a capability — a
// layer that cannot answer must not be asked to.
func TestConfineSweepsWithoutTagCapability(t *testing.T) {
	u, _, s1, _ := taggedBranch(t)
	scope := Scope{Head: s1.ID(), Branch: "a", Height: 5}

	conf, err := confine(context.Background(), untagged{u}, scope)
	require.NoError(t, err)
	require.NotNil(t, conf)
	require.Empty(t, conf.tagKey, "no tags to read")
	require.NotEmpty(t, conf.members, "so the closure is swept instead")
}

// TestConfineSweepsWhenBranchUntagged: tagging is an accelerator a read may not
// depend on, and an absent tag reads as non-membership. A Tags-capable store whose
// branch was never tagged must therefore sweep, not answer empty.
func TestConfineSweepsWhenBranchUntagged(t *testing.T) {
	u, a := twoBranches(t) // built with PutClaim only — never tagged
	scope := Scope{Head: a["sA"].ID(), Branch: "a", Height: 5}

	conf, err := confine(context.Background(), u, scope)
	require.NoError(t, err)
	require.NotNil(t, conf)
	require.Empty(t, conf.tagKey, "an untagged branch cannot be confined by tag")
	require.NotEmpty(t, conf.members)
}

// TestConfineTagAndSweepAgree is the property that matters: the same query over the
// same graph, once where tags decide membership and once where the closure is
// swept, at both revisions. Same answers, whichever way.
func TestConfineTagAndSweepAgree(t *testing.T) {
	u, rootA, s1, s2 := taggedBranch(t)

	for _, tc := range []struct {
		name  string
		scope Scope
		want  map[string]bool
	}{
		{"revision 1", Scope{Head: s1.ID(), Branch: "a", Height: 5}, idsOf(s1, rootA)},
		{"revision 2", Scope{Head: s2.ID(), Branch: "a", Height: 9}, idsOf(s2, s1, rootA)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := Query{Select: Select{Branch: "a", Head: tc.scope.Head}}
			byTag := reachedUnder(t, u, q, tc.scope)
			bySweep := reachedUnder(t, untagged{u}, q, tc.scope)

			require.Equal(t, tc.want, byTag, "tag path")
			require.Equal(t, byTag, bySweep, "the two membership tactics must agree")
		})
	}
}

// TestConfineTagIsPointInTime: at revision 1 the branch does not yet hold s2, and
// the tag says so — it joined at height 9, above the scope's 5. Reading the tag
// without that comparison would admit it, since the tag is present either way.
func TestConfineTagIsPointInTime(t *testing.T) {
	ctx := context.Background()
	u, rootA, s1, s2 := taggedBranch(t)
	scope := Scope{Head: s1.ID(), Branch: "a", Height: 5}

	conf, err := confine(ctx, u, scope)
	require.NoError(t, err)
	require.Equal(t, BranchTagKey("a"), conf.tagKey)

	// Read back through the Universe: tags live there, not on the claim a caller
	// built, and admits reads the tag off the claim the walk already holds.
	stored := func(c Claim) Claim {
		got, err := GetClaim(ctx, u, c.ID())
		require.NoError(t, err)
		return got
	}
	require.True(t, conf.admits(stored(s1)), "joined at 5, the scope's own height")
	require.True(t, conf.admits(stored(rootA)))
	require.False(t, conf.admits(stored(s2)), "tagged for the branch, but joined above the scope's height")
}
