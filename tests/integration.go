// package: tests / conformance
// type:    test
// job:     public IntegrationTest entry point + a per-scenario fixture that drives the reference dev Sequencer over a pluggable storage backend
// limits:  no scenario bodies here; those live alongside (-> tests/scenarios.go)
//
// Package tests is the public conformance suite for the Ranke-Graph
// library. A storage backend (a Universe + a History) proves it conforms
// to the Ranke-Graph ADT by running IntegrationTest from a *_test.go:
//
//	import "github.com/flocko-motion/ranke-go/tests"
//
//	func TestMyBackend(t *testing.T) {
//	    ctx := context.Background()
//	    tests.IntegrationTest(t, ctx, func(t *testing.T, clk *generator.Clock) (ranke.Universe, ranke.History, error) {
//	        return mybackend.NewUniverse(t.TempDir()), mybackend.NewHistory(clk), nil
//	    })
//	}
//
// The suite stands the reference dev Sequencer (adapter/sequencer/dev) over
// the backend and drives the paper's write path — AddClaims advances a
// single "main" branch — then reads the archive back through immutable
// snapshots and validates each closure.
package tests

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/flocko-motion/ranke-go"
	devhist "github.com/flocko-motion/ranke-go/adapter/history/dev"
	devseq "github.com/flocko-motion/ranke-go/adapter/sequencer/dev"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	"github.com/flocko-motion/ranke-go/tests/generator"
	"github.com/stretchr/testify/require"
)

// Backend opens a fresh, isolated storage tier for one scenario — a Universe
// for claims + content and a History for the head timeline — over the shared
// deterministic clock. It is called once per scenario, so scenarios never
// share state; a durable backend should hand back a handle to fresh storage
// each call.
type Backend func(t *testing.T, clock *generator.Clock) (ranke.Universe, ranke.History, error)

// IntegrationTest runs the full Ranke-Graph integration suite against the
// storage backend. Each scenario gets its own fixture (a fresh backend + a
// fresh reference Sequencer), so they are independent.
func IntegrationTest(t *testing.T, ctx context.Context, backend Backend) {
	t.Helper()
	t.Run("AliceEmail", func(t *testing.T) {
		runAliceEmail(t, newFixture(t, ctx, backend))
	})
	t.Run("MultiHeadAutoConsolidates", func(t *testing.T) {
		runMultiHeadAutoConsolidates(t, newFixture(t, ctx, backend))
	})
	t.Run("SuccessiveAddsAccumulate", func(t *testing.T) {
		runSuccessiveAddsAccumulate(t, newFixture(t, ctx, backend))
	})
}

// --- fixture ---

// fixture is one scenario's write path: the reference dev Sequencer over the
// backend's Universe + History, sharing the deterministic clock so every
// minted claim's created_at is reproducible and monotone. self is the keyed
// contributor the Sequencer signs branch tables with; scenarios also author
// content with it (contributor 0, the operator — the generator does the same).
type fixture struct {
	ctx   context.Context
	u     ranke.Universe
	seq   *devseq.Sequencer
	clock *generator.Clock
	self  ranke.Contributor
}

// fixtureBase is the fixed instant the shared clock starts at — never
// time.Now, so a run reproduces byte-identically.
var fixtureBase = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func newFixture(t *testing.T, ctx context.Context, backend Backend) *fixture {
	t.Helper()
	clock := generator.NewClock(fixtureBase, time.Second)
	self := keyedContributor(t, ctx, clock, "sequencer")
	u, hist, err := backend(t, clock)
	require.NoError(t, err, "open backend")
	seq, err := devseq.NewSequencer(ctx, u, hist, self, clock)
	require.NoError(t, err, "stand up dev Sequencer")
	return &fixture{ctx: ctx, u: u, seq: seq, clock: clock, self: self}
}

// write advances the "main" branch with claims (in topological order:
// references precede the claims that cite them) and returns the new head k′.
func (f *fixture) write(t *testing.T, claims ...ranke.Claim) ranke.Id {
	t.Helper()
	head, err := f.seq.AddClaims(f.ctx, claims)
	require.NoError(t, err, "AddClaims")
	return head
}

// snapshot returns a fresh immutable read view at the current head — the
// re-read every scenario leans on (previously-returned values must stay
// valid, since a claim is self-contained by immutability).
func (f *fixture) snapshot(t *testing.T) ranke.Archive {
	t.Helper()
	arc, err := f.seq.GetArchive(f.ctx)
	require.NoError(t, err, "GetArchive")
	return arc
}

// mainHead returns the root of branch "main" in a fresh snapshot.
func (f *fixture) mainHead(t *testing.T, arc ranke.Archive) ranke.Id {
	t.Helper()
	require.True(t, must(arc.HasBranch(f.ctx, "main")), "branch main exists")
	b, err := arc.GetBranch(f.ctx, "main")
	require.NoError(t, err, "GetBranch main")
	return b.Head()
}

// validate walks the closure from head and verifies every claim (§5.10).
func (f *fixture) validate(t *testing.T, head ranke.Id) {
	t.Helper()
	g, err := ranke.NewGraphFromClosure(f.ctx, head, f.u)
	require.NoError(t, err, "open closure")
	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err(), "closure verification")
	require.Empty(t, run.Failures(), "closure has no verification failures")
}

// --- shared claim builders ---

// keyedContributor builds a deterministic Ed25519-signed root contributor:
// its multikey-encoded pubkey is its content (§5.7), and it carries the key
// so claims attributed to it sign automatically. seedTag fixes the identity.
func keyedContributor(t *testing.T, ctx context.Context, clock *generator.Clock, seedTag string) ranke.Contributor {
	t.Helper()
	seed := sha256.Sum256([]byte(seedTag))
	priv := ed25519.NewKeyFromSeed(seed[:])
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	require.NoError(t, err, "encode pubkey")
	c, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pubkey).
		WithEncoding(ranke.EncodingOctetStream).
		WithCreatedAt(clock.Tick()).
		Sign(priv)
	require.NoError(t, err, "sign contributor")
	ctr, err := c.AsContributor(ctx, nil, priv)
	require.NoError(t, err, "as contributor")
	return ctr
}

// email builds a source/email claim attributed to ctr, stamped from the
// shared clock and sitting one above its contributor (§4.1).
func (f *fixture) email(t *testing.T, ctr ranke.Contributor, from, to, body string) ranke.Claim {
	t.Helper()
	c, err := ranke.NewClaim(ranke.TypeSource("email"), ctr).
		WithInlineContent([]byte("From: " + from + "\r\nTo: " + to + "\r\n\r\n" + body)).
		WithEncoding(ranke.EncodingMessage("rfc822")).
		WithCreatedAt(f.clock.Tick()).
		WithHeight(ranke.HeightOf(ctr)).
		Sign()
	require.NoError(t, err, "sign email")
	return c
}

// entity builds an entity/<sub> claim with a derivation/source edge back to
// source (the §3.5 provenance a semantic claim requires).
func (f *fixture) entity(t *testing.T, ctr ranke.Contributor, sub, label string, source ranke.Claim) ranke.Claim {
	t.Helper()
	de, err := ranke.NewEdge(ranke.EdgeConfig{Reference: source.ID(), Type: ranke.TypeDerivation("source")})
	require.NoError(t, err, "derivation edge")
	c, err := ranke.NewClaim(ranke.TypeEntity(sub), ctr).
		WithInlineContent([]byte(label)).
		WithEncoding(ranke.EncodingPlain).
		WithEdges(de).
		WithCreatedAt(f.clock.Tick()).
		WithHeight(ranke.HeightOf(ctr, source)).
		Sign()
	require.NoError(t, err, "sign entity")
	return c
}

// memFixture builds a fixture over an in-memory Universe + History — the
// backend the sequencer unit tests drive directly.
func memFixture(t *testing.T, ctx context.Context) *fixture {
	t.Helper()
	return newFixture(t, ctx, func(_ *testing.T, clk *generator.Clock) (ranke.Universe, ranke.History, error) {
		return mem.New(), devhist.New(clk), nil
	})
}
