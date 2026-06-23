package sqlite

import (
	"path/filepath"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/adaptertest"
)

// TestConformance runs the shared black-box Universe suite against the
// sqlite adapter, each universe backed by a fresh on-disk database in a
// temp dir (a file, not :memory:, so the connection pool sees one store).
func TestConformance(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) ranke.Universe {
		dsn := filepath.Join(t.TempDir(), "ranke.db")
		u, err := New(dsn)
		if err != nil {
			t.Fatalf("sqlite.New: %v", err)
		}
		t.Cleanup(func() { _ = u.Close() })
		return u
	})
}
