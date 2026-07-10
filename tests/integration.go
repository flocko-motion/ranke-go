// package: tests / conformance
// type:    test
// job:     public IntegrationTest entry point + testArchive Reset harness for any Archive backend
// limits:  no scenario bodies here; those live alongside (-> tests/scenarios.go)
//
// Package tests is the public conformance suite for the Ranke-Graph
// library. Third-party Archive implementations should call
// IntegrationTest from a *_test.go to verify their backend conforms
// to the Ranke-Graph ADT:
//
//	import "github.com/flocko-motion/ranke-go/tests"
//
//	func TestMyArchive(t *testing.T) {
//	    dir := t.TempDir()
//	    tests.IntegrationTest(t, ctx, func() ranke.Sequencer {
//	        s, err := mybackend.NewSequencer(dir)
//	        if err != nil { t.Fatal(err) }
//	        return s
//	    })
//	}
//
// Internally the package owns the helpers, scenarios, and guarantee
// tests that drive any Archive that satisfies the interface — both
// the bundled compositions (Fs/Mem Universe + BranchTableHead) and
// any 3rd-party stack.
package tests

import (
	"context"
	"testing"

	"github.com/flocko-motion/ranke-go"
)

// IntegrationTest runs the full Ranke-Graph integration suite against
// the Archive returned by factory.
//
// factory is called once eagerly to obtain the initial Archive
// handle, and again at every Reset checkpoint inside a scenario.
// Implementors should make factory return:
//
//   - the same Archive instance every call, for in-memory backends —
//     Reset is then a pointer-equality no-op;
//   - a fresh handle backed by the same durable storage, for fs/S3/...
//     backends — Reset re-reads from durable state and clears caches.
//
// Every scenario sprinkles Reset calls between writes and reads. The
// suite is correct iff each Reset is observably a no-op: the handle
// changes, but values previously returned remain valid (claims,
// graphs, branches, branch entries are self-contained).
func IntegrationTest(t *testing.T, ctx context.Context, factory func() ranke.Sequencer) {
	t.Helper()
	t.Run("AliceEmail", func(t *testing.T) {
		runAliceEmail(t, ctx, newTestArchive(factory))
	})
	t.Run("AddGraphAutoConsolidates", func(t *testing.T) {
		runAddGraphAutoConsolidates(t, ctx, newTestArchive(factory))
	})
	t.Run("AddClaimExtendsBranch", func(t *testing.T) {
		runAddClaimExtendsBranch(t, ctx, newTestArchive(factory))
	})
}

// testArchive wraps a ranke.Sequencer with a Reset method that drops
// the current handle and reloads via the open closure. It embeds the
// Sequencer (for the write shims AddGraph/AddClaim and GetArchive) and
// delegates reads to a fresh snapshot, so scenarios can call GetClaim /
// GetBranch / HasBranch directly against the current committed state.
type testArchive struct {
	ranke.Sequencer
	open func() ranke.Sequencer
}

func newTestArchive(open func() ranke.Sequencer) *testArchive {
	return &testArchive{Sequencer: open(), open: open}
}

func (a *testArchive) Reset() { a.Sequencer = a.open() }

// snapshot returns an immutable read view of the current committed state.
func (a *testArchive) snapshot(ctx context.Context) ranke.Archive {
	arc, _, _ := a.Sequencer.GetArchive(ctx)
	return arc
}

func (a *testArchive) GetClaim(ctx context.Context, id ranke.Id) (ranke.Claim, error) {
	return a.snapshot(ctx).GetClaim(ctx, id)
}

func (a *testArchive) HasBranch(ctx context.Context, name string) bool {
	return a.snapshot(ctx).HasBranch(ctx, name)
}

func (a *testArchive) GetBranch(ctx context.Context, name string) (ranke.Branch, error) {
	return a.snapshot(ctx).GetBranch(ctx, name)
}
