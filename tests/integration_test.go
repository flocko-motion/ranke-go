// Integration tests — drive ranke.IntegrationTest against every
// Archive backend the library ships, plus reload checkpoints. The same
// scenario suite is what 3rd-party Archive implementations use to
// confirm conformance.
package ranke_test

import (
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

func TestIntegrationMem(t *testing.T) {
	// Mem: the factory returns the same Archive every call, so Reset
	// is a no-op (state cannot be lost because nothing leaves memory).
	s := ranke.NewMemArchive()
	ranke.IntegrationTest(t, func() ranke.Archive { return s })
}

func TestIntegrationFs(t *testing.T) {
	// Fs: the factory builds a fresh handle at the same dir each
	// call. Reset drops in-memory caches and re-reads branches.json;
	// the next claim/content access fetches from disk.
	dir := t.TempDir()
	ranke.IntegrationTest(t, func() ranke.Archive {
		s, err := ranke.NewFsArchive(dir)
		require.NoError(t, err)
		return s
	})
}
