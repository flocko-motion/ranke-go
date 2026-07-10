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
	seqfile "github.com/flocko-motion/ranke-go/adapter/sequencer/file"
	seqmem "github.com/flocko-motion/ranke-go/adapter/sequencer/mem"
	"github.com/flocko-motion/ranke-go/adapter/storage/fs"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
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

// seqSelf is the server contributor the test Sequencer attests branch
// advances with. Identity-Sign (no key), deterministic id, so re-opening
// across a Reset yields the same self.
func seqSelf() ranke.Contributor {
	c := must(ranke.ClaimBuilder{Type: ranke.NodeContributor, Content: []byte("sequencer")}.Sign())
	return must(c.AsContributor())
}

func TestIntegrationMem(t *testing.T) {
	ctx := context.Background()
	s, err := ranke.NewSequencer(ctx, mem.New(), seqmem.New(), seqSelf())
	require.NoError(t, err)
	IntegrationTest(t, ctx, func() ranke.Sequencer { return s })
}

func TestIntegrationFs(t *testing.T) {
	ctx := context.Background()
	IntegrationTest(t, ctx, func() ranke.Sequencer {
		u, err := fs.New(fsTestDir)
		require.NoError(t, err)
		bth, err := seqfile.New(filepath.Join(fsTestDir, "B_h"))
		require.NoError(t, err)
		s, err := ranke.NewSequencer(ctx, u, bth, seqSelf())
		require.NoError(t, err)
		return s
	})
}
