// Package tests is the public conformance suite for the Ranke-Graph
// library. Third-party Archive implementations should call
// IntegrationTest from a *_test.go to verify their backend conforms
// to the Ranke-Graph ADT:
//
//	import "github.com/flocko-motion/ranke-go/tests"
//
//	func TestMyArchive(t *testing.T) {
//	    dir := t.TempDir()
//	    tests.IntegrationTest(t, func() ranke.Archive {
//	        a, err := mybackend.New(dir)
//	        if err != nil { t.Fatal(err) }
//	        return a
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
func IntegrationTest(t *testing.T, ctx context.Context, factory func() ranke.Archive) {
	t.Helper()
	t.Run("AliceEmail", func(t *testing.T) {
		runAliceEmail(t, ctx, newTestArchive(factory))
	})
	t.Run("AgentAnalyzesEmails", func(t *testing.T) {
		runAgentAnalyzes(t, ctx, newTestArchive(factory))
	})
	t.Run("SetBranchAutoConsolidates", func(t *testing.T) {
		runSetBranchAutoConsolidates(t, ctx, newTestArchive(factory))
	})
}

// testArchive wraps a ranke.Archive with a Reset method that drops
// the current handle and reloads via the open closure.
type testArchive struct {
	ranke.Archive
	open func() ranke.Archive
}

func newTestArchive(open func() ranke.Archive) *testArchive {
	return &testArchive{Archive: open(), open: open}
}

func (a *testArchive) Reset() { a.Archive = a.open() }
