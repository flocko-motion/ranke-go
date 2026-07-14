// package: tests/performance / integration
// type:    test
// job:     a performance matrix — generate the same deterministic size-N archive into each storage backend and time build + verify, one subtest per backend
// limits:  blackbox (talks only to the ranke interfaces + adapter constructors); each backend runs against a fresh EMPTY local instance spawned in setup; size N comes from RANKE_PERF_SIZE (the make test/performance/N target sets it)
package performance

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/fs"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	"github.com/flocko-motion/ranke-go/adapter/storage/s3"
	"github.com/flocko-motion/ranke-go/adapter/storage/sqlite"
	"github.com/flocko-motion/ranke-go/generator"
	"github.com/stretchr/testify/require"
)

// backend is one row of the matrix: a named Universe factory. Each factory
// spawns a FRESH, EMPTY local instance of its backend in test setup, so every
// row starts from nothing and the timings are comparable. Adding a backend is
// a single line in backends(). Factories take *testing.T for scratch space
// (t.TempDir) and cleanup (t.Cleanup).
type backend struct {
	name string
	open func(t *testing.T) ranke.Universe
}

// backends returns the matrix rows: mem (in-process), fs (a fresh temp dir),
// sqlite (a fresh temp db file), and s3 (a fresh in-process gofakes3 server
// with one empty bucket). Each is empty at the start of its subtest.
func backends() []backend {
	return []backend{
		{"mem", func(*testing.T) ranke.Universe {
			return mem.New()
		}},
		{"fs", func(t *testing.T) ranke.Universe {
			u, err := fs.New(t.TempDir())
			require.NoError(t, err)
			return u
		}},
		{"sqlite", func(t *testing.T) ranke.Universe {
			u, err := sqlite.New(filepath.Join(t.TempDir(), "perf.db"))
			require.NoError(t, err)
			return u
		}},
		{"s3", func(t *testing.T) ranke.Universe {
			client, bucket := minioPod(t) // real MinIO in a podman pod (minio_pod_test.go)
			u, err := s3.New(client, bucket)
			require.NoError(t, err)
			return u
		}},
	}
}

// perfSize reads the target graph size from RANKE_PERF_SIZE (set by
// `make test/performance/N`), defaulting small so a bare `go test` stays fast.
func perfSize(t *testing.T) int {
	v := os.Getenv("RANKE_PERF_SIZE")
	if v == "" {
		return 100
	}
	n, err := strconv.Atoi(v)
	require.NoError(t, err, "RANKE_PERF_SIZE must be an integer")
	require.Positive(t, n, "RANKE_PERF_SIZE must be positive")
	return n
}

// TestPerformanceMatrix generates a deterministic size-N archive into every
// backend in the matrix and times build + verify, logging a row per backend.
// Same N + backend → identical archive (the generator is deterministic), so
// runs are comparable across backends and across time.
func TestPerformanceMatrix(t *testing.T) {
	size := perfSize(t)
	spec := generator.SpecForSize(1, size)

	for _, be := range backends() {
		be := be
		t.Run(be.name, func(t *testing.T) {
			ctx := context.Background()
			u := be.open(t)
			defer func() { require.NoError(t, u.Close()) }()

			genStart := time.Now()
			m, err := generator.Generate(ctx, u, spec)
			require.NoError(t, err)
			genDur := time.Since(genStart)

			arc, err := ranke.NewArchive(ctx, u, m.Head)
			require.NoError(t, err)

			verStart := time.Now()
			run, err := arc.Verify(ctx, ranke.WithExternalContent())
			require.NoError(t, err)
			run.Wait()
			verDur := time.Since(verStart)
			require.NoError(t, run.Err())
			require.Empty(t, run.Failures(), "the generated archive verifies on %s", be.name)

			t.Logf("backend=%-8s size=%d claims=%d verified=%d generate=%s verify=%s",
				be.name, size, m.ClaimCount, run.Verified(),
				genDur.Round(time.Millisecond), verDur.Round(time.Millisecond))
		})
	}
}
