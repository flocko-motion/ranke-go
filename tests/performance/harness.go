// package: tests/performance / integration
// type:    tool
// job:     the reusable performance-matrix harness — generate a deterministic size-N archive into each backend and time the chapters (write / verify / random access), reporting per-step latency distributions
// limits:  decoupled from the testing package so both the _test.go entrypoint and cmd/test can drive it; timing only — the backend rows come from tests/backends and correctness belongs to tests/matrix
package performance

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"time"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	"github.com/flocko-motion/ranke-go/tests/backends"
	"github.com/flocko-motion/ranke-go/tests/generator"
)

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

// RunMatrix runs the chapters for each selected backend and writes the report
// to w. It returns an error on a real failure (verify failure, bad config); a
// backend that is unavailable (ErrUnavailable) is reported and skipped, not
// failed. onResult, if set, is called once per completed backend (used by the
// test to assert; the CLI leaves it nil).
func RunMatrix(cfg Config, w io.Writer, onResult func(backend string, verified int) error) error {
	backends.UseNativeServices(cfg.Native)
	rows, err := backends.Select(cfg.Backends)
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
	// deterministic standard — purely to describe the graph every backend is
	// about to be timed on. Whether the backends AGREE on what they read from it
	// is not asked here; that is the conformance matrix's question (tests/matrix).
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
	printQueryList(w, queryList(refM, cfg.Step))

	for _, be := range rows {
		progress("opening " + be.Name)
		u0, cleanup, err := be.Open()
		clearProgress()
		if errors.Is(err, backends.ErrUnavailable) {
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

	// queries: each timed a few times under its own "4.N" phase, so the latency
	// distribution lands in the metered table. The returned stats are already
	// reported through those phases, so nothing is kept here.
	if runQueries {
		progress("queries")
		if _, err = runQuerySet(ctx, u, m, cfg.QueryReps, taggable, cfg.Step, cfg.Report, w); err != nil {
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

	fmt.Fprintf(w, "%s\n", u.report())
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
