// Integration tests — drive IntegrationTest against every storage backend
// the library bundles (in-memory and on-disk), each through the reference
// dev Sequencer.
package tests

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage/fs"
	"github.com/rankegraph/ranke-go/adapter/storage/mem"
	"github.com/rankegraph/ranke-go/tests/generator"
	"github.com/stretchr/testify/require"
)

// fsTestDir is the on-disk root for the fs-backed scenarios. Same path every
// run (default /tmp/ranke-go-test) so the layout is at a predictable spot for
// inspection; TestMain wipes and recreates it, and each scenario gets an
// isolated subdirectory under it. $RANKE_FS_DIR overrides the default (the
// Makefile sets it, then echoes it after the run).
const defaultFsTestDir = "/tmp/ranke-go-test"

var fsTestDir string

func TestMain(m *testing.M) {
	fsTestDir = os.Getenv("RANKE_FS_DIR")
	if fsTestDir == "" {
		fsTestDir = defaultFsTestDir
	}
	if err := os.RemoveAll(fsTestDir); err != nil {
		fmt.Fprintln(os.Stderr, "tests/TestMain: RemoveAll:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(fsTestDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "tests/TestMain: MkdirAll:", err)
		os.Exit(1)
	}
	code := m.Run()
	fmt.Printf("\nfs archive directory (preserved for inspection):\n  %s\n", fsTestDir)
	os.Exit(code)
}

func TestIntegrationMem(t *testing.T) {
	ctx := context.Background()
	IntegrationTest(t, ctx, func(_ *testing.T, _ *generator.Clock) (ranke.Universe, error) {
		return mem.New(), nil
	})
}

func TestIntegrationFs(t *testing.T) {
	ctx := context.Background()
	IntegrationTest(t, ctx, func(_ *testing.T, _ *generator.Clock) (ranke.Universe, error) {
		dir, err := os.MkdirTemp(fsTestDir, "scenario-")
		if err != nil {
			return nil, err
		}
		return fs.New(dir)
	})
}

// TestFsDurability proves the write path is durable: after committing through
// one fs handle, a COLD handle over the same directory reads the archive back
// — the on-disk bytes stand alone, no in-memory state required.
func TestFsDurability(t *testing.T) {
	ctx := context.Background()
	dir, err := os.MkdirTemp(fsTestDir, "durability-")
	require.NoError(t, err, "temp dir")

	f := newFixture(t, ctx, func(_ *testing.T, _ *generator.Clock) (ranke.Universe, error) {
		return fs.New(dir)
	})
	em := f.email(t, f.self, "alice@example.com", "bob@example.com", "durable\r\n")
	head := f.write(t, em)

	cold, err := fs.New(dir)
	require.NoError(t, err, "cold fs handle")
	arc, err := ranke.NewArchive(ctx, cold, head)
	require.NoError(t, err, "open archive from cold handle")
	ok, err := arc.HasClaim(ctx, em.ID())
	require.NoError(t, err, "HasClaim from cold handle")
	require.True(t, ok, "email durably readable through a cold fs handle")
}
