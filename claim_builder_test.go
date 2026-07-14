package ranke

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the fluent ClaimBuilder API (NewClaim + the
// With* setters) and the value-semantics that make chaining safe. The
// struct-literal form is exercised throughout the other suites; this pins
// the chained form, which is equal public API.

func mustInline(t *testing.T, n Node) []byte {
	t.Helper()
	b, err := n.GetInlineContent()
	require.NoError(t, err)
	return b
}

// TestBuilderFluentChain: every setter in a chain is reflected in the
// built claim.
func TestBuilderFluentChain(t *testing.T) {
	alice := contributor(t)
	at := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)

	c, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("body")).
		WithEncoding("text/markdown").
		WithField("topic", "news").
		WithCreatedAt(at).
		Sign()
	require.NoError(t, err)

	require.Equal(t, "source/note", c.Node().Type())
	require.Equal(t, "text/markdown", c.Node().Encoding())
	v, err := c.Node().GetField("topic")
	require.NoError(t, err)
	require.Equal(t, "news", v)
	require.True(t, c.Node().CreatedAt().Equal(at))
	require.Equal(t, []byte("body"), mustInline(t, c.Node()))
}

// TestBuilderWithTypeOverride: WithType replaces the seeded type.
func TestBuilderWithTypeOverride(t *testing.T) {
	alice := contributor(t)
	c, err := NewClaim(TypeSource("note"), alice).
		WithType(TypeSource("email")).
		WithInlineContent([]byte("x")).
		Sign()
	require.NoError(t, err)
	require.Equal(t, "source/email", c.Node().Type())
}

// TestBuilderWithExternalContent: the external-content setter records hash
// and size without inline bytes.
func TestBuilderWithExternalContent(t *testing.T) {
	alice := contributor(t)
	hash, err := HashContent([]byte("external blob"))
	require.NoError(t, err)

	c, err := NewClaim(TypeSource("blob"), alice).
		WithExternalContent(hash, 13).
		Sign()
	require.NoError(t, err)
	require.True(t, c.Node().IsContentExternal())
	require.Equal(t, uint64(13), c.Node().GetContentSize())
	require.True(t, hash.Equal(c.Node().GetContentHash()))
}

// TestBuilderWithEdges: WithEdges attaches provided edges.
func TestBuilderWithEdges(t *testing.T) {
	alice := contributor(t)
	src := srcClaim(t, alice, "s")
	c, err := NewClaim(TypeEntity("person"), alice).
		WithInlineContent([]byte("Alice")).
		WithEdges(mustDerivEdge(t, src)).
		Sign()
	require.NoError(t, err)
	require.Len(t, c.Edges(EdgeFilterType{Type: "derivation/source"}), 1)
}

// TestBuilderWithFieldImmutable: With* returns a copy — mutating one
// derived builder never touches a sibling built from the same base. This
// is what makes it safe to fan out from a partially-configured builder.
func TestBuilderWithFieldImmutable(t *testing.T) {
	alice := contributor(t)
	base := NewClaim(TypeSource("note"), alice).WithInlineContent([]byte("x"))

	withField := base.WithField("a", "1")

	c1, err := base.Sign()
	require.NoError(t, err)
	c2, err := withField.Sign()
	require.NoError(t, err)

	require.False(t, c1.Node().HasField("a"), "the base builder was not mutated")
	require.True(t, c2.Node().HasField("a"), "the derived builder has the field")
}

// TestBuilderSignedRootContributor: a signed root contributor carries its
// pubkey AS its (inline) content (§5.7) and signs with the matching key.
func TestBuilderSignedRootContributor(t *testing.T) {
	priv, pubkey := ed25519Keys(t)
	c, err := NewClaim(NodeContributor, nil).
		WithInlineContent([]byte("alice")).
		WithPubkey(pubkey).
		WithSigningKey(priv).
		Sign()
	require.NoError(t, err)
	require.True(t, c.IsContributor())
	require.Equal(t, "ed25519-pub", c.ID().Algorithm())
}

// --- claim views --------------------------------------------------------

// TestClaimContributor: a normal claim exposes the contributor it was
// attributed to.
func TestClaimContributor(t *testing.T) {
	alice := identityContributor(t, "op@example.com")
	c := srcClaim(t, alice, "s")
	require.NotNil(t, c.Contributor())
	require.True(t, c.Contributor().ID().Equal(alice.ID()))
}

// TestSignedContributorForwarding: WithSigningKey wraps a Contributor and
// forwards every Claim method to the underlying claim while carrying the
// session key.
func TestSignedContributorForwarding(t *testing.T) {
	alice, priv := newSignedContributor(t)
	wrapped := WithSigningKey(alice, priv)

	require.Equal(t, priv, wrapped.SigningKey(), "carries the session key")
	require.True(t, wrapped.IsContributor())
	require.True(t, wrapped.ID().Equal(alice.ID()), "id forwarded")
	require.Equal(t, "contribution/contributor", wrapped.Node().Type(), "node forwarded")
	require.Empty(t, wrapped.Edges(), "edges forwarded (root has none)")

	enc, err := wrapped.Encode()
	require.NoError(t, err)
	require.NotEmpty(t, enc, "Encode forwarded")

	view, err := wrapped.AsContributor()
	require.NoError(t, err)
	require.True(t, view.ID().Equal(alice.ID()), "AsContributor forwarded")
}

// ============================================================
// Construction guarantees — invariants ClaimBuilder.Sign enforces.
// ============================================================

// --- §3.5 provenance invariant -----------------------------------------

// TestProvenanceRequired: derivation/*, entity/*, and relation/* claims —
// interpretations of existing claims — must cite at least one derivation/*
// edge. The auto-built contribution/contributor edge does NOT satisfy it.
func TestProvenanceRequired(t *testing.T) {
	ctr := identityContributor(t, "agent@example.com")
	for _, typ := range []string{
		TypeDerivation("summary"),
		TypeEntity("person"),
		TypeRelation("likes"),
	} {
		_, err := NewClaim(typ, ctr).WithInlineContent([]byte("...")).Sign()
		require.Error(t, err, "%s without a derivation/* edge must be rejected (§3.5)", typ)
	}
}

// TestProvenanceSatisfied: the same claims build fine once they carry a
// derivation/* edge to a source (the positive control for §3.5).
func TestProvenanceSatisfied(t *testing.T) {
	ctr := identityContributor(t, "agent@example.com")
	source := srcClaim(t, ctr, "the source")
	for _, typ := range []string{
		TypeDerivation("summary"),
		TypeEntity("person"),
		TypeRelation("likes"),
	} {
		_, err := NewClaim(typ, ctr).
			WithInlineContent([]byte("...")).
			WithEdges(mustDerivEdge(t, source)).
			Sign()
		require.NoError(t, err, "%s with a derivation/* edge is valid", typ)
	}
}

// TestSourceCarriesProvenance: a source/* claim needs no derivation/* edge
// (it is an external artifact, not an interpretation of other claims), but
// it is NOT provenance-free — it still carries a contribution/contributor
// edge naming who ingested it. Provenance is universal (§4.5); only the
// initial node is exempt (see TestInitialNodeMayLackProvenance).
func TestSourceCarriesProvenance(t *testing.T) {
	ctr := identityContributor(t, "agent@example.com")
	c, err := NewClaim(TypeSource("note"), ctr).WithInlineContent([]byte("x")).Sign()
	require.NoError(t, err, "source/* builds without a derivation edge")
	require.Len(t, c.Edges(EdgeFilterType{Type: EdgeTypeContributor}), 1,
		"a source claim still carries its contribution/contributor edge — its provenance")
}

// TestInitialNodeMayLackProvenance: a contribution/contributor claim MAY
// have no edges — it is an initial node, provenance-free and valid (§4.5).
// This is a datatype property, not a uniqueness rule: a closure may hold
// several initial nodes (e.g. two merged archives), and all verify. The
// "only the first founding claim" restriction is a write-path concern
// enforced by the Sequencer, not here.
func TestInitialNodeMayLackProvenance(t *testing.T) {
	root := identityContributor(t, "root@example.com")
	require.Empty(t, root.Edges(), "an initial node (root contributor) has no edges")
	require.True(t, root.IsContributor())
}

// TestContentClaimRequiresContributor: every non-initial claim must carry
// provenance. A content claim (source/*) always gets a contribution/
// contributor edge, and the builder refuses one with no contributor.
func TestContentClaimRequiresContributor(t *testing.T) {
	root := identityContributor(t, "root@example.com")
	child := srcClaim(t, root, "x")
	require.NotEmpty(t, child.Edges(), "a non-initial claim always carries provenance")

	_, err := NewClaim(TypeSource("note"), nil).WithInlineContent([]byte("x")).Sign()
	require.Error(t, err, "only the root contribution/contributor may omit a contributor")
}

// --- single-edge cardinality guarantees --------------------------------

// TestClaimRejectsTwoContributors: a claim is attributed to exactly one
// contributor. Attributing to alice auto-builds one contribution/
// contributor edge; a second one (to bob) must be rejected.
func TestClaimRejectsTwoContributors(t *testing.T) {
	alice := identityContributor(t, "alice@example.com")
	bob := identityContributor(t, "bob@example.com")
	extra := mustEdge(t, EdgeConfig{Reference: bob.ID(), Type: EdgeTypeContributor})

	_, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("x")).
		WithEdges(extra). // alice via attribution + bob via explicit edge = two contributors
		Sign()
	require.Error(t, err, "a claim may carry only one contribution/contributor edge")
}

// TestClaimRejectsTwoDiffEdges: a claim overlays at most one predecessor,
// so it may carry only one contribution/diff edge.
func TestClaimRejectsTwoDiffEdges(t *testing.T) {
	alice := identityContributor(t, "alice@example.com")
	pred1 := srcClaim(t, alice, "v1")
	pred2 := srcClaim(t, alice, "v1-alt")
	extraDiff := mustEdge(t, EdgeConfig{Reference: pred2.ID(), Type: EdgeTypeDiff})

	_, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("v2")).
		WithDiff(pred1.ID()). // adds one contribution/diff edge...
		WithEdges(extraDiff). // ...this adds a second
		Sign()
	require.Error(t, err, "a claim may overlay at most one predecessor (one contribution/diff edge)")
}

// --- §5.7 signing guarantees at the builder ----------------------------

// TestSignerMustMatchPubkey: Sign rejects a signer whose public key does
// not match the resolved contributor's pubkey — Bob cannot sign a claim
// attributed to Alice.
func TestSignerMustMatchPubkey(t *testing.T) {
	alice, _ := newSignedContributor(t)
	_, bobPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	_, err = NewClaim(TypeSource("email"), alice).
		WithInlineContent([]byte("hi")).
		Sign(bobPriv) // Bob's key, Alice's contributor
	require.Error(t, err, "signer/contributor pubkey mismatch must be rejected")
}

// TestSignedContributorRequiresSigner: declaring a pubkey without
// supplying the matching signing key is rejected — the data says "signed
// by this key" but the call never provides it.
func TestSignedContributorRequiresSigner(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	pubkey, err := EncodePublicKey(priv.Public())
	require.NoError(t, err)

	_, err = NewClaim(NodeContributor, nil).
		WithInlineContent([]byte("alice")).
		WithPubkey(pubkey).
		// no Sign(priv) — signing key withheld on purpose
		Sign()
	require.Error(t, err, "pubkey-without-signer must be rejected")
}

// ============================================================
// Diff overlay — WithDiff builds a claim that overlays a predecessor,
// restating only what differs (node content/title/fields and/or edges) and
// carrying a contribution/diff edge naming it. Full materialisation (the
// loader applying the diff chain) is exercised where that lands.
// ============================================================

// TestClaimDiffEdgeNamesPredecessor: WithDiff adds exactly one
// contribution/diff edge to the predecessor, coexisting with the auto
// contribution/contributor edge.
func TestClaimDiffEdgeNamesPredecessor(t *testing.T) {
	alice := identityContributor(t, "op@example.com")
	pred := srcClaim(t, alice, "v1")

	c, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("v2")).
		WithDiff(pred.ID()).
		Sign()
	require.NoError(t, err)

	diffs := c.Edges(EdgeFilterType{Type: EdgeTypeDiff})
	require.Len(t, diffs, 1, "exactly one contribution/diff edge")
	require.True(t, diffs[0].Reference().Equal(pred.ID()), "it names the predecessor")
	require.Len(t, c.Edges(EdgeFilterType{Type: EdgeTypeContributor}), 1,
		"the diff edge coexists with the contributor edge")
}

// TestClaimDiffRestatesNodeChanges: a diff restates changed node content
// and fields — the built claim carries the new values verbatim.
func TestClaimDiffRestatesNodeChanges(t *testing.T) {
	alice := identityContributor(t, "op@example.com")
	pred := srcClaim(t, alice, "old body")

	c, err := NewClaim(TypeSource("note"), alice).
		WithDiff(pred.ID()).
		WithInlineContent([]byte("new body")).
		WithField("status", "revised").
		Sign()
	require.NoError(t, err)

	require.Equal(t, []byte("new body"), mustInline(t, c.Node()))
	v, err := c.Node().GetField("status")
	require.NoError(t, err)
	require.Equal(t, "revised", v)
}

// TestClaimDiffAddsEdges: a diff adds new edges alongside the diff edge —
// edges participate in the overlay just as node content does. The added
// edge is NAMED, since a diff claim's edges are overlaid by name (see
// TestClaimDiffRejectsUnnamedEdge). The derivation/* edge also satisfies §3.5.
func TestClaimDiffAddsEdges(t *testing.T) {
	alice := identityContributor(t, "op@example.com")
	pred := srcClaim(t, alice, "v1")
	src := srcClaim(t, alice, "cited source")
	named := mustEdge(t, EdgeConfig{
		Reference: src.ID(),
		Type:      TypeDerivation("source"),
		Fields:    map[string]string{FieldName: "primary-source"},
	})

	c, err := NewClaim(TypeEntity("person"), alice).
		WithDiff(pred.ID()).
		WithInlineContent([]byte("Alice v2")).
		WithEdges(named).
		Sign()
	require.NoError(t, err)

	require.Len(t, c.Edges(EdgeFilterType{Type: EdgeTypeDiff}), 1, "diff edge present")
	require.Len(t, c.Edges(EdgeFilterType{Type: "derivation/source"}), 1, "added edge present")
}

// TestClaimDiffRejectsUnnamedEdge: a diff claim overlays its predecessor by
// re-stating edges, addressed by name. So every edge a diff claim carries
// must be named — EXCEPT contribution/diff and contribution/contributor,
// the singletons (limited to one each, so unambiguous without a name). A
// diff claim carrying an unnamed derivation edge must be rejected.
func TestClaimDiffRejectsUnnamedEdge(t *testing.T) {
	alice := identityContributor(t, "op@example.com")
	pred := srcClaim(t, alice, "v1")
	src := srcClaim(t, alice, "cited source")
	unnamed := mustEdge(t, EdgeConfig{Reference: src.ID(), Type: TypeDerivation("source")}) // no name field

	_, err := NewClaim(TypeEntity("person"), alice).
		WithDiff(pred.ID()).
		WithInlineContent([]byte("Alice v2")).
		WithEdges(unnamed).
		Sign()
	require.Error(t, err, "a diff claim may not carry an unnamed (non-singleton) edge")
}

// TestClaimDiffAllowsNamedEdge: the positive control — the same diff claim
// with the edge NAMED builds fine, since a named edge is overlay-addressable.
func TestClaimDiffAllowsNamedEdge(t *testing.T) {
	alice := identityContributor(t, "op@example.com")
	pred := srcClaim(t, alice, "v1")
	src := srcClaim(t, alice, "cited source")
	named := mustEdge(t, EdgeConfig{
		Reference: src.ID(),
		Type:      TypeDerivation("source"),
		Fields:    map[string]string{FieldName: "primary-source"},
	})

	_, err := NewClaim(TypeEntity("person"), alice).
		WithDiff(pred.ID()).
		WithInlineContent([]byte("Alice v2")).
		WithEdges(named).
		Sign()
	require.NoError(t, err, "a named edge on a diff claim is allowed")
}

// TestClaimDiffIsPartOfId: a diff claim's contribution/diff edge
// participates in its id — the same content diffed over a different
// predecessor is a different claim (content-addressing, §5.2).
func TestClaimDiffIsPartOfId(t *testing.T) {
	alice := identityContributor(t, "op@example.com")
	pred1 := srcClaim(t, alice, "p1")
	pred2 := srcClaim(t, alice, "p2")
	at := pred1.Node().CreatedAt() // pin created_at so only the diff edge differs

	mk := func(pred Claim) Claim {
		c, err := NewClaim(TypeSource("note"), alice).
			WithInlineContent([]byte("same body")).
			WithDiff(pred.ID()).
			WithCreatedAt(at).
			Sign()
		require.NoError(t, err)
		return c
	}
	require.False(t, mk(pred1).ID().Equal(mk(pred2).ID()),
		"diffing a different predecessor yields a different claim id")
}

// TestClaimDiffIsSmallerThanFull: the happy-path payoff — a diff claim
// inherits its predecessor's content instead of re-stating it, so it
// serializes smaller. Same materialised content, fewer bytes on the wire —
// the storage optimisation §4.3 promises ("a storage optimisation carrying
// full provenance").
func TestClaimDiffIsSmallerThanFull(t *testing.T) {
	alice := identityContributor(t, "op@example.com")
	big := bytes.Repeat([]byte("a repeated payload block — "), 200)

	// Predecessor holds the large content.
	pred, err := NewClaim(TypeSource("note"), alice).WithInlineContent(big).Sign()
	require.NoError(t, err)

	// Full: re-states the whole content. Diff: inherits it, restating nothing.
	full, err := NewClaim(TypeSource("note"), alice).WithInlineContent(big).Sign()
	require.NoError(t, err)
	diff, err := NewClaim(TypeSource("note"), alice).WithDiff(pred.ID()).Sign()
	require.NoError(t, err)

	fullBytes, err := full.Encode()
	require.NoError(t, err)
	diffBytes, err := diff.Encode()
	require.NoError(t, err)

	require.Less(t, len(diffBytes), len(fullBytes),
		"a diff inheriting content serializes smaller than re-stating it")
}
