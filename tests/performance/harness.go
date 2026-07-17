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
	Size      int      // generator size (SpecForSize); ~5×Size claims
	Seed      int64    // generator seed — fixes every id
	Access    int      // chapter-3 random accesses
	Backends  []string // backend names to run; empty = all
	Progress  bool     // show an in-place per-chapter progress line (interactive CLI; off under go test)
	QueryReps int      // times each RQL query is timed in chapter 4; 0 = 10
	Native    bool     // connect neo4j/redis to a host-native instance (localhost) instead of spawning podman pods
}

// forceNativeServices, set by RunMatrix from Config.Native, routes the
// pod-based services (neo4j, redis) to a host-native instance on localhost
// instead of an ephemeral podman pod. A run under go test leaves it false.
var forceNativeServices bool

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
	u, err := s3.New(client, bucket, s3.WithConcurrency(8))
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
	// A key prefix isolates a perf run's keys from any other tenant of the
	// same redis; a generous TTL lets a run's keys expire on their own; the
	// bulk ops fan out at moderate concurrency.
	u, err := redisstore.New(client,
		redisstore.WithKeyPrefix("rankeperf"),
		redisstore.WithTTL(time.Hour),
		redisstore.WithConcurrency(8))
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
	// Flush at the START, not teardown: each run wants a clean slate, but the
	// graph is left in place afterwards so it stays browsable (Neo4j Browser,
	// http://127.0.0.1:7474). This also clears any data a prior kept run left.
	if _, err := neo4jdriver.ExecuteQuery(context.Background(), driver,
		"MATCH (n) DETACH DELETE n", nil,
		neo4jdriver.EagerResultTransformer, neo4jdriver.ExecuteQueryWithDatabase("neo4j")); err != nil {
		_ = driver.Close(context.Background())
		connCleanup()
		return nil, nil, fmt.Errorf("flush neo4j: %w", err)
	}
	// Target the default database explicitly, and declare neo4j's inline
	// content cap (~4 KiB) so a stack over it can descend to a durable tier
	// for anything larger — the cap-aware read path the stack relies on.
	u := neo4jstore.New(driver,
		neo4jstore.WithDatabase("neo4j"),
		neo4jstore.WithContentCap(4096))
	return u, func() {
		_ = driver.Close(context.Background())
		connCleanup()
	}, nil
}

// stacked composes openers top→bottom into one Universe. Each adapter already
// carries its own write tier (Capabilities.Tier) — neo4j eager, redis lazy,
// mem/s3 authoritative — so the stack routes by what the layers report; there
// is nothing to wrap here. If any component is unavailable (ErrUnavailable) the
// whole stack is, and any already-opened components are cleaned up.
func stacked(openers ...func() (ranke.Universe, func(), error)) func() (ranke.Universe, func(), error) {
	return func() (ranke.Universe, func(), error) {
		var cleanups []func()
		cleanupAll := func() {
			for i := len(cleanups) - 1; i >= 0; i-- {
				cleanups[i]()
			}
		}
		layers := make([]ranke.Universe, 0, len(openers))
		for _, open := range openers {
			u, cl, err := open()
			if err != nil {
				cleanupAll()
				return nil, nil, err
			}
			cleanups = append(cleanups, cl)
			layers = append(layers, u)
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
	forceNativeServices = cfg.Native
	backends, err := selectBackends(cfg.Backends)
	if err != nil {
		return err
	}
	spec := generator.SpecForSize(cfg.Seed, cfg.Size)
	ctx := context.Background()

	// Generate the reference archive once into mem (the standard every backend
	// must match). From this single deterministic graph we (1) print an overview
	// of its shape — so the scale of a --size N run is legible before the matrix
	// — and (2) compute each query's reference result-set hash; a backend whose
	// results diverge is a determinism failure.
	refU := mem.New()
	refM, err := generator.Generate(ctx, refU, spec)
	if err != nil {
		return fmt.Errorf("reference generate: %w", err)
	}
	ov, err := computeOverview(ctx, refU, refM)
	if err != nil {
		return fmt.Errorf("graph overview: %w", err)
	}
	printOverview(w, spec, refM, ov)
	refStats, err := runQuerySet(ctx, refU, refM, 1, refU.Capabilities().Tags)
	if err != nil {
		return fmt.Errorf("mem reference queries: %w", err)
	}
	printQueryList(w, refStats)
	refHashes := make(map[string]string, len(refStats))
	for _, s := range refStats {
		refHashes[s.name] = s.hash
	}

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
		verified, err := runBackend(ctx, be.Name, spec, u0, cfg, w, refHashes)
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
func runBackend(ctx context.Context, name string, spec generator.Spec, u0 ranke.Universe, cfg Config, w io.Writer, refHashes map[string]string) (int, error) {
	u := newMetered(u0)
	defer func() { _ = u.Close() }()

	// Tags (branch membership) are a mutable per-claim overlay only some backends
	// hold. A bare byte store (fs, sqlite, s3, redis) holds none — normal — so it
	// skips the tag chapter and the branch-scoped chapters (branch access, the
	// branch queries), running the chapters it can: write, verify, universe access
	// and the $universe query. In production such a store sits under a tag-capable
	// layer (a stack), which is where the branch chapters are measured.
	taggable := u0.Capabilities().Tags

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

	// Setup — write: generate the test archive into the backend (via the dev
	// sequencer). Timed as ingest, but it is setup; the perf tests below measure
	// operations ON the resulting archive, the first of which is tagging.
	progress("write")
	u.setPhase("0-write")
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

	// Chapter 1 — tag: stamp each claim with its branch membership (_b_<branch>)
	// and each branch table with its revision (_br) — the overlay branch-scoped
	// reads and the browser's tag view rely on. The first operation measured on
	// the freshly-written archive; a real deployment runs it after a contribution.
	// Skipped on a backend that holds no tags (see taggable above).
	var tagDur time.Duration
	if taggable {
		progress("tag")
		u.setPhase("1-tag")
		ctag := time.Now()
		if _, err := ranke.TagArchive(ctx, arc); err != nil {
			return 0, fmt.Errorf("%s: tag: %w", name, err)
		}
		tagDur = time.Since(ctag)
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

	// Chapter 3 — random access: branch (in-closure) vs universe (direct). Sample
	// the ids from the "main" branch's own closure when the backend is taggable
	// (with several branches a manifest claim may live on another branch, so a
	// branch read of it would correctly miss); otherwise sample from the universe
	// head's closure, since only the universe (direct) read runs. Chapter 3a
	// (branch access) is skipped on a non-taggable backend.
	accessRoot := m.Head
	var branch ranke.Branch
	if taggable {
		branch, err = arc.GetBranch(ctx, "main")
		if err != nil {
			return 0, fmt.Errorf("%s: get branch: %w", name, err)
		}
		accessRoot = branch.Head()
	}
	ids, err := accessIDs(ctx, u, accessRoot, cfg.Access)
	if err != nil {
		return 0, fmt.Errorf("%s: access ids: %w", name, err)
	}
	var branchDur time.Duration
	if taggable {
		progress("access:branch")
		u.setPhase("3a-branch")
		c3a := time.Now()
		for _, id := range ids {
			if _, err := branch.GetClaim(ctx, id); err != nil {
				return 0, fmt.Errorf("%s: branch access: %w", name, err)
			}
		}
		branchDur = time.Since(c3a)
	}

	progress("access:universe")
	u.setPhase("3b-universe")
	c3b := time.Now()
	for _, id := range ids {
		if _, err := ranke.GetClaim(ctx, u, id); err != nil {
			return 0, fmt.Errorf("%s: universe access: %w", name, err)
		}
	}
	universeDur := time.Since(c3b)

	// Chapter 4 — RQL queries: each timed a few times (under its own "4.N" phase,
	// so it shows as a table row) and its result set hashed for the cross-backend
	// determinism check against the mem reference.
	progress("queries")
	qstats, err := runQuerySet(ctx, u, m, cfg.QueryReps, taggable)
	if err != nil {
		return 0, fmt.Errorf("%s: query: %w", name, err)
	}

	r1, w1 := u.phaseIO("0-write")
	r1t, w1t := u.phaseIO("1-tag")
	r2, _ := u.phaseIO("2-verify")
	r3a, _ := u.phaseIO("3a-branch")
	r3b, _ := u.phaseIO("3b-universe")
	if showProgress {
		fmt.Fprint(w, "\r\033[K") // clear the progress line; results replace it
	}
	// ms renders a duration; na shows "n/a" for a chapter this backend can't run
	// (a byte store holds no tags — so no tag or branch-scoped chapter).
	ms := func(d time.Duration) string { return d.Round(time.Millisecond).String() }
	na := func(d time.Duration) string {
		if !taggable {
			return "n/a"
		}
		return ms(d)
	}
	fmt.Fprintf(w, "  claims=%d  verified=%d  accesses=%d\n", m.ClaimCount, run.Verified(), len(ids))
	fmt.Fprintf(w, "  write            %-9s (%dw %dr)\n", ms(writeDur), w1, r1)
	fmt.Fprintf(w, "  tag              %-9s (%dw %dr)\n", na(tagDur), w1t, r1t)
	fmt.Fprintf(w, "  verify           %-9s (%dr)\n", ms(verifyDur), r2)
	fmt.Fprintf(w, "  access:branch    %-9s (%dr)  in-closure\n", na(branchDur), r3a)
	fmt.Fprintf(w, "  access:universe  %-9s (%dr)  direct\n", ms(universeDur), r3b)

	// Determinism: each query's ordered result set must hash-match the mem
	// reference. The timings themselves are the "4.N" rows in the table below.
	var diverged []string
	for _, qs := range qstats {
		if want, ok := refHashes[qs.name]; ok && want != qs.hash {
			diverged = append(diverged, qs.name)
		}
	}
	fmt.Fprintf(w, "%s\n", u.report())
	if len(diverged) > 0 {
		return run.Verified(), fmt.Errorf("%s: query result set(s) diverge from the mem reference (must be deterministic): %v", name, diverged)
	}
	return run.Verified(), nil
}

// accessIDs samples n claim ids (with replacement) from the closure reachable
// at root — a branch head — so every sampled id genuinely lives in that branch
// and both the branch read and the direct read resolve it. A fixed seed keeps
// the access pattern reproducible run to run.
func accessIDs(ctx context.Context, u ranke.Universe, root ranke.Id, n int) ([]ranke.Id, error) {
	if n <= 0 || root == nil {
		return nil, nil
	}
	rs, err := u.Query(ctx, ranke.Query{Select: ranke.Select{Branch: ranke.BranchUniverse, Claim: root}}, nil)
	if err != nil {
		return nil, err
	}
	var pool []ranke.Id
	for rs.Next() {
		pool = append(pool, rs.Result().Claim.ID())
	}
	if e := rs.Err(); e != nil {
		_ = rs.Close()
		return nil, e
	}
	if e := rs.Close(); e != nil {
		return nil, e
	}
	if len(pool) == 0 {
		return nil, nil
	}
	rng := rand.New(rand.NewSource(1)) // fixed seed → reproducible access order
	out := make([]ranke.Id, n)
	for i := range out {
		out[i] = pool[rng.Intn(len(pool))]
	}
	return out, nil
}
