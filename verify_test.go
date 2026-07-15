package ranke

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the closure verifier (verify.go): Graph/Archive
// Verify and the VerifyOption knobs (WithMaxDepth, WithTrusted, WithStopAfter,
// WithOnError, WithExternalContent). Verify runs asynchronously and returns a
// VerificationRun handle — Wait for completion, then read Verified/Failures/Err.

// corruptNode mutates a claim's created_at — a field in the canonical node
// encoding — WITHOUT changing its stored id, so the recomputed H(S(node)) no
// longer matches id(v) and verification fails. White-box (package ranke); the
// public API can't build such a claim, which is the whole point of §5.10.
func corruptNode(t *testing.T, c Claim) {
	t.Helper()
	cc, ok := c.(*claim)
	require.True(t, ok, "claim is the package's concrete type")
	cc.node.createdAt = cc.node.createdAt.Add(time.Hour)
}

// --- honest graphs verify ----------------------------------------------

// TestVerifyIdentityGraph: an honestly-built identity-Sign graph verifies —
// the walk finishes with no failures and no terminal error.
func TestVerifyIdentityGraph(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	require.NoError(t, g.AddClaims(context.Background(), srcClaim(t, root, "hello")))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err(), "no terminal error")
	require.Empty(t, run.Failures(), "identity-Sign closure verifies")
}

// TestVerifySignedGraph: an honestly-built signed graph verifies — the
// per-claim signature check passes across the closure.
func TestVerifySignedGraph(t *testing.T) {
	alice, alicePriv := newSignedContributor(t)
	g := newGraph(t, alice)
	src, err := NewClaim(TypeSource("email"), alice).
		WithInlineContent([]byte("From: alice\r\n\r\nhi")).
		WithHeight(HeightOf(alice)).
		Sign(alicePriv)
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(context.Background(), src))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures(), "signed closure verifies")
}

// TestVerifyCountsEveryClaim: the run reports every claim in the closure as
// verified — here the root contributor plus the source.
func TestVerifyCountsEveryClaim(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	require.NoError(t, g.AddClaims(context.Background(), srcClaim(t, root, "hello")))

	run := g.Verify()
	run.Wait()
	require.Empty(t, run.Failures())
	require.Equal(t, 2, run.Verified(), "root + source both verified")
}

// chainGraph builds root ← a(source) ← b(entity) ← c(entity), a 4-claim
// closure two levels deep, for the depth/trusted options.
func chainGraph(t *testing.T) (Graph, Contributor, Claim, Claim, Claim) {
	t.Helper()
	root := contributor(t)
	g := newGraph(t, root)
	a := srcClaim(t, root, "a")
	require.NoError(t, g.AddClaims(context.Background(), a))
	b := entityClaim(t, root, "person", "b", a)
	require.NoError(t, g.AddClaims(context.Background(), b))
	c := entityClaim(t, root, "object", "c", b)
	require.NoError(t, g.AddClaims(context.Background(), c))
	return g, root, a, b, c
}

// --- WithMaxDepth -------------------------------------------------------

// TestVerifyWithMaxDepth: bounding the depth prunes the deepest claims, so
// fewer are verified than an unbounded walk.
func TestVerifyWithMaxDepth(t *testing.T) {
	g, _, _, _, _ := chainGraph(t)

	full := g.Verify()
	full.Wait()
	require.Empty(t, full.Failures())
	require.Equal(t, 4, full.Verified(), "unbounded walk reaches root, a, b, c")

	shallow := g.Verify(WithMaxDepth(1))
	shallow.Wait()
	require.Empty(t, shallow.Failures())
	require.Less(t, shallow.Verified(), full.Verified(),
		"maxDepth stops the walk short of the deepest claim")
}

// --- WithTrusted --------------------------------------------------------

// TestVerifyWithTrusted: a claim reported trusted is pruned — skipped, not
// re-verified — so the verified count drops.
func TestVerifyWithTrusted(t *testing.T) {
	g, _, a, _, _ := chainGraph(t)

	full := g.Verify()
	full.Wait()

	trusted := g.Verify(WithTrusted(func(id Id) bool { return id.Equal(a.ID()) }))
	trusted.Wait()
	require.Empty(t, trusted.Failures())
	require.Less(t, trusted.Verified(), full.Verified(),
		"a trusted claim (and its pruned subtree) is not re-verified")
}

// --- WithMaxClaims ------------------------------------------------------

// TestVerifyWithMaxClaims: a hard work cap stops the walk after n claims
// have been processed, independent of depth.
func TestVerifyWithMaxClaims(t *testing.T) {
	g, _, _, _, _ := chainGraph(t) // 4 claims: root, a, b, c

	full := g.Verify()
	full.Wait()
	require.Equal(t, 4, full.Verified())

	capped := g.Verify(WithMaxClaims(2))
	capped.Wait()
	require.Equal(t, 2, capped.Verified(), "the walk stops after 2 claims are processed")
}

// --- WithCreatedAfter ---------------------------------------------------

// TestVerifyWithCreatedAfter: claims older than the bound are pruned. The
// walk descends toward older references, so this bounds verification to a
// recent window — here the older root contributor is skipped.
func TestVerifyWithCreatedAfter(t *testing.T) {
	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	rootClaim, err := NewClaim(NodeContributor, nil). // empty content = identity-Sign
								WithCreatedAt(old).
								Sign()
	require.NoError(t, err)
	root, err := rootClaim.AsContributor(context.Background(), nil)
	require.NoError(t, err)

	g := newGraph(t, root)
	em, err := NewClaim(TypeSource("note"), root).
		WithInlineContent([]byte("recent")).
		WithCreatedAt(recent).
		WithHeight(HeightOf(root)).
		Sign()
	require.NoError(t, err)
	require.NoError(t, g.AddClaims(context.Background(), em))

	full := g.Verify()
	full.Wait()
	require.Equal(t, 2, full.Verified())

	bounded := g.Verify(WithCreatedAfter(recent))
	bounded.Wait()
	require.Empty(t, bounded.Failures())
	require.Less(t, bounded.Verified(), full.Verified(),
		"the older root is pruned by the created_at bound")
}

// --- WithOnError --------------------------------------------------------

// TestVerifyWithOnError: the callback fires for each failing claim, carrying
// the offending id.
func TestVerifyWithOnError(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	bad := srcClaim(t, root, "will be corrupted")
	require.NoError(t, g.AddClaims(context.Background(), bad))
	corruptNode(t, bad)

	var seen []Failure
	run := g.Verify(WithOnError(func(f Failure) { seen = append(seen, f) }))
	run.Wait()

	require.NotEmpty(t, seen, "WithOnError fires on the corrupted claim")
	require.NotEmpty(t, run.Failures(), "the failure is also recorded on the run")
	var found bool
	for _, f := range seen {
		if f.ID.Equal(bad.ID()) {
			found = true
		}
	}
	require.True(t, found, "the reported failure names the corrupted claim")
}

// --- WithStopAfter ------------------------------------------------------

// TestVerifyWithStopAfter: the walk halts once the failure budget is hit,
// so no more than that many failures are recorded.
func TestVerifyWithStopAfter(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	em := srcClaim(t, root, "seed")
	require.NoError(t, g.AddClaims(context.Background(), em))
	e1 := entityClaim(t, root, "person", "a", em)
	e2 := entityClaim(t, root, "object", "b", em)
	require.NoError(t, g.AddClaims(context.Background(), e1, e2))
	corruptNode(t, e1) // both open heads fail
	corruptNode(t, e2)

	run := g.Verify(WithStopAfter(1))
	run.Wait()
	require.Len(t, run.Failures(), 1, "the walk stops after the first failure")
	require.True(t, run.Done())
}

// --- WithExternalContent (Archive) -------------------------------------

// TestVerifyExternalContentToggle: external content is skipped by default
// (may be huge) and verified only under WithExternalContent — so a corrupt
// external blob passes the default walk but fails when external content is on.
func TestVerifyExternalContentToggle(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)

	realBlob := []byte("the real external payload bytes")
	hash, err := HashContent(realBlob)
	require.NoError(t, err)
	ext, err := NewClaim(TypeSource("blob"), root).
		WithExternalContent(hash, uint64(len(realBlob))).
		WithHeight(HeightOf(root)).
		Sign()
	require.NoError(t, err)
	bth := branchTable(t, root, []Claim{ext}, branchEdge(t, "main", ext.ID()))
	putClaims(t, u, root, ext, bth)

	// Store a CORRUPT blob under the hash — same length, one byte flipped.
	corruptBlob := make([]byte, len(realBlob))
	copy(corruptBlob, realBlob)
	corruptBlob[0] ^= 0xFF
	require.NoError(t, u.PutContents(ctx, []ContentBlob{{Hash: hash, Content: corruptBlob}}))

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	// Default: external content not fetched → the corruption is not seen.
	def, err := arc.Verify(ctx)
	require.NoError(t, err)
	def.Wait()
	require.Empty(t, def.Failures(), "external content skipped by default")

	// With external content: the verifying reader catches the mismatch.
	ext2, err := arc.Verify(ctx, WithExternalContent())
	require.NoError(t, err)
	ext2.Wait()
	require.NotEmpty(t, ext2.Failures(), "corrupt external content is detected when verified")
}

// --- Branch.Verify ------------------------------------------------------

// TestVerifyBranch: a branch verifies its subgraph from its head claim.
func TestVerifyBranch(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "seed")
	bth := branchTable(t, root, []Claim{em}, branchEdge(t, "main", em.ID()))
	putClaims(t, u, root, em, bth)

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)
	b, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err)

	run, err := b.Verify(ctx)
	require.NoError(t, err)
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures(), "the branch subgraph verifies from its head")
}

// --- height invariant (§4.1) -------------------------------------------

// TestVerifyRejectsWrongHeight: the verifier re-derives height == 1 + max(refs)
// and rejects a claim whose stored height disagrees — even though its id is
// internally consistent (Sign accepts any nonzero height on a referencing
// claim). The gate lives in the verifier, not the builder.
func TestVerifyRejectsWrongHeight(t *testing.T) {
	root := contributor(t)
	g := newGraph(t, root)
	bad, err := NewClaim(TypeSource("note"), root).
		WithInlineContent([]byte("body")).
		WithHeight(99). // correct is 1 — the only reference is the height-0 contributor
		Sign()
	require.NoError(t, err, "Sign does not itself check height against refs")
	require.NoError(t, g.AddClaims(context.Background(), bad))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	var found bool
	for _, f := range run.Failures() {
		if f.ID.Equal(bad.ID()) {
			require.ErrorIs(t, f.Err, errHeightMismatch)
			found = true
		}
	}
	require.True(t, found, "the wrong-height claim is reported")
}
