// Integration tests — drive IntegrationTest against every Archive
// backend the library ships. Same suite a 3rd-party backend uses,
// just called with our two example factories.
package tests

import (
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

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
	dir := t.TempDir()
	IntegrationTest(t, func() ranke.Archive {
		a, err := ranke.NewFsArchive(dir)
		require.NoError(t, err)
		return a
	})
}
