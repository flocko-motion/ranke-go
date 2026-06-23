// Integration tests — drive IntegrationTest against every Archive
// shape the library supports, composed explicitly from Universe +
// BranchTableHead.
package tests

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/fs"
	"github.com/flocko-motion/ranke-go/adapter/mem"
	"github.com/stretchr/testify/require"
)

// fsTestDir is the on-disk location used by TestIntegrationFs.
// Same path every run (default: /tmp/ranke-go-test) so the layout
// is always at a predictable spot for inspection. The dir is
// emptied before the run so prior state can't bleed in.
//
// $RANKE_FS_DIR overrides the default; the Makefile sets it so the
// Makefile can echo it after `go test` finishes (test stdout is
// otherwise hidden without `-v`).
const defaultFsTestDir = "/tmp/ranke-go-test"

var fsTestDir string

func TestMain(m *testing.M) {
	fsTestDir = os.Getenv("RANKE_FS_DIR")
	if fsTestDir == "" {
		fsTestDir = defaultFsTestDir
	}
	// Empty + recreate so each run starts clean.
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
	a, err := ranke.NewArchive(ctx, mem.New(), ranke.NewMemBranchTableHead())
	require.NoError(t, err)
	IntegrationTest(t, ctx, func() ranke.Archive { return a })
}

func TestIntegrationFs(t *testing.T) {
	ctx := context.Background()
	IntegrationTest(t, ctx, func() ranke.Archive {
		u, err := fs.New(fsTestDir)
		require.NoError(t, err)
		bth, err := ranke.NewFsBranchTableHead(filepath.Join(fsTestDir, "B_h"))
		require.NoError(t, err)
		a, err := ranke.NewArchive(ctx, u, bth)
		require.NoError(t, err)
		return a
	})
}
