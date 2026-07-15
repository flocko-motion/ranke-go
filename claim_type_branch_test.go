package ranke

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the Branch view (claim_type_branch.go): a named
// contribution/branch edge with read-only navigation into its subgraph. Built
// over a small struct archive (two branches, two revisions each) so the
// access paths — Name/Head, HasClaim/GetClaim/GetClaimContent, and the Prev
// revision chain — are exercised without any adapter.

// branchFixture is an archive with two branches, main and dev, each at its
// second revision: v2 overlays v1 via a contribution/diff edge (a revision).
type branchFixture struct {
	ctx                          context.Context
	arc                          Archive
	mainV1, mainV2, devV1, devV2 Claim
}

// revision builds v1 then v2, where v2 is a diff over v1 — one branch advance.
func revision(t *testing.T, root Contributor, name string) (v1, v2 Claim) {
	t.Helper()
	v1 = srcClaim(t, root, name+" v1")
	v2, err := NewClaim(TypeSource("note"), root).
		WithInlineContent([]byte(name + " v2")).
		WithDiff(v1.ID()).
		WithHeight(HeightOf(root, v1)).
		Sign()
	require.NoError(t, err)
	return v1, v2
}

func newBranchFixture(t *testing.T) branchFixture {
	t.Helper()
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	mainV1, mainV2 := revision(t, root, "main")
	devV1, devV2 := revision(t, root, "dev")
	bth := branchTable(t, root, []Claim{mainV2, devV2},
		branchEdge(t, "main", mainV2.ID()),
		branchEdge(t, "dev", devV2.ID()),
	)
	putClaims(t, u, root, mainV1, mainV2, devV1, devV2, bth)
	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)
	return branchFixture{ctx, arc, mainV1, mainV2, devV1, devV2}
}

// TestBranchNameHeadAndEdge: a Branch IS the named contribution/branch edge —
// its Name, Head, Type, and Reference all read off that edge.
func TestBranchNameHeadAndEdge(t *testing.T) {
	f := newBranchFixture(t)
	b, err := f.arc.GetBranch(f.ctx, "main")
	require.NoError(t, err)

	require.Equal(t, "main", b.Name())
	require.True(t, b.Head().Equal(f.mainV2.ID()), "head is the current (v2) revision")
	require.Equal(t, EdgeTypeBranch, b.Type(), "a Branch is a contribution/branch edge")
	require.True(t, b.Reference().Equal(b.Head()), "Head is the edge's Reference")
}

// TestBranchHasClaim: membership is resolved within the branch's own closure —
// the head and its diff predecessor are in; another branch's claims are out.
func TestBranchHasClaim(t *testing.T) {
	f := newBranchFixture(t)
	main, err := f.arc.GetBranch(f.ctx, "main")
	require.NoError(t, err)

	has := func(id Id) bool {
		ok, err := main.HasClaim(f.ctx, id)
		require.NoError(t, err)
		return ok
	}
	require.True(t, has(f.mainV2.ID()), "current head is in the branch")
	require.True(t, has(f.mainV1.ID()), "the diff predecessor is in the closure")
	require.False(t, has(f.devV2.ID()), "another branch's head is not")

	_, err = main.HasClaim(f.ctx, nil)
	require.Error(t, err, "a nil id is a usage error")
}

// TestBranchGetClaim: lookup within the closure returns the claim; a nil or
// out-of-closure id errors.
func TestBranchGetClaim(t *testing.T) {
	f := newBranchFixture(t)
	main, err := f.arc.GetBranch(f.ctx, "main")
	require.NoError(t, err)

	got, err := main.GetClaim(f.ctx, f.mainV2.ID())
	require.NoError(t, err)
	require.True(t, got.ID().Equal(f.mainV2.ID()))

	_, err = main.GetClaim(f.ctx, nil)
	require.Error(t, err, "nil id")
	_, err = main.GetClaim(f.ctx, f.devV2.ID())
	require.Error(t, err, "a claim outside the branch's closure is not found")
}

// TestBranchGetClaimContent: content of a claim in the branch streams back.
func TestBranchGetClaimContent(t *testing.T) {
	f := newBranchFixture(t)
	main, err := f.arc.GetBranch(f.ctx, "main")
	require.NoError(t, err)

	r, err := main.GetClaimContent(f.ctx, f.mainV2.ID())
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, []byte("main v2"), b)
}

// TestBranchPrevWalksRevisions: Prev follows the head's contribution/diff
// edge back one revision, keeping the branch name, and returns nil at the
// first revision.
func TestBranchPrevWalksRevisions(t *testing.T) {
	f := newBranchFixture(t)
	main, err := f.arc.GetBranch(f.ctx, "main")
	require.NoError(t, err)

	prev, err := main.Prev(f.ctx)
	require.NoError(t, err)
	require.NotNil(t, prev, "v2 has a v1 predecessor")
	require.Equal(t, "main", prev.Name(), "the previous revision keeps the branch name")
	require.True(t, prev.Head().Equal(f.mainV1.ID()), "prev points at v1")

	first, err := prev.Prev(f.ctx)
	require.NoError(t, err)
	require.Nil(t, first, "v1 is the first revision — no predecessor")
}

// TestBranchesAreIndependent: the archive exposes both branches, each at its
// own head, with disjoint closures.
func TestBranchesAreIndependent(t *testing.T) {
	f := newBranchFixture(t)

	all, err := f.arc.GetBranches(f.ctx)
	require.NoError(t, err)
	names := map[string]Id{}
	for _, b := range all {
		names[b.Name()] = b.Head()
	}
	require.Len(t, names, 2)
	require.True(t, names["main"].Equal(f.mainV2.ID()))
	require.True(t, names["dev"].Equal(f.devV2.ID()))

	dev, err := f.arc.GetBranch(f.ctx, "dev")
	require.NoError(t, err)
	hasMainV2, err := dev.HasClaim(f.ctx, f.mainV2.ID())
	require.NoError(t, err)
	require.False(t, hasMainV2, "dev's closure does not include main's head")
}
