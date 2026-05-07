// Integration tests — drive ranke.IntegrationTest against every
// Store backend the library ships, plus reload checkpoints. The same
// scenario suite is what 3rd-party Store implementations use to
// confirm conformance.
package ranke_test

import (
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/stretchr/testify/require"
)

func TestIntegrationMem(t *testing.T) {
	// Mem: the factory returns the same Store every call, so Reset
	// is a no-op (state cannot be lost because nothing leaves memory).
	s := ranke.NewMemStore()
	ranke.IntegrationTest(t, func() ranke.Store { return s })
}

func TestIntegrationFs(t *testing.T) {
	// Fs: the factory builds a fresh handle at the same dir each
	// call. Reset drops in-memory caches and re-reads branches.json;
	// the next claim/content access fetches from disk.
	dir := t.TempDir()
	ranke.IntegrationTest(t, func() ranke.Store {
		s, err := ranke.NewFsStore(dir)
		require.NoError(t, err)
		return s
	})
}
