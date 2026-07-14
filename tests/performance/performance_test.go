// package: tests/performance / integration
// type:    test
// job:     a performance matrix — generate the same deterministic size-N archive into each storage backend and time build + verify, one subtest per backend
// limits:  blackbox (talks only to the ranke interfaces + adapter constructors); each backend runs against a fresh EMPTY local instance spawned in setup; size N comes from RANKE_PERF_SIZE (the make test/performance/N target sets it)
package performance

import (
	"context"
	"math/rand"
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
			u := newMetered(be.open(t)) // count round-trips + time each, for every backend
			defer func() { require.NoError(t, u.Close()) }()

			// Chapter 1 — write the graph. Generation drives the dev Sequencer,
			// which also verifies internally (paper step 3–4); that cost lives
			// in this chapter because it is inherent to writing via a Sequencer.
			u.setPhase("1-write")
			c1 := time.Now()
			m, err := generator.Generate(ctx, u, spec)
			require.NoError(t, err)
			writeDur := time.Since(c1)

			arc, err := ranke.NewArchive(ctx, u, m.Head)
			require.NoError(t, err)

			// Chapter 2 — full verification of the persisted archive.
			u.setPhase("2-verify")
			c2 := time.Now()
			run, err := arc.Verify(ctx, ranke.WithExternalContent())
			require.NoError(t, err)
			run.Wait()
			verifyDur := time.Since(c2)
			require.NoError(t, run.Err())
			require.Empty(t, run.Failures(), "the generated archive verifies on %s", be.name)

			// Chapter 3 — random access, two paths over the SAME ids:
			//   branch   — Branch.GetClaim → GetFromClosure, an in-closure check
			//              (walks the closure from the branch head per access)
			//   universe — GetClaim(ctx, u, id), a direct addressed read, no check
			// The gap is the price of the membership guarantee.
			ids := accessOrder(m, accessCount(t))
			branch, err := arc.GetBranch(ctx, "main")
			require.NoError(t, err)

			u.setPhase("3a-branch")
			c3a := time.Now()
			for _, id := range ids {
				_, err := branch.GetClaim(ctx, id)
				require.NoError(t, err)
			}
			branchDur := time.Since(c3a)

			u.setPhase("3b-universe")
			c3b := time.Now()
			for _, id := range ids {
				_, err := ranke.GetClaim(ctx, u, id)
				require.NoError(t, err)
			}
			universeDur := time.Since(c3b)

			r1, w1 := u.phaseIO("1-write")
			r2, _ := u.phaseIO("2-verify")
			r3a, _ := u.phaseIO("3a-branch")
			r3b, _ := u.phaseIO("3b-universe")
			t.Logf("backend=%-8s size=%d claims=%d verified=%d accesses=%d\n"+
				"      ch1-write=%s (%dw %dr)  ch2-verify=%s (%dr)  ch3a-branch=%s (%dr)  ch3b-universe=%s (%dr)\n      %s",
				be.name, size, m.ClaimCount, run.Verified(), len(ids),
				writeDur.Round(time.Millisecond), w1, r1,
				verifyDur.Round(time.Millisecond), r2,
				branchDur.Round(time.Millisecond), r3a,
				universeDur.Round(time.Millisecond), r3b,
				u.report())
		})
	}
}

// accessCount is how many random accesses chapter 3 performs, from
// RANKE_PERF_ACCESS (default 50). The branch path is O(closure) per access, so
// this stays modest — the point is the per-access latency contrast, not
// throughput.
func accessCount(t *testing.T) int {
	v := os.Getenv("RANKE_PERF_ACCESS")
	if v == "" {
		return 50
	}
	n, err := strconv.Atoi(v)
	require.NoError(t, err, "RANKE_PERF_ACCESS must be an integer")
	require.Positive(t, n)
	return n
}

// accessOrder builds a deterministic pseudo-random sequence of n claim ids
// drawn from the manifest's content claims (sources, derivations, entities,
// relations) — a fixed seed so the access pattern is reproducible across runs
// and backends. Sampling with replacement when the pool is smaller than n.
func accessOrder(m *generator.Manifest, n int) []ranke.Id {
	pool := make([]ranke.Id, 0, len(m.Sources)+len(m.Derivations)+len(m.Entities)+len(m.Relations))
	pool = append(pool, m.Sources...)
	pool = append(pool, m.Derivations...)
	pool = append(pool, m.Entities...)
	pool = append(pool, m.Relations...)
	if len(pool) == 0 {
		return nil
	}
	rng := rand.New(rand.NewSource(1)) // fixed seed → reproducible access order
	out := make([]ranke.Id, n)
	for i := range out {
		out[i] = pool[rng.Intn(len(pool))]
	}
	return out
}
