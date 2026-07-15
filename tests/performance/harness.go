// package: tests/performance / integration
// type:    tool
// job:     the reusable performance-matrix harness — generate a deterministic size-N archive into each backend and time the chapters (write / verify / random access), reporting per-step latency distributions
// limits:  decoupled from the testing package so both the _test.go entrypoint and cmd/test can drive it; backends spawn fresh empty local instances (s3 via a podman MinIO pod) and return a cleanup + an ErrUnavailable when they can't run here
package performance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/fs"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	neo4jstore "github.com/flocko-motion/ranke-go/adapter/storage/neo4j"
	"github.com/flocko-motion/ranke-go/adapter/storage/s3"
	"github.com/flocko-motion/ranke-go/adapter/storage/sqlite"
	"github.com/flocko-motion/ranke-go/adapter/storage/stack"
	"github.com/flocko-motion/ranke-go/generator"
)

// ErrUnavailable is returned by a Backend.Open when the backend cannot run in
// this environment (e.g. s3 with no podman). The matrix reports it as skipped
// rather than a failure.
var ErrUnavailable = errors.New("backend unavailable")

// Config parameterises a matrix run — the knobs cmd/test exposes as flags.
type Config struct {
	Size     int      // generator size (SpecForSize); ~5×Size claims
	Seed     int64    // generator seed — fixes every id
	Access   int      // chapter-3 random accesses
	Backends []string // backend names to run; empty = all
}

// Backend is one matrix row: a named factory that spins up a FRESH, EMPTY
// local instance and returns it plus a cleanup func. Open returns
// ErrUnavailable when the backend can't run here.
type Backend struct {
	Name string
	Open func() (ranke.Universe, func(), error)
}

// AllBackends is the matrix: mem (in-process), fs (temp dir), sqlite (temp db),
// s3 (a MinIO podman pod). Each starts empty.
func AllBackends() []Backend {
	return []Backend{
		{"mem", func() (ranke.Universe, func(), error) {
			return mem.New(), func() {}, nil
		}},
		{"fs", func() (ranke.Universe, func(), error) {
			dir, err := os.MkdirTemp("", "ranke-perf-fs-")
			if err != nil {
				return nil, nil, err
			}
			u, err := fs.New(dir)
			if err != nil {
				_ = os.RemoveAll(dir)
				return nil, nil, err
			}
			return u, func() { _ = os.RemoveAll(dir) }, nil
		}},
		{"sqlite", func() (ranke.Universe, func(), error) {
			dir, err := os.MkdirTemp("", "ranke-perf-sqlite-")
			if err != nil {
				return nil, nil, err
			}
			u, err := sqlite.New(filepath.Join(dir, "perf.db"))
			if err != nil {
				_ = os.RemoveAll(dir)
				return nil, nil, err
			}
			return u, func() { _ = os.RemoveAll(dir) }, nil
		}},
		{"s3", func() (ranke.Universe, func(), error) {
			client, bucket, cleanup, err := minioPod()
			if err != nil {
				return nil, nil, err
			}
			u, err := s3.New(client, bucket)
			if err != nil {
				cleanup()
				return nil, nil, err
			}
			return u, cleanup, nil
		}},
		{"neo4j", func() (ranke.Universe, func(), error) {
			// Neo4j is a graph-native CACHE: it stores structure id-faithfully
			// but drops content over its cap and holds no external bytes, so it
			// CANNOT pass a full verification alone. Its real deployment — and
			// the only one that verifies — is stacked as a lazy cache over a
			// durable tier that holds the bytes and serves content misses.
			conn, connCleanup, err := neo4jConn()
			if err != nil {
				return nil, nil, err
			}
			driver, err := neo4jdriver.NewDriverWithContext(
				conn.BoltURI, neo4jdriver.BasicAuth(conn.User, conn.Password, ""))
			if err != nil {
				connCleanup()
				return nil, nil, err
			}
			u, err := stack.NewStack(
				stack.Lazy(neo4jstore.New(driver)), // graph cache on top
				stack.Eager(mem.New()),             // durable authoritative bytes below
			)
			if err != nil {
				_ = driver.Close(context.Background())
				connCleanup()
				return nil, nil, err
			}
			cleanup := func() {
				_ = driver.Close(context.Background())
				connCleanup()
			}
			return u, cleanup, nil
		}},
	}
}

// selectBackends filters AllBackends by cfg.Backends (empty = all), preserving
// matrix order. Unknown names are reported via the returned error.
func selectBackends(names []string) ([]Backend, error) {
	all := AllBackends()
	if len(names) == 0 {
		return all, nil
	}
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	var out []Backend
	for _, be := range all {
		if want[be.Name] {
			out = append(out, be)
			delete(want, be.Name)
		}
	}
	if len(want) > 0 {
		var unknown []string
		for n := range want {
			unknown = append(unknown, n)
		}
		return nil, fmt.Errorf("unknown backend(s): %v (have mem, fs, sqlite, s3)", unknown)
	}
	return out, nil
}

// RunMatrix runs the chapters for each selected backend and writes the report
// to w. It returns an error on a real failure (verify failure, bad config); a
// backend that is unavailable (ErrUnavailable) is reported and skipped, not
// failed. onResult, if set, is called once per completed backend (used by the
// test to assert; the CLI leaves it nil).
func RunMatrix(cfg Config, w io.Writer, onResult func(backend string, verified int) error) error {
	backends, err := selectBackends(cfg.Backends)
	if err != nil {
		return err
	}
	spec := generator.SpecForSize(cfg.Seed, cfg.Size)
	ctx := context.Background()

	for _, be := range backends {
		u0, cleanup, err := be.Open()
		if errors.Is(err, ErrUnavailable) {
			fmt.Fprintf(w, "backend=%-8s SKIP — %v\n", be.Name, err)
			continue
		}
		if err != nil {
			return fmt.Errorf("open %s: %w", be.Name, err)
		}
		verified, err := runBackend(ctx, be.Name, spec, u0, cfg, w)
		cleanup()
		if err != nil {
			return err
		}
		if onResult != nil {
			if err := onResult(be.Name, verified); err != nil {
				return err
			}
		}
	}
	return nil
}

// runBackend runs the three chapters against one open backend and writes its
// report block. Returns the number of claims verified.
func runBackend(ctx context.Context, name string, spec generator.Spec, u0 ranke.Universe, cfg Config, w io.Writer) (int, error) {
	u := newMetered(u0)
	defer func() { _ = u.Close() }()

	// Chapter 1 — write.
	u.setPhase("1-write")
	c1 := time.Now()
	m, err := generator.Generate(ctx, u, spec)
	if err != nil {
		return 0, fmt.Errorf("%s: generate: %w", name, err)
	}
	writeDur := time.Since(c1)

	arc, err := ranke.NewArchive(ctx, u, m.Head)
	if err != nil {
		return 0, fmt.Errorf("%s: open archive: %w", name, err)
	}

	// Chapter 2 — verify.
	u.setPhase("2-verify")
	c2 := time.Now()
	run, err := arc.Verify(ctx, ranke.WithExternalContent())
	if err != nil {
		return 0, fmt.Errorf("%s: verify: %w", name, err)
	}
	run.Wait()
	verifyDur := time.Since(c2)
	if err := run.Err(); err != nil {
		return 0, fmt.Errorf("%s: verify: %w", name, err)
	}
	if fs := run.Failures(); len(fs) > 0 {
		return 0, fmt.Errorf("%s: %d verify failure(s), first: %v", name, len(fs), fs[0])
	}

	// Chapter 3 — random access: branch (in-closure) vs universe (direct).
	ids := accessOrder(m, cfg.Access)
	branch, err := arc.GetBranch(ctx, "main")
	if err != nil {
		return 0, fmt.Errorf("%s: get branch: %w", name, err)
	}
	u.setPhase("3a-branch")
	c3a := time.Now()
	for _, id := range ids {
		if _, err := branch.GetClaim(ctx, id); err != nil {
			return 0, fmt.Errorf("%s: branch access: %w", name, err)
		}
	}
	branchDur := time.Since(c3a)

	u.setPhase("3b-universe")
	c3b := time.Now()
	for _, id := range ids {
		if _, err := ranke.GetClaim(ctx, u, id); err != nil {
			return 0, fmt.Errorf("%s: universe access: %w", name, err)
		}
	}
	universeDur := time.Since(c3b)

	r1, w1 := u.phaseIO("1-write")
	r2, _ := u.phaseIO("2-verify")
	r3a, _ := u.phaseIO("3a-branch")
	r3b, _ := u.phaseIO("3b-universe")
	fmt.Fprintf(w, "backend=%-8s size=%d claims=%d verified=%d accesses=%d\n"+
		"      ch1-write=%s (%dw %dr)  ch2-verify=%s (%dr)  ch3a-branch=%s (%dr)  ch3b-universe=%s (%dr)\n      %s\n",
		name, cfg.Size, m.ClaimCount, run.Verified(), len(ids),
		writeDur.Round(time.Millisecond), w1, r1,
		verifyDur.Round(time.Millisecond), r2,
		branchDur.Round(time.Millisecond), r3a,
		universeDur.Round(time.Millisecond), r3b,
		u.report())
	return run.Verified(), nil
}

// accessOrder builds a deterministic pseudo-random sequence of n claim ids from
// the manifest's content claims — a fixed seed so the access pattern is
// reproducible across runs and backends. Samples with replacement.
func accessOrder(m *generator.Manifest, n int) []ranke.Id {
	pool := make([]ranke.Id, 0, len(m.Sources)+len(m.Derivations)+len(m.Entities)+len(m.Relations))
	pool = append(pool, m.Sources...)
	pool = append(pool, m.Derivations...)
	pool = append(pool, m.Entities...)
	pool = append(pool, m.Relations...)
	if len(pool) == 0 || n <= 0 {
		return nil
	}
	rng := rand.New(rand.NewSource(1)) // fixed seed → reproducible access order
	out := make([]ranke.Id, n)
	for i := range out {
		out[i] = pool[rng.Intn(len(pool))]
	}
	return out
}
