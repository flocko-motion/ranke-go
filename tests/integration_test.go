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
// Intentionally NOT cleaned up — branches.json / claims / content
// stay around so the layout can be inspected.
//
// If $RANKE_FS_DIR is set, that path is used (Makefile passes it in
// and echoes it after the run, so it's visible on plain `go test`).
// Otherwise we MkdirTemp with the conventional prefix and print via
// TestMain — only surfaces with `go test -v`.
var fsTestDir string

func TestMain(m *testing.M) {
	if d := os.Getenv("RANKE_FS_DIR"); d != "" {
		fsTestDir = d
		if err := os.MkdirAll(fsTestDir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "tests/TestMain: MkdirAll:", err)
			os.Exit(1)
		}
	} else {
		var err error
		fsTestDir, err = os.MkdirTemp("", "ranke-test-fs-")
		if err != nil {
			fmt.Fprintln(os.Stderr, "tests/TestMain: MkdirTemp:", err)
			os.Exit(1)
		}
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
