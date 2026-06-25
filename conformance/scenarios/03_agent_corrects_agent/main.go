// Conformance scenario 03 — agentB corrects agentA via contradiction.
//
// The Ranke-Graph is append-only (§5.4): you don't delete a wrong
// claim, you ADD claims that contradict it. This scenario shows the
// pattern in practice.
//
// agentA reads a short email and infers "Alice and Bob are siblings"
// — encoded as a relation/sibling claim with conviction=+1.0. A month
// later agentB reviews the same source, realises Bob is Alice's
// employer (not her brother), and issues TWO new claims:
//
//   1. The SAME (Alice, sibling, Bob) relation, by agentB, with
//      conviction=-1.0 — a structural negation of agentA's claim.
//   2. A new (Alice, employed_by, Bob) relation with conviction=+1.0
//      — the corrected reading.
//
// Both extractions remain in U. Consumers resolve the contradiction
// based on contributor trust + recency + their own policy. The
// graph documents the disagreement, it does not decide.
//
// Maps to spec §2.2 ("contradictions in the evidence base are
// themselves evidence") and §3.5 (conviction values).

// package: main / scenario
// type:    cmd
// job:     build & persist the scenario-03 agent-corrects-agent data bundle
// limits:  doesn't verify variant reproductions; that's the run.sh harness (-> conformance/helpers)
package main

import (
	"context"
	"path/filepath"
	"time"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/sequencer/file"
	"github.com/flocko-motion/ranke-go/adapter/storage/fs"
	"github.com/flocko-motion/ranke-go/conformance/helpers"
)

func must[T any](v T, rest ...any) T { return helpers.Must(v, rest...) }

const expectedMainHead = "bciqi4cldyiqgmwbwpjbt4hwyg4yyq5rgcpcaqiue3bwjmdwyekyruni"

func main() {
	ctx := context.Background()
	s := helpers.New("03 - agent corrects agent",
		time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC))

	strong := map[string]string{"conviction": "1.0"}
	negate := map[string]string{"conviction": "-1.0"}

	// --- 1. Operator + two extraction agents. ---
	operatorClaim := must(ranke.ClaimBuilder{
		Type:      ranke.NodeContributor,
		Content:   []byte("operator@example.com"),
		CreatedAt: s.NextTimestamp(time.Second),
	}.Sign())
	operator := must(operatorClaim.AsContributor())
	g := ranke.NewGraph(operator)

	agentAKey := must(ranke.LoadPrivateKey(helpers.KeyPath("agentA.pem")))
	agentAClaim := must(ranke.ClaimBuilder{
		Type:        ranke.NodeContributor,
		Content:     []byte("extraction-agent-A"),
		Pubkey:      agentAKey.Pubkey,
		Contributor: operator,
		CreatedAt:   s.NextTimestamp(time.Second),
	}.Sign())
	agentA := must(agentAClaim.AsContributor(agentAKey.Private))
	must(g.Add(agentAClaim))

	agentBKey := must(ranke.LoadPrivateKey(helpers.KeyPath("agentB.pem")))
	agentBClaim := must(ranke.ClaimBuilder{
		Type:        ranke.NodeContributor,
		Content:     []byte("extraction-agent-B"),
		Pubkey:      agentBKey.Pubkey,
		Contributor: operator,
		CreatedAt:   s.NextTimestamp(time.Second),
	}.Sign())
	agentB := must(agentBClaim.AsContributor(agentBKey.Private))
	must(g.Add(agentBClaim))

	// --- 2. Source email — ambiguous reference to "brother". ---
	email := must(ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     []byte("From: alice@example.com\r\nTo: bob@example.com\r\n\r\nBob, please tell my brother to call me. — Alice\r\n"),
		Contributor: operator,
		CreatedAt:   s.NextTimestamp(time.Second),
	}.Sign())
	must(g.Add(email))

	// --- 3. Entities (Alice + Bob), extracted by agentA. ---
	alice := must(ranke.ClaimBuilder{
		Type:        ranke.TypeEntity("person"),
		Content:     []byte("Alice"),
		Contributor: agentA,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: email.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(alice))
	bob := must(ranke.ClaimBuilder{
		Type:        ranke.TypeEntity("person"),
		Content:     []byte("Bob"),
		Contributor: agentA,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: email.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(bob))

	// --- 4. agentA's original (wrong) extraction: Alice — sibling → Bob. ---
	siblingA := must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation("sibling"),
		Content:     []byte("Alice mentions 'my brother' in a note to Bob; agentA infers they are siblings."),
		Contributor: agentA,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: email.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("sibling"), RelationDirection: ranke.RelationFrom, Fields: strong})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bob.ID(), Type: ranke.TypeRelation("sibling"), RelationDirection: ranke.RelationTo, Fields: strong})),
		},
	}.Sign())
	must(g.Add(siblingA))

	// --- 5. A month later, agentB reviews and corrects. ---
	// First a NEGATION — identical relation shape but conviction=-1.0,
	// attributed to agentB. Says: "agentA's claim is wrong."
	siblingNeg := must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation("sibling"),
		Content:     []byte("Re-reading the source: 'my brother' refers to a third party, not Bob. agentA's sibling claim is incorrect."),
		Contributor: agentB,
		CreatedAt:   s.NextTimestamp(31 * 24 * time.Hour),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: email.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("sibling"), RelationDirection: ranke.RelationFrom, Fields: negate})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bob.ID(), Type: ranke.TypeRelation("sibling"), RelationDirection: ranke.RelationTo, Fields: negate})),
		},
	}.Sign())
	must(g.Add(siblingNeg))

	// Then the CORRECTION — Alice is employed by Bob, conviction=+1.0.
	employedBy := must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation("employed_by"),
		Content:     []byte("Cross-referenced employment records: Alice works for Bob's company."),
		Contributor: agentB,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: email.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("employed_by"), RelationDirection: ranke.RelationFrom, Fields: strong})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bob.ID(), Type: ranke.TypeRelation("employed_by"), RelationDirection: ranke.RelationTo, Fields: strong})),
		},
	}.Sign())
	must(g.Add(employedBy))

	// --- 6. AddGraph — archive auto-consolidates the open heads. ---
	u := must(fs.New(helpers.UniverseDir))
	bth := must(file.New(filepath.Join(helpers.DataDir, "branches", "B_h")))
	arc := must(ranke.NewArchive(ctx, u, bth))
	must(arc.AddGraph(ctx, "main", g, operator, s.NextTimestamp(time.Second)))

	// --- 7. Reload, verify every branch, dump ids, assert head. ---
	s.ReloadAndVerify(ctx, "main", expectedMainHead)
}
