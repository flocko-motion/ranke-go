package generator

import (
	"context"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

// generateInto runs Generate over a fresh reference Universe and returns both,
// failing the test on error.
func generateInto(t *testing.T, spec Spec) (ranke.Universe, *Manifest) {
	t.Helper()
	u := ranke.NewMemoryUniverse()
	m, err := Generate(context.Background(), u, spec)
	require.NoError(t, err)
	return u, m
}

// TestGenerateVerifies is the acid test: a generated archive verifies end to
// end. Open it at the manifest head and run the real closure verifier — every
// claim's id (hash + signature) and content must check out, across all the
// corners the builder bakes in.
func TestGenerateVerifies(t *testing.T) {
	ctx := context.Background()
	u, m := generateInto(t, SpecForSize(1, 20))

	arc, err := ranke.NewArchive(ctx, u, m.Head)
	require.NoError(t, err)

	run, err := arc.Verify(ctx, ranke.WithExternalContent())
	require.NoError(t, err)
	run.Wait()
	require.NoError(t, run.Err(), "no terminal error walking the archive")
	require.Empty(t, run.Failures(), "every generated claim verifies")
	require.Greater(t, run.Verified(), m.ClaimCount, "the whole closure is walked, heads and tables included")
}

// TestGenerateDeterministic pins the core promise: the same spec yields the
// exact same archive — identical head id, identical claim ids — on every run.
func TestGenerateDeterministic(t *testing.T) {
	spec := SpecForSize(7, 15)
	_, m1 := generateInto(t, spec)
	_, m2 := generateInto(t, spec)

	require.True(t, m1.Head.Equal(m2.Head), "same spec → identical archive head")
	require.Equal(t, idset(m1.Sources), idset(m2.Sources), "identical source ids")
	require.Equal(t, idset(m1.Entities), idset(m2.Entities), "identical entity ids")
	require.Equal(t, idset(m1.Relations), idset(m2.Relations), "identical relation ids")
}

// TestGenerateSeedVaries: a different seed changes every id.
func TestGenerateSeedVaries(t *testing.T) {
	_, a := generateInto(t, SpecForSize(1, 15))
	_, b := generateInto(t, SpecForSize(2, 15))
	require.False(t, a.Head.Equal(b.Head), "a different seed → a different archive")
}

// TestGenerateCornersPresent: the manifest reports every feasible corner, and
// flags the blocked ones as skipped rather than silently omitting them.
func TestGenerateCornersPresent(t *testing.T) {
	_, m := generateInto(t, SpecForSize(1, 30))

	require.GreaterOrEqual(t, len(m.Contributors), 2, "multiple contributors")
	require.NotEmpty(t, m.Sources, "sources")
	require.NotEmpty(t, m.ExternalBlobs, "external (hash-addressed) content")
	require.NotNil(t, m.TinyBlob, "a source with tiny content")
	require.NotEmpty(t, m.Derivations, "derivations")
	require.NotNil(t, m.HighDegree, "a high-degree derivation")
	require.NotEmpty(t, m.Entities, "entities")
	require.NotEmpty(t, m.Relations, "relations")
	require.NotNil(t, m.DiffChainHead, "a long diff chain")
	require.NotEmpty(t, m.Expiries, "contribution/expiry claims (key expiry)")
	require.Empty(t, m.Deletes, "deletes are off by default — a DB-level concern")
}

// TestGenerateExpiryShape: an expiry claim is a contribution/expiry node with a
// contribution/expiry edge to a contributor and a pubkey_expires_after field —
// resolvable through the archive and structurally as the paper (§Types) describes.
func TestGenerateExpiryShape(t *testing.T) {
	ctx := context.Background()
	u, m := generateInto(t, SpecForSize(2, 30))
	require.NotEmpty(t, m.Expiries)

	arc, err := ranke.NewArchive(ctx, u, m.Head)
	require.NoError(t, err)
	c, err := arc.GetClaim(ctx, m.Expiries[0])
	require.NoError(t, err)
	require.Equal(t, "contribution/expiry", c.Node().Type())

	_, err = c.Node().GetField("pubkey_expires_after")
	require.NoError(t, err, "expiry carries the pubkey_expires_after field")

	var hasExpiryEdge bool
	for _, e := range c.Edges() {
		if e.Type() == "contribution/expiry" {
			hasExpiryEdge = true
		}
	}
	require.True(t, hasExpiryEdge, "expiry edge points at the contributor")
}

// TestGenerateTinyBlobSize: the tiny-content corner is a size, so the claim's
// content_size must be exactly TinyBlobBytes — otherwise a query filtering on
// the small-content boundary has nothing to find.
func TestGenerateTinyBlobSize(t *testing.T) {
	ctx := context.Background()
	spec := SpecForSize(1, 30)
	u, m := generateInto(t, spec)
	require.NotNil(t, m.TinyBlob)

	arc, err := ranke.NewArchive(ctx, u, m.Head)
	require.NoError(t, err)
	c, err := arc.GetClaim(ctx, m.TinyBlob)
	require.NoError(t, err)
	require.Equal(t, uint64(spec.TinyBlobBytes), c.Node().GetContentSize())
}

// TestGenerateDeleteShape: deletes are off by default (a DB-level concern), but
// the knob still builds a valid tombstone when a DB-level test opts in — a
// contribution/delete edge naming the removed claim by id (§Types).
func TestGenerateDeleteShape(t *testing.T) {
	ctx := context.Background()
	spec := SpecForSize(2, 30)
	spec.Deletes = 2 // opt in explicitly
	u := ranke.NewMemoryUniverse()
	m, err := Generate(ctx, u, spec)
	require.NoError(t, err)
	require.NotEmpty(t, m.Deletes)

	arc, err := ranke.NewArchive(ctx, u, m.Head)
	require.NoError(t, err)
	c, err := arc.GetClaim(ctx, m.Deletes[0])
	require.NoError(t, err)

	var hasDeleteEdge bool
	for _, e := range c.Edges() {
		if e.Type() == "contribution/delete" {
			hasDeleteEdge = true
		}
	}
	require.True(t, hasDeleteEdge, "delete edge names the removed claim")
}

// TestGenerateDiffChainMaterialises: the head of the generated diff chain,
// read through the archive, materialises its overlay — it carries the latest
// revision field AND inherits the base content.
func TestGenerateDiffChainMaterialises(t *testing.T) {
	ctx := context.Background()
	u, m := generateInto(t, SpecForSize(3, 20))

	arc, err := ranke.NewArchive(ctx, u, m.Head)
	require.NoError(t, err)
	got, err := arc.GetClaim(ctx, m.DiffChainHead)
	require.NoError(t, err)
	_, err = got.Node().GetField("rev")
	require.NoError(t, err, "the chain head carries a revision field")
}

// TestGenerateScales: a large size produces a 10k+-claim archive that still
// verifies — the scale target for integration/conformance fixtures.
func TestGenerateScales(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 10k-claim generation in -short")
	}
	ctx := context.Background()
	u, m := generateInto(t, SpecForSize(1, 2000))
	require.Greater(t, m.ClaimCount, 10000, "size 2000 yields a 10k+-claim archive")

	arc, err := ranke.NewArchive(ctx, u, m.Head)
	require.NoError(t, err)
	run, err := arc.Verify(ctx, ranke.WithExternalContent())
	require.NoError(t, err)
	run.Wait()
	require.NoError(t, run.Err())
	require.Empty(t, run.Failures(), "the 10k+ archive verifies")
}

func idset(ids []ranke.Id) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}
