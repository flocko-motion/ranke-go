// package: ranke / verify
// type:    logic
// job:     the configurable closure verifier — §5.10 per-claim integrity + authenticity over a graph, archive, or branch closure, as a live progress run
// limits:  does not fetch content bytes unless asked (WithExternalContent); does not persist or advance anything (-> universe, sequencer)
package ranke

import (
	"context"
	"io"
	"sync"
	"time"
)

// Failure is one verification failure: the claim that failed, its depth in
// the walk, and why.
type Failure struct {
	ID    Id
	Depth int
	Err   error
}

// VerificationRun is a live handle on a running (or finished) verification.
// It is safe to read while the walk runs — poll for progress, or Wait for
// completion.
type VerificationRun interface {
	// Verified is the number of claims that passed so far.
	Verified() int
	// Failures is a snapshot of the failures found so far.
	Failures() []Failure
	// Done reports whether the walk has finished (completed or stopped).
	Done() bool
	// Err is a terminal error that aborted the walk (a load failure,
	// ctx cancellation) — distinct from per-claim Failures. Nil otherwise.
	Err() error
	// Wait blocks until the walk is Done.
	Wait()
}

type verificationRun struct {
	mu       sync.Mutex
	verified int
	failures []Failure
	done     bool
	err      error
	doneCh   chan struct{}
}

func newRun() *verificationRun { return &verificationRun{doneCh: make(chan struct{})} }

func (r *verificationRun) Verified() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.verified
}

func (r *verificationRun) Failures() []Failure {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Failure, len(r.failures))
	copy(out, r.failures)
	return out
}

func (r *verificationRun) Done() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}

func (r *verificationRun) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *verificationRun) Wait() { <-r.doneCh }

func (r *verificationRun) pass() {
	r.mu.Lock()
	r.verified++
	r.mu.Unlock()
}

func (r *verificationRun) fail(f Failure, onError func(Failure)) int {
	r.mu.Lock()
	r.failures = append(r.failures, f)
	n := len(r.failures)
	r.mu.Unlock()
	if onError != nil {
		onError(f)
	}
	return n
}

func (r *verificationRun) abort(err error) {
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
}

func (r *verificationRun) finish() {
	r.mu.Lock()
	r.done = true
	r.mu.Unlock()
	close(r.doneCh)
}

// --- configuration ---

type verifyConfig struct {
	maxDepth        int           // 0 = unbounded
	maxClaims       int           // stop after n claims processed; 0 = unlimited
	createdAfter    time.Time     // prune claims created before this; zero = no bound
	trusted         func(Id) bool // prune predicate; nil = trust nothing
	externalContent bool          // fetch + verify external content (default: inline only)
	stopAfter       int           // stop after n failures; 0 = verify everything
	onError         func(Failure) // fired per failure, from the run goroutine
}

// VerifyOption configures a verification run.
type VerifyOption func(*verifyConfig)

// WithMaxDepth bounds the closure walk to depth n (0 = full closure).
func WithMaxDepth(n int) VerifyOption { return func(c *verifyConfig) { c.maxDepth = n } }

// WithMaxClaims stops the walk after n claims have been processed (0 =
// unlimited) — a hard work cap independent of depth.
func WithMaxClaims(n int) VerifyOption { return func(c *verifyConfig) { c.maxClaims = n } }

// WithCreatedAfter prunes any claim whose created_at is before t (skips it
// and its older references). Since the closure walks toward older
// references (monotonicity), this bounds verification to a recent window.
func WithCreatedAfter(t time.Time) VerifyOption { return func(c *verifyConfig) { c.createdAfter = t } }

// WithTrusted prunes the walk at any claim for which fn returns true —
// already-verified / committed claims. Backed by whatever the caller has
// (DB, bloom filter, the Sequencer's committed set); no id list to build.
func WithTrusted(fn func(Id) bool) VerifyOption { return func(c *verifyConfig) { c.trusted = fn } }

// WithExternalContent also fetches and verifies externalized content
// (default: inline content only — external blobs can be gigabytes).
func WithExternalContent() VerifyOption { return func(c *verifyConfig) { c.externalContent = true } }

// WithStopAfter stops the walk once n failures are found (1 = fail fast,
// 0 = verify everything).
func WithStopAfter(n int) VerifyOption { return func(c *verifyConfig) { c.stopAfter = n } }

// WithOnError registers a callback fired as each failure is found. It runs
// on the run's goroutine, so it must be cheap and concurrency-safe.
func WithOnError(fn func(Failure)) VerifyOption { return func(c *verifyConfig) { c.onError = fn } }

func newVerifyConfig(opts ...VerifyOption) *verifyConfig {
	c := &verifyConfig{}
	for _, o := range opts {
		o(c)
	}
	return c
}

// --- the walk ---

type fetchFunc func(context.Context, Id) (*claim, error)

// runVerification walks the closure from roots in the background, verifying
// each claim (§5.10), and returns a live handle. fetch obtains a claim by
// id (an in-memory map lookup for a Graph, a Universe load for an
// Archive/Branch); u is the Universe for external-content fetches (nil for
// an in-memory graph). rootCheck, if set, validates each depth-0 root
// (e.g. an Archive requires a branch-table head).
func runVerification(ctx context.Context, roots []Id, fetch fetchFunc, u Universe, cfg *verifyConfig, rootCheck func(*claim) error) *verificationRun {
	run := newRun()
	go func() {
		defer run.finish()

		type item struct {
			id    Id
			depth int
		}
		seen := map[string]struct{}{}
		queue := make([]item, 0, len(roots))
		for _, id := range roots {
			queue = append(queue, item{id, 0})
		}

		stop := func() bool { return cfg.stopAfter > 0 && len(run.failures) >= cfg.stopAfter }
		processed := 0

		for len(queue) > 0 {
			if err := ctx.Err(); err != nil {
				run.abort(err)
				return
			}
			cur := queue[0]
			queue = queue[1:]

			k := cur.id.String()
			if _, ok := seen[k]; ok {
				continue
			}
			seen[k] = struct{}{}

			if cfg.trusted != nil && cfg.trusted(cur.id) {
				continue // pruned: already trusted/committed
			}

			c, err := fetch(ctx, cur.id)
			if err != nil {
				run.fail(Failure{ID: cur.id, Depth: cur.depth, Err: err}, cfg.onError)
				if stop() {
					return
				}
				continue
			}

			if !cfg.createdAfter.IsZero() && c.node.createdAt.Before(cfg.createdAfter) {
				continue // pruned: older than the created_at bound
			}

			if cur.depth == 0 && rootCheck != nil {
				if err := rootCheck(c); err != nil {
					run.fail(Failure{ID: cur.id, Depth: 0, Err: err}, cfg.onError)
					if stop() {
						return
					}
				}
			}

			if err := verifyClaim(ctx, c, fetch, cfg, u); err != nil {
				run.fail(Failure{ID: cur.id, Depth: cur.depth, Err: err}, cfg.onError)
				if stop() {
					return
				}
			} else {
				run.pass()
			}

			processed++
			if cfg.maxClaims > 0 && processed >= cfg.maxClaims {
				return // hit the work cap
			}

			// Descend into the claim's own (delta) edge references — this
			// reaches the full closure, including a diff predecessor and,
			// through it, inherited entries.
			if cfg.maxDepth == 0 || cur.depth < cfg.maxDepth {
				for _, e := range c.edges {
					queue = append(queue, item{e.reference, cur.depth + 1})
				}
			}
		}
	}()
	return run
}

// verifyClaim runs the §5.10 check on one claim: recompute H(S(node)) and
// verify the signature against id(v) (integrity + authenticity, §5.2 +
// §5.7), then verify content integrity (inline always; external only when
// configured).
func verifyClaim(ctx context.Context, c *claim, fetch fetchFunc, cfg *verifyConfig, u Universe) error {
	encoded, err := encodeNode(c.node)
	if err != nil {
		return wrapDetail(errVerify, "encode", err)
	}
	recomputed, err := hashContent(encoded)
	if err != nil {
		return wrapDetail(errVerify, "hash", err)
	}
	pubkey, err := resolveClaimPubkey(ctx, c, fetch)
	if err != nil {
		return wrapDetail(errVerify, "resolve pubkey", err)
	}
	idH, ok := c.node.id.(*id)
	if !ok {
		return errForeignIdType
	}
	if err := verifySignature(pubkey, recomputed.raw, idH.raw); err != nil {
		return wrapDetail(errVerify, "§5.7", err)
	}

	// Content integrity: node, then each edge.
	if err := verifyContentRef(ctx, c.node.contentHash, c.node.contentSize, c.node.content, cfg, u); err != nil {
		return wrapDetail(errVerify, "node content", err)
	}
	for _, e := range c.edges {
		if err := verifyContentRef(ctx, e.contentHash, e.contentSize, e.content, cfg, u); err != nil {
			return wrapDetail(errVerify, "edge content", err)
		}
	}
	return nil
}

// resolveClaimPubkey returns the pubkey whose private key signed this
// claim's id (§5.7). A contributor carries its pubkey as its content, so
// this is the claim's own content for an initial node (no edges), else the
// content of the contributor referenced by its contribution/contributor
// edge (fetched via fetch).
func resolveClaimPubkey(ctx context.Context, c *claim, fetch fetchFunc) ([]byte, error) {
	if len(c.edges) == 0 {
		return c.node.content, nil // initial node: pubkey is its content
	}
	for _, e := range c.edges {
		if e.typeClass == EdgeClassContribution && e.typeSub == "contributor" {
			contributor, err := fetch(ctx, e.reference)
			if err != nil {
				return nil, wrapDetail(errContributorUnresolved, e.reference.String(), err)
			}
			return contributor.node.content, nil // contributor's pubkey is its content
		}
	}
	return nil, errNoContributorEdge
}

// verifyContentRef checks content integrity for one content reference.
// Inline content is always re-hashed; external content is fetched and
// verified only when cfg.externalContent is set and a Universe is available.
func verifyContentRef(ctx context.Context, hash Id, size uint64, inline []byte, cfg *verifyConfig, u Universe) error {
	if hash == nil {
		return nil // no content
	}
	if inline != nil {
		return VerifyContent(hash, size, inline)
	}
	if !cfg.externalContent || u == nil {
		return nil // external content: skipped by default (may be huge)
	}
	rc, err := u.StreamContent(ctx, hash, size)
	if err != nil {
		return err
	}
	defer rc.Close()
	vr, err := NewVerifyingReader(rc, hash, size)
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, vr) // the reader verifies as it streams
	return err
}

// --- entry points ---

// Verify walks this in-memory graph from every open head and verifies each
// claim (§5.10, inline content). External content is not available to an
// in-memory graph.
func (g *graph) Verify(opts ...VerifyOption) VerificationRun {
	cfg := newVerifyConfig(opts...)
	fetch := func(_ context.Context, id Id) (*claim, error) {
		c, ok := g.claims[id.String()]
		if !ok {
			return nil, withDetail(errRefMissingClaim, id.String())
		}
		return c, nil
	}
	return runVerification(context.Background(), g.Heads(), fetch, nil, cfg, nil)
}

// universeFetch loads and unwraps a claim from u (materialising diffs).
func universeFetch(u Universe) fetchFunc {
	return func(ctx context.Context, id Id) (*claim, error) {
		cl, err := GetClaim(ctx, u, id)
		if err != nil {
			return nil, err
		}
		c, ok := cl.(*claim)
		if !ok {
			return nil, errForeignClaim
		}
		return c, nil
	}
}

// Verify walks the archive's closure from its head and verifies each claim.
// It requires the head to be a contribution/branches claim (a branch table).
func (a *archive) Verify(ctx context.Context, opts ...VerifyOption) (VerificationRun, error) {
	cfg := newVerifyConfig(opts...)
	rootCheck := func(c *claim) error {
		if c.node.typeClass == NodeClassContribution && c.node.typeSub == string(NodeSubtypeBranches) {
			return nil
		}
		return errNotBranchTable
	}
	return runVerification(ctx, []Id{a.bth.ID()}, universeFetch(a.u), a.u, cfg, rootCheck), nil
}

// Verify walks the branch's subgraph from its referenced root and verifies
// each claim.
func (b *branch) Verify(ctx context.Context, opts ...VerifyOption) (VerificationRun, error) {
	cfg := newVerifyConfig(opts...)
	return runVerification(ctx, []Id{b.Reference()}, universeFetch(b.u), b.u, cfg, nil), nil
}
