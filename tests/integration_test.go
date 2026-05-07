// Integration tests — drive IntegrationTest against every Archive
// backend the library ships. Same suite a 3rd-party backend uses,
// just called with our two example factories.
package tests

import (
	"fmt"
	"os"
	"testing"

	"github.com/flocko-motion/ranke-go"
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
	// Mem: the factory returns the same Archive every call, so Reset
	// is a no-op (state cannot be lost because nothing leaves memory).
	a := ranke.NewMemArchive()
	IntegrationTest(t, func() ranke.Archive { return a })
}

func TestIntegrationFs(t *testing.T) {
	// Fs: the factory builds a fresh handle at the same dir each
	// call. Reset drops in-memory caches and re-reads branches.json;
	// the next claim/content access fetches from disk.
	IntegrationTest(t, func() ranke.Archive {
		a, err := ranke.NewFsArchive(fsTestDir)
		require.NoError(t, err)
		return a
	})
}
