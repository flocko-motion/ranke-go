// Conformance scenario 01 — Alice signs sources and builds a small
// knowledge graph.
//
// Run from the repository root:
//
//	go run ./conformance/scenarios/01_alice_signs_a_source
//
// Reads fixtures from conformance/fixtures/{keys,sources}/ and writes
// reproducible outputs into this scenario's own ./archive/ and
// ./ids.txt — these are the byte-exact artifacts variant
// implementations must reproduce.
//
// See scenario.md for the narrative and paper references.

package main

import (
	"crypto/ed25519"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flocko-motion/ranke-go"
)

// All claims in this scenario share one wall-clock timestamp so the
// scenario is reproducible across runs and implementations. The
// monotonicity rule (§4.3) is trivially satisfied: same time ≥ same
// time.
var scenarioTime = time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)

// Paths are relative to the scenario directory. run.sh cd's here
// before invoking `go run .` — keeps main.go free of path-walking
// or repo-root detection. Fixtures live two levels up.
const (
	fixturesDir = "../../fixtures"
	archiveDir  = "./archive"
	idsPath     = "./ids.txt"
)

func main() {
	// Fresh archive: wipe the previous run's bytes so we generate
	// from scratch every time.
	must(os.RemoveAll(archiveDir), "wipe archive")

	// --- 1. Alice as initial node, signed by her Ed25519 key. ---
	alicePriv, err := ranke.LoadEd25519PrivateKeyPEM(filepath.Join(fixturesDir, "keys", "alice.pem"))
	must(err, "load alice.pem")
	alicePubkey, err := ranke.EncodePublicKey(alicePriv.Public())
	must(err, "encode alice pubkey")
	aliceClaim, err := ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeContribution,
		TypeSub:       "contributor",
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte("alice@example.com"),
		Pubkey:        alicePubkey,
		SigningKey:    alicePriv,
		CreatedAt:     scenarioTime,
	})
	must(err, "build alice contributor")
	alice, err := aliceClaim.AsContributor()
	must(err, "alice as contributor")
	// Bundle Alice's signing key with the contributor view so every
	// downstream NewClaim (including the ones SetBranch builds
	// internally) signs on her behalf.
	aliceSigned := ranke.WithSigningKey(alice, alicePriv)

	g := ranke.NewGraph(aliceSigned)

	// --- 2. Ingest the two source emails. ---
	apples := ok(ingestEmail(aliceSigned, filepath.Join(fixturesDir, "sources", "alice_to_bob__apples.eml")))
	family := ok(ingestEmail(aliceSigned, filepath.Join(fixturesDir, "sources", "alice_to_bob__family.eml")))
	mustAdd(g, apples)
	mustAdd(g, family)

	// --- 3. A summary derivation over the apples email. ---
	summary := ok(ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeDerivation,
		TypeSub:       "summary",
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte("Alice tells Bob she likes apples."),
		Contributor:   aliceSigned,
		CreatedAt:     scenarioTime,
		Edges:         []ranke.Edge{ok(derivationSource(apples))},
	}))
	mustAdd(g, summary)

	// --- 4. Entities extracted from each source. ---
	aliceEntity := ok(entity(aliceSigned, "person", "Alice", apples))
	bobSr := ok(entity(aliceSigned, "person", "Bob", apples))
	applesEntity := ok(entity(aliceSigned, "object", "apples", apples))
	bobJr := ok(entity(aliceSigned, "person", "Bob Jr.", family))
	mustAdd(g, aliceEntity)
	mustAdd(g, bobSr)
	mustAdd(g, applesEntity)
	mustAdd(g, bobJr)

	// --- 5. Relations: Alice—likes→apples, Alice—knows→Bob (sr.), Bob⇄Bob Jr. ---
	likes := ok(relation(aliceSigned, "likes",
		"Alice expresses preference for apples.",
		[]ranke.Claim{apples},
		[]ranke.Claim{aliceEntity},
		[]ranke.Claim{applesEntity}))
	knows := ok(relation(aliceSigned, "knows",
		"Alice addresses Bob directly.",
		[]ranke.Claim{apples},
		[]ranke.Claim{aliceEntity},
		[]ranke.Claim{bobSr}))
	familyRel := ok(symmetricRelation(aliceSigned, "family",
		"Bob and Bob Jr. share kinship per Alice's reference.",
		[]ranke.Claim{family},
		bobSr, bobJr))
	mustAdd(g, likes)
	mustAdd(g, knows)
	mustAdd(g, familyRel)

	// --- 6. Open the fs archive and commit branch "main". ---
	arc, err := ranke.NewFsArchive(archiveDir)
	must(err, "open archive")
	if !g.IsConsolidated() {
		head, err := g.Consolidate(aliceSigned, scenarioTime)
		must(err, "consolidate")
		_, err = g.AddClaim(head)
		must(err, "add consolidation")
	}
	must(arc.SetBranch("main", g, aliceSigned, scenarioTime), "SetBranch main")

	// --- 7. Reload from disk and validate end-to-end. ---
	fmt.Println("reloading archive from disk...")
	arc2, err := ranke.NewFsArchive(archiveDir)
	must(err, "reopen archive")

	fmt.Println("verifying every claim's signature and integrity...")
	branch, err := arc2.GetBranch("main")
	must(err, "GetBranch main")
	g2, err := arc2.GetGraph(branch.Latest().Head())
	must(err, "GetGraph head")
	must(g2.Validate(), "validate reloaded graph")
	fmt.Println("  all claims valid ✓")

	// --- 8. Dump sorted ids of every claim reachable from the head. ---
	ids := collectIds(g2)
	sort.Strings(ids)
	var out strings.Builder
	for _, id := range ids {
		out.WriteString(id)
		out.WriteByte('\n')
	}
	must(os.WriteFile(idsPath, []byte(out.String()), 0o644), "write ids.txt")

	// --- 9. Final summary: archive location, branches, head ids. ---
	fmt.Println()
	fmt.Printf("archive:  %s  (%d claims)\n", archiveDir, len(ids))
	fmt.Printf("ids.txt:  %s\n", idsPath)
	fmt.Println()
	fmt.Println("branches:")
	for _, b := range arc2.Branches() {
		fmt.Printf("  %s → %s\n", b.Name(), b.Latest().Head().String())
	}
}

// ingestEmail builds a source/email claim from a .eml file attributed
// to the given contributor.
func ingestEmail(contributor ranke.Contributor, path string) (ranke.Claim, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeSource,
		TypeSub:       "email",
		EncodingClass: ranke.EncodingMessage,
		EncodingSub:   "rfc822",
		Content:       body,
		Contributor:   contributor,
		CreatedAt:     scenarioTime,
	})
}

// derivationSource builds a derivation/source edge from a derived
// claim back to its source.
func derivationSource(source ranke.Claim) (ranke.Edge, error) {
	return ranke.NewEdge(ranke.EdgeConfig{
		Reference: source.ID(),
		TypeClass: ranke.EdgeDerivation,
		TypeSub:   "source",
	})
}

// entity builds an entity/<sub> claim with a derivation/source edge
// back to the source it was extracted from.
func entity(contributor ranke.Contributor, sub, label string, source ranke.Claim) (ranke.Claim, error) {
	srcEdge, err := derivationSource(source)
	if err != nil {
		return nil, err
	}
	return ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeEntity,
		TypeSub:       sub,
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(label),
		Contributor:   contributor,
		CreatedAt:     scenarioTime,
		Edges:         []ranke.Edge{srcEdge},
	})
}

// relation builds a relation/<sub> claim with one derivation/source
// edge per source and one relation/<sub> edge per from-side and
// to-side entity.
func relation(contributor ranke.Contributor, sub, text string, sources, froms, tos []ranke.Claim) (ranke.Claim, error) {
	edges, err := relationEdges(sub, sources, froms, tos, nil)
	if err != nil {
		return nil, err
	}
	return ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeRelation,
		TypeSub:       sub,
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(text),
		Contributor:   contributor,
		CreatedAt:     scenarioTime,
		Edges:         edges,
	})
}

// symmetricRelation builds a relation/<sub> claim where every member
// carries RelationFrom — no role distinction (§4.7).
func symmetricRelation(contributor ranke.Contributor, sub, text string, sources []ranke.Claim, members ...ranke.Claim) (ranke.Claim, error) {
	edges, err := relationEdges(sub, sources, members, nil, []ranke.RelationDirection{})
	if err != nil {
		return nil, err
	}
	return ranke.NewClaim(ranke.ClaimConfig{
		TypeClass:     ranke.NodeRelation,
		TypeSub:       sub,
		EncodingClass: ranke.EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(text),
		Contributor:   contributor,
		CreatedAt:     scenarioTime,
		Edges:         edges,
	})
}

// relationEdges builds the edge list for a relation claim:
//   - one derivation/source edge per source
//   - one relation/<sub> edge per from-side member (RelationFrom)
//   - one relation/<sub> edge per to-side member (RelationTo)
//
// When symmetric is non-nil, all from-side members are treated as
// RelationFrom and tos is ignored.
func relationEdges(sub string, sources, froms, tos []ranke.Claim, symmetric []ranke.RelationDirection) ([]ranke.Edge, error) {
	out := make([]ranke.Edge, 0, len(sources)+len(froms)+len(tos))
	for _, s := range sources {
		e, err := derivationSource(s)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	for _, f := range froms {
		e, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference:         f.ID(),
			TypeClass:         ranke.EdgeRelation,
			TypeSub:           sub,
			RelationDirection: ranke.RelationFrom,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if symmetric == nil {
		for _, t := range tos {
			e, err := ranke.NewEdge(ranke.EdgeConfig{
				Reference:         t.ID(),
				TypeClass:         ranke.EdgeRelation,
				TypeSub:           sub,
				RelationDirection: ranke.RelationTo,
			})
			if err != nil {
				return nil, err
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// collectIds returns the ids of every claim reachable from any open
// head of g, as multibase strings. The graph is single-headed after
// consolidation, so this is the full closure.
func collectIds(g ranke.Graph) []string {
	seen := make(map[string]bool)
	queue := append([]ranke.Id{}, g.Heads()...)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		k := id.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		c, ok := g.GetClaim(id)
		if !ok {
			continue
		}
		for _, e := range c.Edges() {
			queue = append(queue, e.Reference())
		}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	return out
}

// --- error-checking shims that keep the main flow readable ---

func must(err error, what string) {
	if err != nil {
		log.Fatalf("scenario 01: %s: %v", what, err)
	}
}

// ok unwraps any (value, error) pair, panicking on error. Generic so
// it works for ranke.Claim, ranke.Edge, and whatever else the
// scenario constructs.
func ok[T any](v T, err error) T {
	if err != nil {
		log.Fatalf("scenario 01: %v", err)
	}
	return v
}

func mustAdd(g ranke.Graph, c ranke.Claim) {
	if _, err := g.AddClaim(c); err != nil {
		log.Fatalf("scenario 01: addClaim: %v", err)
	}
}

// Suppress the crypto/ed25519 import shadow — ranke.LoadEd25519PrivateKeyPEM
// returns an ed25519.PrivateKey but we never name the type directly.
var _ = ed25519.PublicKey(nil)
