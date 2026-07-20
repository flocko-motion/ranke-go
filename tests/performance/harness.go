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
	Size      int      // target claim count (SpecForNodes); a dimension, not exact
	Seed      int64    // generator seed — fixes every id
	Access    int      // chapter-3 random accesses
	Backends  []string // backend names to run; empty = all
	Progress  bool     // show an in-place per-chapter progress line (interactive CLI; off under go test)
	QueryReps int      // times each RQL query is timed in chapter 4; 0 = 10
	Native    bool     // connect neo4j/redis to a host-native instance (localhost) instead of spawning podman pods
	Step      string   // run only this step (e.g. "2-verify", "3.1", "4.5"); "" = all. setup+tag always run.
	Report    bool     // print each query's full execution report in the queries section
	// Correctness runs the queries once on the mem reference and checks every
	// backend's result sets against it (the determinism analysis). Off by
	// default — it is the slow pre-matrix pass; the overview still prints.
	Correctness bool
}

// stepSelected reports whether the measured step with this phase id runs under
// cfg.Step. Empty Step runs all; otherwise Step must equal the id or its numeric
// prefix (the part before "-"), so "3.1" matches "3.1-branch" and "2" matches
// "2-verify". setup and tag are prerequisites gated separately.
func (cfg Config) stepSelected(id string) bool {
	if cfg.Step == "" || cfg.Step == id {
		return true
	}
	if i := strings.IndexByte(id, '-'); i >= 0 && cfg.Step == id[:i] {
		return true
	}
	return false
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
	spec := generator.SpecForNodes(cfg.Seed, cfg.Size)
	ctx := context.Background()

	// showProgress labels the otherwise-silent between-block phases (reference
	// generate, reference queries, opening each backend) with an in-place line —
	// interactive CLI only; a piped/NO_COLOR/go-test run stays clean.
	showProgress := cfg.Progress && useColor
	progress := func(stage string) {
		if showProgress {
			fmt.Fprintf(w, "\r  \033[90m⏳ %-52s\033[0m\033[K", stage)
		}
	}
	clearProgress := func() {
		if showProgress {
			fmt.Fprint(w, "\r\033[K")
		}
	}

	// Generate the reference archive once into mem — a fast, always-available,
	// deterministic standard. It powers the overview (graph shape, printed
	// upfront) always, and — under --correctness — the determinism baseline every
	// backend's query results are checked against.
	progress("generating reference archive")
	refU := mem.New()
	refM, err := generator.Generate(ctx, refU, spec)
	if err != nil {
		return fmt.Errorf("reference generate: %w", err)
	}
	ov, err := computeOverview(ctx, refU, refM)
	if err != nil {
		return fmt.Errorf("graph overview: %w", err)
	}
	clearProgress()
	printOverview(w, spec, refM, ov)

	// The query list needs no run; result counts + determinism hashes do, so the
	// reference query pass (the slowest pre-matrix step) runs only under
	// --correctness. Without it, backends still time their queries — they just
	// aren't checked against a baseline.
	refHashes := map[string]string{}
	var refStats []queryStat
	if cfg.Correctness {
		progress("running reference queries (result counts + determinism hashes)")
		refStats, err = runQuerySet(ctx, refU, refM, 1, refU.Capabilities().Tags, cfg.Step, false, w)
		if err != nil {
			return fmt.Errorf("mem reference queries: %w", err)
		}
		clearProgress()
		for _, s := range refStats {
			refHashes[s.name] = s.hash
		}
	} else {
		refStats = queryList(refM, cfg.Step)
	}
	printQueryList(w, refStats, cfg.Correctness)

	for _, be := range backends {
		progress("opening " + be.Name)
		u0, cleanup, err := be.Open()
		clearProgress()
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

	// Setup — write: generate the archive into the backend (via the dev
	// sequencer). Timed as ingest, but it is setup — the steps below measure
	// operations ON the resulting archive. Always run; every step needs it.
	progress("setup")
	u.setPhase("setup")
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

	// --step runs only the named step; setup always runs, and tag runs whenever
	// the target needs branch membership (tag, branch access, or a branch query).
	runVerify := cfg.stepSelected("2-verify")
	runBranch := taggable && cfg.stepSelected("3.1-branch")
	runUniverse := cfg.stepSelected("3.2-universe")
	runQueries := cfg.Step == "" || strings.HasPrefix(cfg.Step, "4")
	needTag := taggable && (cfg.Step == "" || cfg.stepSelected("1-tag") || runBranch || runQueries)

	// tag: stamp each claim's branch membership (_b_<branch>) and each branch
	// table's revision (_br) — what the branch-scoped reads rely on.
	var tagDur time.Duration
	if needTag {
		progress("tag")
		u.setPhase("1-tag")
		ctag := time.Now()
		if _, err := ranke.TagArchive(ctx, arc); err != nil {
			return 0, fmt.Errorf("%s: tag: %w", name, err)
		}
		tagDur = time.Since(ctag)
	}

	// verify: walk the provenance DAG and check every claim.
	var run ranke.VerificationRun
	var verifyDur time.Duration
	if runVerify {
		progress("verify")
		u.setPhase("2-verify")
		c2 := time.Now()
		run, err = arc.Verify(ctx, ranke.WithExternalContent())
		if err != nil {
			return 0, fmt.Errorf("%s: verify: %w", name, err)
		}
		run.Wait()
		verifyDur = time.Since(c2)
		if err := run.Err(); err != nil {
			return 0, fmt.Errorf("%s: verify: %w", name, err)
		}
		if fs := run.Failures(); len(fs) > 0 {
			return 0, fmt.Errorf("%s: %d verify failure(s), first: %v", name, len(fs), fs[0])
		}
	}

	// access: branch (in-closure) vs universe (direct), over ids sampled from the
	// branch closure (taggable) or the universe head's closure otherwise.
	var ids []ranke.Id
	var branchDur, universeDur time.Duration
	if runBranch || runUniverse {
		accessRoot := m.Head
		var branch ranke.Branch
		if runBranch {
			branch, err = arc.GetBranch(ctx, "main")
			if err != nil {
				return 0, fmt.Errorf("%s: get branch: %w", name, err)
			}
			accessRoot = branch.Head()
		}
		if ids, err = accessIDs(ctx, u, accessRoot, cfg.Access); err != nil {
			return 0, fmt.Errorf("%s: access ids: %w", name, err)
		}
		if runBranch {
			progress("access:branch")
			u.setPhase("3.1-branch")
			c3a := time.Now()
			for _, id := range ids {
				if _, err := branch.GetClaim(ctx, id); err != nil {
					return 0, fmt.Errorf("%s: branch access: %w", name, err)
				}
			}
			branchDur = time.Since(c3a)
		}
		if runUniverse {
			progress("access:universe")
			u.setPhase("3.2-universe")
			c3b := time.Now()
			for _, id := range ids {
				if _, err := ranke.GetClaim(ctx, u, id); err != nil {
					return 0, fmt.Errorf("%s: universe access: %w", name, err)
				}
			}
			universeDur = time.Since(c3b)
		}
	}

	// queries: each timed a few times (under its own "4.N" phase) and its result
	// set hashed for the cross-backend determinism check against the mem reference.
	var qstats []queryStat
	if runQueries {
		progress("queries")
		if qstats, err = runQuerySet(ctx, u, m, cfg.QueryReps, taggable, cfg.Step, cfg.Report, w); err != nil {
			return 0, fmt.Errorf("%s: query: %w", name, err)
		}
	}

	if showProgress {
		fmt.Fprint(w, "\r\033[K") // clear the progress line; results replace it
	}
	ms := func(d time.Duration) string { return d.Round(time.Millisecond).String() }
	verified := 0
	if run != nil {
		verified = run.Verified()
	}
	// full shows the "n/a" rows only on a full run — under --step the untouched
	// steps are simply omitted, not marked unavailable.
	full := cfg.Step == ""
	fmt.Fprintf(w, "  claims=%d  verified=%d  accesses=%d\n", m.ClaimCount, verified, len(ids))
	sr, sw := u.phaseIO("setup")
	fmt.Fprintf(w, "  setup            %-9s (%dw %dr)\n", ms(writeDur), sw, sr)
	if needTag {
		r, wr := u.phaseIO("1-tag")
		fmt.Fprintf(w, "  tag              %-9s (%dw %dr)\n", ms(tagDur), wr, r)
	} else if full && !taggable {
		fmt.Fprintf(w, "  tag              n/a\n")
	}
	if runVerify {
		r, _ := u.phaseIO("2-verify")
		fmt.Fprintf(w, "  verify           %-9s (%dr)\n", ms(verifyDur), r)
	}
	if runBranch {
		r, _ := u.phaseIO("3.1-branch")
		fmt.Fprintf(w, "  access:branch    %-9s (%dr)  in-closure\n", ms(branchDur), r)
	} else if full && !taggable {
		fmt.Fprintf(w, "  access:branch    n/a\n")
	}
	if runUniverse {
		r, _ := u.phaseIO("3.2-universe")
		fmt.Fprintf(w, "  access:universe  %-9s (%dr)  direct\n", ms(universeDur), r)
	}

	// Determinism: each query's ordered result set must hash-match the mem reference.
	var diverged []string
	for _, qs := range qstats {
		if want, ok := refHashes[qs.name]; ok && want != qs.hash {
			diverged = append(diverged, qs.name)
		}
	}
	fmt.Fprintf(w, "%s\n", u.report())
	if len(diverged) > 0 {
		return verified, fmt.Errorf("%s: query result set(s) diverge from the mem reference (must be deterministic): %v", name, diverged)
	}
	return verified, nil
}

// accessIDs samples n claim ids (with replacement) from the closure reachable
// at root — a branch head — so every sampled id genuinely lives in that branch
// and both the branch read and the direct read resolve it. A fixed seed keeps
// the access pattern reproducible run to run.
func accessIDs(ctx context.Context, u ranke.Universe, root ranke.Id, n int) ([]ranke.Id, error) {
	if n <= 0 || root == nil {
		return nil, nil
	}
	rs, err := u.Query(ctx, ranke.Query{Select: ranke.Select{Branch: ranke.BranchUniverse, Claim: root}}, ranke.Scope{Branch: ranke.BranchUniverse})
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
