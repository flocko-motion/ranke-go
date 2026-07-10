// Conformance scenario 02 — agent analyses two emails.
//
// An operator (identity-Sign contributor) ingests two emails as
// source/email claims. An extraction agent (signed by its own
// Ed25519 key) is itself contributed by the operator; the agent
// then derives entities and relations from the emails — a summary,
// four entities (Alice, apples, Bob Sr, Bob Jr), and four relations
// (likes, knows, ignores, family).
//
// Two Bobs from two emails get distinct ids by content-addressing
// (different derivation/source edges → different node hashes). The
// "family" relation is symmetric: both members carry RelationFrom.
//
// Demonstrates the multi-contributor pattern (operator + agent) and
// the §3.5 derivation chain (every derived claim cites its source
// via a derivation/source edge).

// package: main / scenario
// type:    cmd
// job:     build & persist the scenario-02 agent-analyses data bundle
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

const expectedMainHead = "bciqd3khoouqui5q36ikpfuwgzyc674azteg6s74zvw5kkh6fhw4hhdi"

func main() {
	ctx := context.Background()
	s := helpers.New("02 - agent analyses",
		time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC))

	// --- 1. Operator: identity-Sign root contributor. ---
	operatorClaim := must(ranke.ClaimBuilder{
		Type:      ranke.NodeContributor,
		Content:   []byte("operator@example.com"),
		CreatedAt: s.NextTimestamp(time.Second),
	}.Sign())
	operator := must(operatorClaim.AsContributor())
	g := ranke.NewGraph(operator)

	// --- 2. Extraction agent — Ed25519-signed contributor under operator. ---
	// The agent claim itself is signed by operator (identity-Sign);
	// agentAKey is bound to the agent for the agent's OWN contributions.
	agentAKey := must(ranke.LoadPrivateKey(helpers.KeyPath("agentA.pem")))
	agentClaim := must(ranke.ClaimBuilder{
		Type:        ranke.NodeContributor,
		Content:     []byte("extraction-agent-v1"),
		Pubkey:      agentAKey.Pubkey,
		Contributor: operator,
		CreatedAt:   s.NextTimestamp(time.Second),
	}.Sign())
	agent := must(agentClaim.AsContributor(agentAKey.Private))
	must(g.Add(agentClaim))

	// --- 3. Ingest two source emails, attributed to the operator. ---
	emailApples := must(ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     must(helpers.LoadSource("alice_to_bob__apples.eml")),
		Contributor: operator,
		CreatedAt:   s.NextTimestamp(time.Second),
	}.Sign())
	must(g.Add(emailApples))
	emailFamily := must(ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     must(helpers.LoadSource("alice_to_bob__family.eml")),
		Contributor: operator,
		CreatedAt:   s.NextTimestamp(time.Second),
	}.Sign())
	must(g.Add(emailFamily))

	// --- 4. Agent: summary derivation of the apples email. ---
	summary := must(ranke.ClaimBuilder{
		Type:        ranke.TypeDerivation("summary"),
		Content:     []byte("Alice expresses preference for apples."),
		Contributor: agent,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(summary))

	// --- 5. Agent: extract entities (one Alice, one apples, two Bobs). ---
	alice := must(ranke.ClaimBuilder{
		Type:        ranke.TypeEntity("person"),
		Content:     []byte("Alice"),
		Contributor: agent,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(alice))
	apples := must(ranke.ClaimBuilder{
		Type:        ranke.TypeEntity("object"),
		Content:     []byte("apples"),
		Contributor: agent,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(apples))
	bobSr := must(ranke.ClaimBuilder{
		Type:        ranke.TypeEntity("person"),
		Content:     []byte("Bob"),
		Contributor: agent,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(bobSr))
	bobJr := must(ranke.ClaimBuilder{
		Type:        ranke.TypeEntity("person"),
		Content:     []byte("Bob Jr."),
		Contributor: agent,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailFamily.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(bobJr))

	// --- 6. Agent: extract relations. ---
	// Each relation/* edge carries an additional "conviction" field
	// in the -1..1 range — an application convention (paper §3.5
	// mentions "conviction values" as the kind of detail consumers
	// fetch at intermediate abstraction levels). +1.0 is strong
	// assertion, -1.0 strong negation (used in scenario 03), 0 is
	// neutral. Values are decimal-encoded strings since edge Fields
	// is map[string]string. They participate in the canonical
	// encoding — variant impls must match byte-for-byte.
	strong := map[string]string{"conviction": "1.0"}
	medium := map[string]string{"conviction": "0.5"}
	weak := map[string]string{"conviction": "0.3"}

	likes := must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation("likes"),
		Content:     []byte("Alice expresses preference for apples in the email."),
		Contributor: agent,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: emailApples.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("likes"), RelationDirection: ranke.RelationFrom, Fields: strong})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: apples.ID(), Type: ranke.TypeRelation("likes"), RelationDirection: ranke.RelationTo, Fields: strong})),
		},
	}.Sign())
	must(g.Add(likes))
	knows := must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation("knows"),
		Content:     []byte("Alice addresses Bob directly, implying they are acquainted."),
		Contributor: agent,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: emailApples.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("knows"), RelationDirection: ranke.RelationFrom, Fields: strong})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bobSr.ID(), Type: ranke.TypeRelation("knows"), RelationDirection: ranke.RelationTo, Fields: strong})),
		},
	}.Sign())
	must(g.Add(knows))
	ignores := must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation("ignores"),
		Content:     []byte("Bob does not respond to Alice (inferred from absence of reply)."),
		Contributor: agent,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: emailApples.ID(), Type: ranke.TypeDerivation("source")})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bobSr.ID(), Type: ranke.TypeRelation("ignores"), RelationDirection: ranke.RelationFrom, Fields: weak})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: alice.ID(), Type: ranke.TypeRelation("ignores"), RelationDirection: ranke.RelationTo, Fields: weak})),
		},
	}.Sign())
	must(g.Add(ignores))
	family := must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation("family"),
		Content:     []byte("Bob Sr. and Bob Jr. share a surname; Alice's email implies kinship."),
		Contributor: agent,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: emailFamily.ID(), Type: ranke.TypeDerivation("source")})),
			// Symmetric: both members are RelationFrom (§4.7).
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bobSr.ID(), Type: ranke.TypeRelation("family"), RelationDirection: ranke.RelationFrom, Fields: medium})),
			must(ranke.NewEdge(ranke.EdgeConfig{Reference: bobJr.ID(), Type: ranke.TypeRelation("family"), RelationDirection: ranke.RelationFrom, Fields: medium})),
		},
	}.Sign())
	must(g.Add(family))

	// --- 7. AddGraph — archive auto-consolidates the open heads and persists. ---
	u := must(fs.New(helpers.UniverseDir))
	bth := must(file.New(filepath.Join(helpers.DataDir, "branches", "B_h")))
	seq := must(ranke.NewSequencer(ctx, u, bth, operator))
	must(seq.AddGraph(ctx, "main", g, operator, s.NextTimestamp(time.Second)))

	// --- 8. Reload, verify every branch, dump ids, assert head. ---
	s.ReloadAndVerify(ctx, "main", expectedMainHead)
}
