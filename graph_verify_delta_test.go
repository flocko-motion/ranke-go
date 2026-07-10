// White-box tests for Graph.VerifyDelta. They live in package ranke
// (not tests/) because proving that pruning actually SKIPS verifyOne
// requires a graph whose trusted subtree would fail verification — a
// state Graph.Add refuses to build, so the map is populated directly.
package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// buildValidClosure returns valid claims forming the chain
// d1 → e1 → em → op (op the initial contributor), all signed. Every
// claim passes verifyOne when its provenance is present.
func buildValidClosure(t *testing.T) (op Contributor, em, e1, d1 Claim) {
	t.Helper()

	opClaim, err := ClaimBuilder{
		Type:    NodeContributor,
		Content: []byte("verifydelta-op@example.com"),
	}.Sign()
	require.NoError(t, err)
	op, err = opClaim.AsContributor()
	require.NoError(t, err)

	em, err = ClaimBuilder{
		Type:        TypeSource("email"),
		Encoding:    EncodingMessage("rfc822"),
		Content:     []byte("From: a@x\r\nTo: b@x\r\n\r\nbody\r\n"),
		Contributor: op,
	}.Sign()
	require.NoError(t, err)

	emEdge, err := NewEdge(EdgeConfig{Reference: em.ID(), Type: TypeDerivation("source")})
	require.NoError(t, err)
	e1, err = ClaimBuilder{
		Type:        TypeEntity("person"),
		Content:     []byte("Alice"),
		Contributor: op,
		Edges:       []Edge{emEdge},
	}.Sign()
	require.NoError(t, err)

	e1Edge, err := NewEdge(EdgeConfig{Reference: e1.ID(), Type: TypeDerivation("source")})
	require.NoError(t, err)
	d1, err = ClaimBuilder{
		Type:        TypeDerivation("summary"),
		Content:     []byte("summary of Alice"),
		Contributor: op,
		Edges:       []Edge{e1Edge},
	}.Sign()
	require.NoError(t, err)

	return op, em, e1, d1
}

// rawGraph builds a *graph directly from claims, bypassing Add's
// atomic-creation check, so a deliberately non-closed graph can be
// constructed for the pruning tests.
func rawGraph(t *testing.T, claims ...Claim) *graph {
	t.Helper()
	g := &graph{
		claims:     make(map[string]*claim),
		referenced: make(map[string]struct{}),
	}
	for _, c := range claims {
		cc, ok := c.(*claim)
		require.True(t, ok, "claim is the concrete *claim type")
		g.claims[cc.node.id.String()] = cc
		for _, e := range cc.edges {
			g.referenced[e.reference.String()] = struct{}{}
		}
	}
	return g
}

// TestVerifyDeltaPrunesAtTrusted is the core proof: on a graph whose
// trusted subtree is NOT loaded (so it would fail Validate), trusting
// that subtree lets VerifyDelta pass — the trusted claims are neither
// verified nor descended into. Same graph, opposite outcomes.
func TestVerifyDeltaPrunesAtTrusted(t *testing.T) {
	op, em, e1, d1 := buildValidClosure(t)

	// Graph holds the delta (d1) and its immediate frontier (e1, op),
	// but NOT em — e1's provenance. verifyOne(e1) therefore cannot
	// resolve e1's edge to em.
	g := rawGraph(t, d1, e1, op)

	// Validate walks into e1 and fails: its edge to em dangles.
	require.Error(t, g.Validate(),
		"Validate must fail — e1's provenance (em) is absent")

	// VerifyDelta trusting e1's whole closure prunes at e1: it is never
	// verified, and em is never needed. Only d1 (the delta) is checked.
	require.NoError(t, g.VerifyDelta(e1.ID(), em.ID(), op.ID()),
		"VerifyDelta must pass — e1 is a trusted anchor, pruned before verifyOne")
}

// TestVerifyDeltaStillVerifiesDelta confirms the flip side: a broken
// claim OUTSIDE the trusted set is still caught. Here the delta claim
// d1 dangles (e1 absent and untrusted), and VerifyDelta must fail.
func TestVerifyDeltaStillVerifiesDelta(t *testing.T) {
	op, _, _, d1 := buildValidClosure(t)

	// Graph holds d1 and op but not e1 — and e1 is NOT trusted.
	g := rawGraph(t, d1, op)

	require.Error(t, g.VerifyDelta(op.ID()),
		"VerifyDelta must fail — d1 is a delta claim with a dangling edge")
}

// TestVerifyDeltaRejectsNonClosedTrustedSet confirms the soundness
// guard: trusting a claim without trusting its references is refused,
// because pruning there would skip an unverified claim.
func TestVerifyDeltaRejectsNonClosedTrustedSet(t *testing.T) {
	op, em, e1, d1 := buildValidClosure(t)

	// Full, valid closure.
	g := NewGraph(op)
	require.NoError(t, g.Add(em))
	require.NoError(t, g.Add(e1))
	require.NoError(t, g.Add(d1))
	require.NoError(t, g.Validate(), "sanity: the closure is valid")

	// Trust e1 but not its provenance (em, op). e1 references both, so
	// the trusted set is not closed under references.
	err := g.VerifyDelta(e1.ID())
	require.Error(t, err, "non-closed trusted set must be refused")
	require.Contains(t, err.Error(), "not closed under references")
}

// TestVerifyDeltaEquivalentOnValidClosure confirms VerifyDelta never
// produces a false negative on an honest closure, for the empty,
// partial-but-closed, and full trusted sets.
func TestVerifyDeltaEquivalentOnValidClosure(t *testing.T) {
	op, em, e1, d1 := buildValidClosure(t)

	g := NewGraph(op)
	require.NoError(t, g.Add(em))
	require.NoError(t, g.Add(e1))
	require.NoError(t, g.Add(d1))
	require.NoError(t, g.Validate())

	// Empty trusted set — verifies everything, same as Validate.
	require.NoError(t, g.VerifyDelta())

	// Closed trusted subset {em, op} (em's only reference is op; op is
	// an initial node). VerifyDelta checks d1 and e1, prunes em, op.
	require.NoError(t, g.VerifyDelta(em.ID(), op.ID()))

	// Full closure trusted — prunes at the head, verifies nothing.
	require.NoError(t, g.VerifyDelta(d1.ID(), e1.ID(), em.ID(), op.ID()))
}
