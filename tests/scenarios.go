// package: tests / conformance
// type:    test
// job:     multi-stage scenario stories that write through the Sequencer, re-read via immutable
// snapshots, and verify survival
// limits:  no public entry point; driven by IntegrationTest (-> tests/integration.go)
package tests

import (
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

// hasClaim reports whether id is in the archive's closure (a re-read against
// the current committed snapshot).
func hasClaim(t *testing.T, f *fixture, arc ranke.Archive, id ranke.Id) bool {
	t.Helper()
	ok, err := arc.HasClaim(f.ctx, id)
	require.NoError(t, err, "HasClaim")
	return ok
}

// --- Scenario: AliceEmail ---
//
// Paper §1 — "A file exists, attributed to Alice by its headers, that appears
// to be a copy of an email to Bob in which Alice claims to like apples." A
// single source/email claim is written and must survive a re-read.
func runAliceEmail(t *testing.T, f *fixture) {
	t.Logf("Scenario: a single email is ingested as a source claim.")

	op := f.self
	t.Logf("[Stage 1] write Alice's email as a source/email claim")
	em := f.email(t, op,
		"alice@example.com", "bob@example.com",
		"Bob, just so you know, I really do like apples.\r\n— Alice\r\n")
	t.Logf("    email id: %s", em.ID().String()[:16]+"…")
	head := f.write(t, em)

	t.Logf("[Verify] re-read the committed snapshot, walk the closure, validate")
	arc := f.snapshot(t)
	mh := f.mainHead(t, arc)
	require.True(t, mh.Equal(em.ID()),
		"a single-claim batch is not wrapped — branch main points straight at the email")
	require.True(t, hasClaim(t, f, arc, em.ID()), "email reachable after commit")
	require.True(t, hasClaim(t, f, arc, op.ID()), "contributor reachable after commit")
	f.validate(t, head)

	t.Logf("[Verify] re-read again: the head id is stable across snapshots")
	arc2 := f.snapshot(t)
	require.True(t, f.mainHead(t, arc2).Equal(mh), "head id stable across re-read")
}

// --- Scenario: MultiHeadAutoConsolidates ---
//
// Two unrelated claims added in one batch leave the graph multi-headed; the
// Sequencer wraps every open head in a single contribution/head claim (§4.6),
// so the branch head is that consolidation and both originals are reachable.
func runMultiHeadAutoConsolidates(t *testing.T, f *fixture) {
	t.Logf("Scenario: a multi-headed batch auto-consolidates on commit.")

	op := f.self
	em := f.email(t, op, "alice@example.com", "bob@example.com", "seed\r\n")
	e1 := f.entity(t, op, "person", "Alice", em)
	e2 := f.entity(t, op, "object", "apples", em)
	t.Logf("    2 open heads: %s, %s",
		e1.ID().String()[:16]+"…", e2.ID().String()[:16]+"…")

	head := f.write(t, em, e1, e2)
	arc := f.snapshot(t)
	mh := f.mainHead(t, arc)

	headClaim, err := arc.GetClaim(f.ctx, mh)
	require.NoError(t, err, "GetClaim(branch head)")
	require.Equal(t, "contribution/head", headClaim.Node().Type(),
		"branch head is a contribution/head consolidation")

	// The consolidation must wrap every original open head.
	refs := map[string]bool{}
	for _, e := range headClaim.Edges() {
		if e.Type() == "contribution/head" {
			refs[e.Reference().String()] = true
		}
	}
	require.True(t, refs[e1.ID().String()], "consolidation wraps entity1")
	require.True(t, refs[e2.ID().String()], "consolidation wraps entity2")

	require.True(t, hasClaim(t, f, arc, e1.ID()), "entity1 reachable")
	require.True(t, hasClaim(t, f, arc, e2.ID()), "entity2 reachable")
	require.True(t, hasClaim(t, f, arc, em.ID()), "shared source reachable")
	f.validate(t, head)
	t.Logf("    ✓ contribution/head wraps both original heads")
}

// --- Scenario: SuccessiveAddsAccumulate ---
//
// Two successive AddClaims commits must both survive: the Sequencer folds the
// prior main head into the new one, so the branch closure accumulates rather
// than dropping the earlier write (Q1 — no lost writes).
func runSuccessiveAddsAccumulate(t *testing.T, f *fixture) {
	t.Logf("Scenario: successive commits accumulate on the branch.")

	op := f.self
	emA := f.email(t, op, "alice@example.com", "bob@example.com", "first\r\n")
	f.write(t, emA)
	t.Logf("    committed emA: %s", emA.ID().String()[:16]+"…")

	emB := f.email(t, op, "alice@example.com", "bob@example.com", "second\r\n")
	head := f.write(t, emB)
	t.Logf("    committed emB: %s", emB.ID().String()[:16]+"…")

	arc := f.snapshot(t)
	require.True(t, hasClaim(t, f, arc, emA.ID()), "first write survived the second commit")
	require.True(t, hasClaim(t, f, arc, emB.ID()), "second write present")
	require.True(t, hasClaim(t, f, arc, op.ID()), "contributor reachable")
	f.validate(t, head)
	t.Logf("    ✓ both writes reachable through the folded head")
}
