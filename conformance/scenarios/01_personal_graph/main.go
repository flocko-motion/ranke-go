// Conformance scenario 01 — personal knowledge graph.
//
// Alice (Ed25519 keypair, signed) ingests two emails, then extracts
// a small knowledge graph (summary + entities + relations) over
// them. The result is persisted as a data/ bundle (universe + B_h +
// sorted ids), and a conformant variant implementation must
// reproduce the same bundle byte-for-byte.
//
// Every claim is built inline so the reader sees the exact
// ClaimBuilder + Sign call for each one — no local helpers hiding
// the pattern.
//
// Run from anywhere:
//
//	conformance/scenarios/01_personal_graph/run.sh

package main

import (
	"context"
	"time"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/conformance/helpers"
)

// must aliases helpers.Must so error-checked calls read with a
// single word at every site.
func must[T any](v T, rest ...any) T { return helpers.Must(v, rest...) }

// Expected final head of branch "main" — hardcoded so the
// scenario itself fails loud if anything in the chain changes.
const expectedMainHead = "b5ua3tgiyjt3tezqsng2dserleoawi6g6ymk2qj4xwi2ojxwk6mzgdi35sdapnp7i333tbisaykhmnnzokoouxqbhwvyajrymtfyrk7zhbq"

func main() {
	ctx := context.Background()
	s := helpers.New("01 - personal knowledge graph",
		time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC))

	// --- 1. Alice as initial node, signed by her Ed25519 key. ---
	aliceKey := must(ranke.LoadPrivateKey(helpers.KeyPath("alice.pem")))
	aliceClaim := must(ranke.ClaimBuilder{
		Type:      ranke.NodeContributor,
		Content:   []byte("alice@example.com"),
		Pubkey:    aliceKey.Pubkey,
		CreatedAt: s.NextTimestamp(time.Second),
	}.Sign(aliceKey.Private))
	alice := must(aliceClaim.AsContributor(aliceKey.Private))
	g := ranke.NewGraph(alice)

	// --- 2. Ingest the two source emails. ---
	emailApples := must(ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     must(helpers.LoadSource("alice_to_bob__apples.eml")),
		Contributor: alice,
		CreatedAt:   s.NextTimestamp(time.Second),
	}.Sign())
	must(g.Add(emailApples))
	emailFamily := must(ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     must(helpers.LoadSource("alice_to_bob__family.eml")),
		Contributor: alice,
		CreatedAt:   s.NextTimestamp(time.Second),
	}.Sign())
	must(g.Add(emailFamily))

	// --- 3. A summary derivation of the apples email. ---
	summary := must(ranke.ClaimBuilder{
		Type:        ranke.TypeDerivation("summary"),
		Content:     []byte("Alice tells Bob she likes apples."),
		Contributor: alice,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(summary))

	// --- 4. "Alice likes apples" — entities + their relation, made together. ---
	aliceEntity := must(ranke.ClaimBuilder{
		Type:        ranke.TypeEntity("person"),
		Content:     []byte("Alice"),
		Contributor: alice,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(aliceEntity))
	applesEntity := must(ranke.ClaimBuilder{
		Type:        ranke.TypeEntity("object"),
		Content:     []byte("apples"),
		Contributor: alice,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(applesEntity))
	likes := must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation("likes"),
		Content:     []byte("Alice expresses preference for apples."),
		Contributor: alice,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference: emailApples.ID(),
				Type:      ranke.TypeDerivation("source"),
			})),
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         aliceEntity.ID(),
				Type:              ranke.TypeRelation("likes"),
				RelationDirection: ranke.RelationFrom,
			})),
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         applesEntity.ID(),
				Type:              ranke.TypeRelation("likes"),
				RelationDirection: ranke.RelationTo,
			})),
		},
	}.Sign())
	must(g.Add(likes))

	// --- 5. "Alice knows Bob" — Alice already in the graph; add Bob and the relation. ---
	bobSr := must(ranke.ClaimBuilder{
		Type:        ranke.TypeEntity("person"),
		Content:     []byte("Bob"),
		Contributor: alice,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailApples.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(bobSr))
	knows := must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation("knows"),
		Content:     []byte("Alice addresses Bob directly."),
		Contributor: alice,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference: emailApples.ID(),
				Type:      ranke.TypeDerivation("source"),
			})),
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         aliceEntity.ID(),
				Type:              ranke.TypeRelation("knows"),
				RelationDirection: ranke.RelationFrom,
			})),
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         bobSr.ID(),
				Type:              ranke.TypeRelation("knows"),
				RelationDirection: ranke.RelationTo,
			})),
		},
	}.Sign())
	must(g.Add(knows))

	// --- 6. "Bob and Bob Jr are family" (symmetric) — Bob already in the graph. ---
	bobJr := must(ranke.ClaimBuilder{
		Type:        ranke.TypeEntity("person"),
		Content:     []byte("Bob Jr."),
		Contributor: alice,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{must(ranke.NewEdge(ranke.EdgeConfig{
			Reference: emailFamily.ID(),
			Type:      ranke.TypeDerivation("source"),
		}))},
	}.Sign())
	must(g.Add(bobJr))
	familyRel := must(ranke.ClaimBuilder{
		Type:        ranke.TypeRelation("family"),
		Content:     []byte("Bob and Bob Jr. share kinship per Alice's reference."),
		Contributor: alice,
		CreatedAt:   s.NextTimestamp(time.Second),
		Edges: []ranke.Edge{
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference: emailFamily.ID(),
				Type:      ranke.TypeDerivation("source"),
			})),
			// Symmetric: both members carry RelationFrom (§4.7).
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         bobSr.ID(),
				Type:              ranke.TypeRelation("family"),
				RelationDirection: ranke.RelationFrom,
			})),
			must(ranke.NewEdge(ranke.EdgeConfig{
				Reference:         bobJr.ID(),
				Type:              ranke.TypeRelation("family"),
				RelationDirection: ranke.RelationFrom,
			})),
		},
	}.Sign())
	must(g.Add(familyRel))

	// --- 7. Consolidate, then compose an Archive from its parts ---
	// (filesystem Universe under data/universe/, B_h file under
	// data/branches/) and persist as branch "main". Real deployments
	// would stack a MemUniverse on top, or swap S3Universe in below —
	// this scenario keeps it flat so the bundle is just a directory.
	must(g.Consolidate(alice, s.NextTimestamp(time.Second)))
	u := must(ranke.NewFsUniverse(helpers.UniverseDir))
	bth := must(ranke.NewFsBranchTableHead(helpers.BranchTableHeadPath))
	arc := must(ranke.NewArchive(ctx, u, bth))
	must(arc.AddGraph(ctx, "main", g, alice, s.NextTimestamp(time.Second)))

	// --- 8. Reload, verify every branch, dump ids, assert head. ---
	s.ReloadAndVerify(ctx, "main", expectedMainHead)
}
