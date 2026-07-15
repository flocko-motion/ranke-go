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
	"strings"
	"time"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	goredis "github.com/redis/go-redis/v9"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/fs"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	neo4jstore "github.com/flocko-motion/ranke-go/adapter/storage/neo4j"
	redisstore "github.com/flocko-motion/ranke-go/adapter/storage/redis"
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
	Progress bool     // show an in-place per-chapter progress line (interactive CLI; off under go test)
}

// Backend is one matrix row: a named factory that spins up a FRESH, EMPTY
// local instance and returns it plus a cleanup func. Open returns
// ErrUnavailable when the backend can't run here.
type Backend struct {
	Name string
	Open func() (ranke.Universe, func(), error)
}

// AllBackends is the matrix. Durable byte-stores run standalone (mem, fs,
// sqlite, s3, redis). Neo4j is a graph-native CACHE — it holds no canonical
// CBOR and drops content over its cap, so it can never pass a full
// verification alone; it appears ONLY in stacks over a durable tier that holds
// the bytes and serves content misses (neo4j/mem; the production
// neo4j/redis/s3). Each row starts empty.
func AllBackends() []Backend {
	return []Backend{
		{"mem", openMem},
		{"fs", openFS},
		{"sqlite", openSqlite},
		{"s3", openS3},
		{"redis", openRedis},
		{"neo4j/mem", stacked(openNeo4j, openMem)},
		{"neo4j/redis/s3", stacked(openNeo4j, openRedis, openS3)},
	}
}

// --- component openers: each yields a fresh, empty instance + cleanup, or
// ErrUnavailable when it can't run here. Composed into standalone rows and,
// via stacked(), into cache stacks. ---

func openMem() (ranke.Universe, func(), error) { return mem.New(), func() {}, nil }

func openFS() (ranke.Universe, func(), error) {
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
}

func openSqlite() (ranke.Universe, func(), error) {
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
}

func openS3() (ranke.Universe, func(), error) {
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
}

func openRedis() (ranke.Universe, func(), error) {
	addr, pass, cleanup, err := redisConn()
	if err != nil {
		return nil, nil, err
	}
	client := goredis.NewClient(&goredis.Options{Addr: addr, Password: pass})
	u, err := redisstore.New(client)
	if err != nil {
		_ = client.Close()
		cleanup()
		return nil, nil, err
	}
	return u, func() { _ = client.Close(); cleanup() }, nil
}

func openNeo4j() (ranke.Universe, func(), error) {
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
	return neo4jstore.New(driver), func() {
		_ = driver.Close(context.Background())
		connCleanup()
	}, nil
}

// stacked composes openers top→bottom into one Universe: every layer but the
// last is a Lazy cache, the last is the Eager durable/authoritative tier. If
// any component is unavailable (ErrUnavailable) the whole stack is, and any
// already-opened components are cleaned up.
func stacked(openers ...func() (ranke.Universe, func(), error)) func() (ranke.Universe, func(), error) {
	return func() (ranke.Universe, func(), error) {
		var cleanups []func()
		cleanupAll := func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
		}
		layers := make([]stack.Layer, 0, len(openers))
		for i, open := range openers {
			u, cl, err := open()
			if err != nil {
				cleanupAll()
				return nil, nil, err
			}
			cleanups = append(cleanups, cl)
			if i == len(openers)-1 {
				layers = append(layers, stack.Eager(u)) // durable authoritative tier
			} else {
				layers = append(layers, stack.Lazy(u)) // cache tier
			}
		}
		st, err := stack.NewStack(layers...)
		if err != nil {
			cleanupAll()
			return nil, nil, err
		}
		return st, cleanupAll, nil
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
			rule := strings.Repeat("═", 88)
			fmt.Fprintf(w, "\n%s\n  %-14s  SKIP — %v\n%s\n", rule, be.Name, err, rule)
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

	// Banner up front, so a slow backend announces what it is working on before
	// the work starts.
	rule := strings.Repeat("═", 88)
	fmt.Fprintf(w, "\n%s\n  %-14s  size=%d\n%s\n", rule, name, cfg.Size, rule)
	// progress overwrites a single line in place while a chapter runs
	// (interactive only — piped/NO_COLOR output stays clean); it is cleared and
	// replaced by the results when the backend is done.
	showProgress := cfg.Progress && useColor
	progress := func(stage string) {
		if showProgress {
			fmt.Fprintf(w, "\r  \033[90m⏳ %-16s\033[0m\033[K", stage)
		}
	}

	// Chapter 1 — write.
	progress("write")
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
	progress("verify")
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
	progress("access:branch")
	u.setPhase("3a-branch")
	c3a := time.Now()
	for _, id := range ids {
		if _, err := branch.GetClaim(ctx, id); err != nil {
			return 0, fmt.Errorf("%s: branch access: %w", name, err)
		}
	}
	branchDur := time.Since(c3a)

	progress("access:universe")
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
	if showProgress {
		fmt.Fprint(w, "\r\033[K") // clear the progress line; results replace it
	}
	ms := func(d time.Duration) string { return d.Round(time.Millisecond).String() }
	fmt.Fprintf(w, "  claims=%d  verified=%d  accesses=%d\n", m.ClaimCount, run.Verified(), len(ids))
	fmt.Fprintf(w, "  write            %-9s (%dw %dr)\n", ms(writeDur), w1, r1)
	fmt.Fprintf(w, "  verify           %-9s (%dr)\n", ms(verifyDur), r2)
	fmt.Fprintf(w, "  access:branch    %-9s (%dr)  in-closure\n", ms(branchDur), r3a)
	fmt.Fprintf(w, "  access:universe  %-9s (%dr)  direct\n", ms(universeDur), r3b)
	fmt.Fprintf(w, "%s\n", u.report())
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
