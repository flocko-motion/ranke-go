// package: ranke / verify
// type:    logic
// job:     the configurable closure verifier — §5.10 per-claim integrity + authenticity over a graph, archive, or branch closure, as a live progress run
// limits:  does not fetch content bytes unless asked (WithExternalContent); does not persist or advance anything (-> universe, sequencer)
package ranke

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
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
	maxDepth        int             // 0 = unbounded
	maxClaims       int             // stop after n claims processed; 0 = unlimited
	createdAfter    time.Time       // prune claims created before this; zero = no bound
	trusted         func(Id) bool   // prune predicate; nil = trust nothing
	externalContent bool            // fetch + verify external content (default: inline only)
	stopAfter       int             // stop after n failures; 0 = verify everything
	onError         func(Failure)   // fired per failure, from the run goroutine
	skipRule        map[string]bool // verifyRules[].name to omit (WithSkipRules)
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

// WithSkipRules omits the named verification rules from the run. Names match
// VerifyRule.Name (see VerifyRuleSet) — a scan can drop expensive rules for
// duration reasons while keeping the rest. Unknown names are ignored.
func WithSkipRules(names ...string) VerifyOption {
	return func(c *verifyConfig) {
		if c.skipRule == nil {
			c.skipRule = make(map[string]bool, len(names))
		}
		for _, n := range names {
			c.skipRule[n] = true
		}
	}
}

// VerifyRule describes a registered verification rule for reports and for
// selecting which to skip (WithSkipRules): Name is the stable identifier,
// Rule the human-readable statement printed on violation.
type VerifyRule struct{ Name, Rule string }

// VerifyRuleSet lists the verification rules, in application order — the
// menu a caller picks from when deciding what to skip.
func VerifyRuleSet() []VerifyRule {
	out := make([]VerifyRule, len(verifyRules))
	for i, r := range verifyRules {
		out[i] = VerifyRule{Name: r.name, Rule: r.rule}
	}
	return out
}

func newVerifyConfig(opts ...VerifyOption) *verifyConfig {
	c := &verifyConfig{}
	for _, o := range opts {
		o(c)
	}
	return c
}

// --- the walk ---

// runVerification walks the closure from roots in the background, verifying
// each claim (§5.10), and returns a live handle. Everything comes from the
// one Universe u: claims via GetClaim, content via the node (inline) or
// GetContents/StreamContent (external). rootCheck, if set, validates each
// depth-0 root (e.g. an Archive requires a branch-table head).
func runVerification(ctx context.Context, roots []Id, u Universe, cfg *verifyConfig, rootCheck func(Claim) error) *verificationRun {
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

			// Fetch the stored raw CBOR — the exact bytes the id was signed
			// over — and decode it. Verification hashes this CBOR's node
			// preimage (never a re-encode, which would drift as the alias
			// taxonomy grows) and walks the stored (delta) edges, so we work
			// from the raw claim, not a materialised one. In a stack the
			// request routes to the byte layer that holds the CBOR; a
			// structure-only cache (neo4j) misses and falls through.
			raws, err := u.GetClaimsRaw(ctx, []Id{cur.id})
			if err != nil {
				run.fail(Failure{ID: cur.id, Depth: cur.depth, Err: err}, cfg.onError)
				if stop() {
					return
				}
				continue
			}
			raw := raws[0]
			c, err := DecodeClaim(cur.id, raw)
			if err != nil {
				run.fail(Failure{ID: cur.id, Depth: cur.depth, Err: err}, cfg.onError)
				if stop() {
					return
				}
				continue
			}

			if !cfg.createdAfter.IsZero() && c.Node().CreatedAt().Before(cfg.createdAfter) {
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

			if err := verifyClaim(ctx, c, raw, cfg, u); err != nil {
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
				for _, e := range c.Edges() {
					queue = append(queue, item{e.Reference(), cur.depth + 1})
				}
			}
		}
	}()
	return run
}

// verifyClaim runs every verification rule against one claim (§5.10). Each rule
// is a small, self-contained function checking one invariant; verifyRules is
// the registry the walk applies to every claim. Adding an invariant means
// adding a rule below and one entry to verifyRules — the rule's statement and
// its check live side by side, and the walk stays untouched.
func verifyClaim(ctx context.Context, c Claim, raw []byte, cfg *verifyConfig, u Universe) error {
	pubkey, err := resolveClaimPubkey(ctx, c, u)
	if err != nil {
		return wrapDetail(errVerify, "resolve pubkey", err)
	}
	t := &claimUnderVerification{claim: c, raw: raw, pubkey: pubkey, cfg: cfg, u: u}
	fail := func(r verifyRule, err error) error {
		return wrapDetail(errVerify, r.name+" — "+r.rule, err) // name the rule + its statement
	}
	// Per claim, and per content carrier (the node).
	for _, r := range verifyRules {
		if cfg.skipRule[r.name] {
			continue
		}
		if r.claim != nil {
			if err := r.claim(ctx, t); err != nil {
				return fail(r, err)
			}
		}
		if r.content != nil {
			if err := r.content(ctx, c.Node(), t); err != nil {
				return fail(r, err)
			}
		}
	}
	// Per edge — each edge is also a content carrier.
	for _, e := range c.Edges() {
		for _, r := range verifyRules {
			if cfg.skipRule[r.name] {
				continue
			}
			if r.content != nil {
				if err := r.content(ctx, e, t); err != nil {
					return fail(r, err)
				}
			}
			if r.edge != nil {
				if err := r.edge(ctx, e, t); err != nil {
					return fail(r, err)
				}
			}
		}
	}
	return nil
}

// verifyArchiveHead runs the archive-scoped rules against an archive's head
// claim; wired as runVerification's depth-0 root check by archive.Verify.
func verifyArchiveHead(ctx context.Context, head Claim, u Universe, cfg *verifyConfig) error {
	t := &claimUnderVerification{claim: head, cfg: cfg, u: u}
	for _, r := range verifyRules {
		if r.archive == nil || cfg.skipRule[r.name] {
			continue
		}
		if err := r.archive(ctx, t); err != nil {
			return wrapDetail(errVerify, r.name+" — "+r.rule, err)
		}
	}
	return nil
}

// claimUnderVerification bundles everything a rule may inspect about one claim:
// the decoded claim, its stored raw CBOR (the bytes the id was signed over),
// the resolved signing pubkey, the run config, and the Universe (to resolve
// references). Rules read; they do not mutate.
type claimUnderVerification struct {
	claim  Claim
	raw    []byte
	pubkey []byte
	cfg    *verifyConfig
	u      Universe
}

// verifyRule is one verification invariant: a short name, the rule stated in
// words (printed on violation, so a failure explains the invariant broken, not
// just a label), and up to four scoped checks — each optional:
//
//	claim   — once per claim
//	content — per content carrier: the node, and each edge (they share the
//	          same content_hash/content_size/bytes logic)
//	edge    — per edge (edge-specific, e.g. what it references)
//	archive — once against an archive's head
//
// The walk applies claim/content/edge checks to every claim; archive checks run
// once against the head (archive.Verify).
type verifyRule struct {
	name    string
	rule    string
	claim   func(ctx context.Context, t *claimUnderVerification) error
	content func(ctx context.Context, cc contentCarrier, t *claimUnderVerification) error
	edge    func(ctx context.Context, e Edge, t *claimUnderVerification) error
	archive func(ctx context.Context, t *claimUnderVerification) error
}

// verifyRules is the ordered rule set. Register an invariant here; its
// statement, scope, and implementation all live on this one entry.
var verifyRules = []verifyRule{
	{name: "§5.7 signature", rule: "a claim's id is a valid signature over H(S(v)) by its contributor's key", claim: ruleSignature},
	{name: "§4.1 height", rule: "height = 1 + max(reference heights), and 0 for an initial node", claim: ruleHeight},
	{name: "content integrity", rule: "content with a content_hash matches it and content_size (inline content is committed by the claim id)", content: ruleContent},
	{name: "content encoding", rule: "a node or edge that carries content declares an encoding (media type)", content: ruleContentEncoding},
	{name: "branch-table reference", rule: "a branch-table (contribution/branches) claim may be referenced only by another branch-table claim", edge: ruleBranchTableReference},
	{name: "archive head", rule: "an archive's head claim is a branch table (contribution/branches)", archive: ruleArchiveHead},
}

// ruleSignature: id(v) is a valid signature over H(preimage(raw)) by the
// claim's signing pubkey (hashes the stored bytes, never re-encodes).
func ruleSignature(_ context.Context, t *claimUnderVerification) error {
	return t.claim.verifyID(t.pubkey, t.raw)
}

// ruleHeight: §4.1 committed height matches the reference structure.
func ruleHeight(ctx context.Context, t *claimUnderVerification) error {
	return verifyHeight(ctx, t.claim, t.u)
}

// ruleContent (per content carrier): content that carries a content_hash must
// match it and content_size — external only when the run is configured for it.
// Native inline content has no hash and is committed by the claim id, so it
// needs no check here. The node and every edge share this logic, so one
// function serves both.
func ruleContent(ctx context.Context, cc contentCarrier, t *claimUnderVerification) error {
	return verifyContentRef(ctx, cc, t.cfg, t.u)
}

// ruleContentEncoding (per content carrier): a node or edge that carries
// content must declare an encoding (a MIME media type).
func ruleContentEncoding(_ context.Context, cc contentCarrier, _ *claimUnderVerification) error {
	if cc.GetContentHash() == nil {
		return nil // no content — no encoding required
	}
	if cc.Encoding() == "" {
		return errContentWithoutEncoding
	}
	return nil
}

// ruleBranchTableReference (per edge): a contribution/branches (branch-table)
// claim may be referenced ONLY by another branch-table claim. Branch tables
// form their own lineage (the diff chain of tables heading an archive); ordinary
// claims must never point into that archive-management layer. So if the owning
// claim is not a branch table, this edge may not reference one.
func ruleBranchTableReference(ctx context.Context, e Edge, t *claimUnderVerification) error {
	if t.claim.Node().Type() == NodeBranches {
		return nil // a branch table may reference branch tables (its lineage)
	}
	ref, err := GetClaim(ctx, t.u, e.Reference())
	if errors.Is(err, ErrNotFound) {
		return nil // dangling refs are caught elsewhere; judge only resolvable ones
	}
	if err != nil {
		return err
	}
	if ref.Node().Type() == NodeBranches {
		return fmt.Errorf("%s claim references branch table %s", t.claim.Node().Type(), e.Reference())
	}
	return nil
}

// ruleArchiveHead (per archive): an archive's head claim is a branch table
// (contribution/branches) — the root of the branch-table lineage.
func ruleArchiveHead(_ context.Context, t *claimUnderVerification) error {
	if t.claim.Node().Type() != NodeBranches {
		return fmt.Errorf("archive head is %s, want %s", t.claim.Node().Type(), NodeBranches)
	}
	return nil
}

// verifyID checks that this claim's id is a valid signature by pubkey over
// H(S(node)) — the node preimage extracted from the claim's stored raw CBOR,
// NOT re-encoded. Hashing the stored bytes (rather than re-running the encoder)
// keeps verification stable as the alias taxonomy grows: a newer encoder would
// emit the same claim more compactly, so re-encoding would change the hash and
// wrongly reject a valid claim. The claim does this itself (sealed) since it is
// a core-type crypto step no caller should hand-roll.
func (c *claim) verifyID(pubkey, raw []byte) error {
	preimage, err := nodePreimage(raw)
	if err != nil {
		return wrapDetail(errVerify, "preimage", err)
	}
	recomputed, err := hashContent(preimage)
	if err != nil {
		return wrapDetail(errVerify, "hash", err)
	}
	return verifySignature(pubkey, recomputed.raw, idBytes(c.node.id))
}

// verifyHeight re-derives the claim's generation number from its committed
// references and enforces the §4.1 invariant: height == 1 + max(reference
// heights), and == 0 for an initial node (no edges). Because the closure walk
// verifies every claim, this single-level check at each node transitively
// proves the recursive property over the whole graph — and rejects any height
// a builder committed that does not match the structure. It re-derives, never
// trusting the stored value it is checking.
func verifyHeight(ctx context.Context, c Claim, u Universe) error {
	edges := c.Edges()
	var want uint64
	if len(edges) > 0 {
		ids := make([]Id, len(edges))
		for i, e := range edges {
			ids[i] = e.Reference()
		}
		heights, err := u.GetClaimHeights(ctx, ids)
		if err != nil {
			return wrapDetail(errVerify, "resolve reference heights", err)
		}
		var max uint64
		for _, h := range heights {
			if h > max {
				max = h
			}
		}
		want = max + 1
	}
	if got := c.Node().Height(); got != want {
		return withDetail(errHeightMismatch, "got "+strconv.FormatUint(got, 10)+", want "+strconv.FormatUint(want, 10))
	}
	return nil
}

// resolveClaimPubkey returns the pubkey whose private key signed this claim's
// id (§5.7): the claim's own content for an initial node (no edges), else the
// content of the contributor named by its contribution/contributor edge.
// Purely interface-driven.
func resolveClaimPubkey(ctx context.Context, c Claim, u Universe) ([]byte, error) {
	// The pubkey is the content of the referenced contribution/contributor claim
	target, loc := c.ID(), ContentLocationOf(c.Node())
	viaEdge := false
	if edges := c.Edges(); len(edges) > 0 {
		target = nil
		for _, e := range edges {
			if e.TypeClass() == EdgeClassContribution && e.TypeSub() == "contributor" {
				// The contributor is another claim (and may be a diff, whose
				// pubkey is inherited); its location is unknown here, so let
				// GetClaimContent inspect it.
				target, loc, viaEdge = e.Reference(), ContentLocationUnknown, true
				break
			}
		}
		if target == nil {
			return nil, errNoContributorEdge
		}
	}
	rdr, err := GetClaimContent(ctx, u, target, loc)
	if err != nil {
		if viaEdge {
			return nil, wrapDetail(errContributorUnresolved, target.String(), err)
		}
		return nil, err
	}
	defer rdr.Close()
	return io.ReadAll(rdr)
}

// contentCarrier is the content-addressing surface shared by Node and Edge —
// enough to verify a content reference without the concrete type.
type contentCarrier interface {
	GetContentHash() Id
	GetContentSize() uint64
	IsContentExternal() bool
	GetInlineContent() ([]byte, error)
	Encoding() string
}

// verifyContentRef checks content integrity for one node/edge.
func verifyContentRef(ctx context.Context, cc contentCarrier, cfg *verifyConfig, u Universe) error {
	hash := cc.GetContentHash()
	if hash == nil {
		return nil // no content
	}
	if !cc.IsContentExternal() {
		inline, err := cc.GetInlineContent()
		if err != nil {
			return err
		}
		return VerifyContent(hash, cc.GetContentSize(), inline)
	}
	if !cfg.externalContent || u == nil {
		return nil // external content: skipped by default (may be huge)
	}
	rc, err := u.StreamContent(ctx, hash, cc.GetContentSize())
	if err != nil {
		return err
	}
	defer rc.Close()
	vr, err := NewVerifyingReader(rc, hash, cc.GetContentSize())
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, vr) // the reader verifies as it streams
	return err
}

// --- entry points ---

// Verify walks this graph's closure from every open head and verifies each
// claim (§5.10) against the graph's Universe.
func (g *graph) Verify(opts ...VerifyOption) VerificationRun {
	cfg := newVerifyConfig(opts...)
	return runVerification(context.Background(), g.Heads(), g.u, cfg, nil)
}

// Verify walks the archive's closure from its head and verifies each claim.
// It requires the head to be a contribution/branches claim (a branch table).
func (a *archive) Verify(ctx context.Context, opts ...VerifyOption) (VerificationRun, error) {
	cfg := newVerifyConfig(opts...)
	rootCheck := func(c Claim) error { return verifyArchiveHead(ctx, c, a.u, cfg) }
	return runVerification(ctx, []Id{a.bth.ID()}, a.u, cfg, rootCheck), nil
}

// Verify walks the branch's subgraph from its referenced root and verifies
// each claim.
func (b *branch) Verify(ctx context.Context, opts ...VerifyOption) (VerificationRun, error) {
	cfg := newVerifyConfig(opts...)
	return runVerification(ctx, []Id{b.Reference()}, b.u, cfg, nil), nil
}
