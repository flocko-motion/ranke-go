// package: ranke / archive
// type:    logic
// job:     an immutable (𝒰, B_h@H) read snapshot; reads claims and branches and Prepares a verified, height-stamped contribution
// limits:  advances no branch state — only the Sequencer moves B_h (-> sequencer); does not itself verify claim bytes (-> graph)
package ranke

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Archive is an immutable read snapshot of a Ranke-Archive (spec §4.8):
// the (𝒰, B_h) tuple pinned at a height. Branches and graphs are
// projections of claims that live in the underlying Universe. Reads run
// on the snapshot; the one mutating operation, advancing B_h, belongs to
// the Sequencer. Prepare is a read that verifies a contribution and
// mints a SealedProof for Submit — it persists claims into the
// content-addressed 𝒰 (idempotent) but moves no branch. Every method
// takes a ctx threaded from the entry point.
//
// Snapshot lifetime: a claim reachable from a live snapshot stays
// resolvable for that snapshot's lifetime — a reader holding an Archive
// never has the ground pulled from under it. This holds trivially on the
// append-only mem/fs backends today; any future garbage collection or
// eviction must honour it (retain what a live snapshot can reach).
type Archive interface {
	HasClaim(ctx context.Context, id Id) bool
	// GetClaim returns the claim at id. Call .Graph(ctx) on the
	// result to materialize the provenance.
	GetClaim(ctx context.Context, id Id) (Claim, error)

	HasBranch(ctx context.Context, name string) bool
	GetBranch(ctx context.Context, name string) (Branch, error)
	Branches(ctx context.Context) []Branch

	// Prepare verifies a contribution against this snapshot and returns
	// a height-stamped SealedProof for Sequencer.Submit. It is the slow,
	// concurrent half of the write pipeline (signatures, id-chain,
	// content re-hash), safe to run off the Sequencer's lock: every
	// claim in g is absorbed into 𝒰 (idempotent, content-addressed) and
	// the delta is verified against the branch's prior head via
	// VerifyDelta, trusting the already-committed closure. contributor
	// signs the contribution/head that consolidates g's open heads.
	Prepare(ctx context.Context, branchName string, g Graph, contributor Contributor, createdAt ...time.Time) (SealedProof, error)

	// VerifyBranch loads the claim at the branch's latest head and
	// runs the §5.10 checks across its provenance.
	VerifyBranch(ctx context.Context, name string) error
}

// NewArchive opens an immutable read snapshot over a Universe (𝒰) at the
// current B_h. The Archive holds no resources of its own — closing it
// does not close u; the caller manages the lifetime, which is what lets
// multiple Archives share one Universe. Writers use NewSequencer, whose
// GetArchive hands out snapshots bound to the live height.
func NewArchive(ctx context.Context, u Universe, bth BranchTableHead) (Archive, error) {
	if u == nil {
		return nil, errors.New("ranke.NewArchive: nil Universe")
	}
	if bth == nil {
		return nil, errors.New("ranke.NewArchive: nil BranchTableHead")
	}
	bh, err := bth.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewArchive: load B_h: %w", err)
	}
	table, err := LoadBranchTable(ctx, u, bh)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewArchive: load branch table: %w", err)
	}
	h, err := tableHeight(ctx, table)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewArchive: height: %w", err)
	}
	return newArchiveSnapshot(u, table, h), nil
}

// newArchiveSnapshot builds a read snapshot over u pinned at table/height.
// Shared by NewArchive and Sequencer.GetArchive.
func newArchiveSnapshot(u Universe, table BranchTable, height Height) *archive {
	return &archive{
		u:       u,
		table:   table,
		height:  height,
		claims:  make(map[string]*claim),
		content: make(map[string][]byte),
	}
}

// tableHeight is the archive height a branch table sits at: the number
// of contribution/branches revisions in its chain. The empty table is 0.
func tableHeight(ctx context.Context, table BranchTable) (Height, error) {
	if table == nil || table.Bh() == nil {
		return 0, nil
	}
	hist, err := table.History(ctx)
	if err != nil {
		return 0, err
	}
	return Height(len(hist) + 1), nil
}

type archive struct {
	u      Universe
	table  BranchTable
	height Height

	claims  map[string]*claim
	content map[string][]byte
}

func (a *archive) lookupClaim(ctx context.Context, id Id) (*claim, error) {
	if id == nil {
		return nil, errors.New("nil id")
	}
	k := id.String()
	if c, ok := a.claims[k]; ok {
		return c, nil
	}
	c, err := loadClaimAs(ctx, a.u, id)
	if err != nil {
		return nil, err
	}
	a.claims[k] = c
	if err := a.loadClaimContent(ctx, c); err != nil {
		delete(a.claims, k)
		return nil, err
	}
	return c, nil
}

func (a *archive) loadClaimContent(ctx context.Context, c *claim) error {
	if c.node.contentHash == nil || c.node.content != nil {
		return nil
	}
	b, err := a.fetchContent(ctx, c.node.contentHash, c.node.size)
	if err != nil {
		return fmt.Errorf("node content %s: %w", c.node.contentHash.String(), err)
	}
	c.node.content = b
	return nil
}

func (a *archive) fetchContent(ctx context.Context, id Id, expected uint64) ([]byte, error) {
	k := id.String()
	if b, ok := a.content[k]; ok {
		return b, nil
	}
	b, err := GetContent(ctx, a.u, id, expected)
	if err != nil {
		return nil, err
	}
	a.content[k] = b
	return b, nil
}

func (a *archive) absorbClaim(ctx context.Context, c *claim) error {
	k := c.node.id.String()
	if c.universe == nil {
		c.universe = a.u
	}
	if _, ok := a.claims[k]; !ok {
		a.claims[k] = c
	}
	if err := PutClaim(ctx, a.u, c); err != nil {
		return err
	}
	if c.node.content != nil && c.node.contentHash != nil {
		if err := a.absorbContent(ctx, c.node.contentHash, c.node.content); err != nil {
			return err
		}
	}
	return nil
}

func (a *archive) absorbContent(ctx context.Context, id Id, b []byte) error {
	k := id.String()
	a.content[k] = b
	return PutContent(ctx, a.u, id, b)
}

func (a *archive) HasClaim(ctx context.Context, id Id) bool {
	if id == nil {
		return false
	}
	_, err := a.lookupClaim(ctx, id)
	return err == nil
}

func (a *archive) GetClaim(ctx context.Context, id Id) (Claim, error) {
	if id == nil {
		return nil, errors.New("ranke.Archive.GetClaim: nil id")
	}
	c, err := a.lookupClaim(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("ranke.Archive.GetClaim: %s: %w", id.String(), err)
	}
	return c, nil
}

// --- Branches ---

func (a *archive) HasBranch(_ context.Context, name string) bool {
	return a.table.Has(name)
}

func (a *archive) GetBranch(ctx context.Context, name string) (Branch, error) {
	return a.table.Get(ctx, name)
}

func (a *archive) Branches(ctx context.Context) []Branch {
	out, err := a.table.List(ctx)
	if err != nil {
		return nil
	}
	return out
}

func (a *archive) VerifyBranch(ctx context.Context, name string) error {
	b, err := a.GetBranch(ctx, name)
	if err != nil {
		return fmt.Errorf("ranke.Archive.VerifyBranch: %w", err)
	}
	c, err := a.GetClaim(ctx, b.Latest().Head())
	if err != nil {
		return fmt.Errorf("ranke.Archive.VerifyBranch: %w", err)
	}
	return c.Validate(ctx)
}

// Prepare verifies a contribution against this snapshot and mints a
// SealedProof for Sequencer.Submit (spec §4.7). It is a read: it never
// advances B_h. Concretely it
//  1. absorbs every claim in g into 𝒰 (idempotent, content-addressed);
//  2. verifies the delta against the branch's prior head with
//     VerifyDelta, trusting the already-committed closure (so committed
//     claims are not re-verified);
//  3. builds the contributor-signed contribution/head that consolidates
//     g's open heads — the head Submit binds the branch to;
//  4. stamps the proof with this snapshot's height and the prior head,
//     so Submit can detect a concurrent branch advance.
//
// The consolidation head is freshly signed here and so trusted by
// construction; only the client's delta passes through VerifyDelta.
func (a *archive) Prepare(ctx context.Context, branchName string, g Graph, contributor Contributor, createdAt ...time.Time) (SealedProof, error) {
	if g == nil {
		return nil, errors.New("ranke.Archive.Prepare: nil graph")
	}
	if contributor == nil {
		return nil, errors.New("ranke.Archive.Prepare: contributor required")
	}
	heads := g.Heads()
	if len(heads) == 0 {
		return nil, errors.New("ranke.Archive.Prepare: graph has no open heads to bind")
	}
	cg, ok := g.(*graph)
	if !ok {
		return nil, errors.New("ranke.Archive.Prepare: graph from foreign implementation")
	}

	// Prior head + trusted (already-committed) closure of the branch.
	var priorHead Id
	var trusted []Id
	if a.table.Has(branchName) {
		b, err := a.table.Get(ctx, branchName)
		if err != nil {
			return nil, fmt.Errorf("ranke.Archive.Prepare: %w", err)
		}
		priorHead = b.Latest().Head()
		trusted, err = a.closureIds(ctx, priorHead)
		if err != nil {
			return nil, fmt.Errorf("ranke.Archive.Prepare: prior closure: %w", err)
		}
	}

	// Absorb the contribution into 𝒰 before verifying — persistence
	// precedes any branch advance (§4.7). Content-addressed puts are
	// idempotent, so this is safe to run concurrently across Prepares.
	for _, c := range cg.claims {
		if err := a.absorbClaim(ctx, c); err != nil {
			return nil, fmt.Errorf("ranke.Archive.Prepare: absorb claim: %w", err)
		}
	}

	// Verify only the delta: prune the walk at the committed closure.
	if err := cg.VerifyDelta(trusted...); err != nil {
		return nil, fmt.Errorf("ranke.Archive.Prepare: %w", err)
	}

	// Consolidate g's open heads under one contribution/head, signed by
	// the client. Freshly built, so it needs no re-verification.
	at := firstNonZero(createdAt)
	headEdges := make([]Edge, 0, len(heads))
	for _, h := range heads {
		e, err := NewEdge(EdgeConfig{
			Reference: h,
			TypeClass: EdgeContribution,
			TypeSub:   "head",
		})
		if err != nil {
			return nil, fmt.Errorf("ranke.Archive.Prepare: build head edge: %w", err)
		}
		headEdges = append(headEdges, e)
	}
	headClaim, err := ClaimBuilder{
		Type:        NodeHead,
		Contributor: contributor,
		Edges:       headEdges,
		CreatedAt:   at,
	}.Sign()
	if err != nil {
		return nil, fmt.Errorf("ranke.Archive.Prepare: build head claim: %w", err)
	}
	headC := headClaim.(*claim)
	if err := a.absorbClaim(ctx, headC); err != nil {
		return nil, fmt.Errorf("ranke.Archive.Prepare: absorb head claim: %w", err)
	}

	return &sealedProof{
		height:           a.height,
		branch:           branchName,
		head:             headC.node.id,
		priorHead:        priorHead,
		containsLimiting: containsLimitingClaim(cg),
		createdAt:        at,
	}, nil
}

// closureIds walks the closure of head over edges and returns every
// claim id in it, loading nodes without their content (edges alone drive
// the walk). It is the trusted anchor set VerifyDelta prunes at: the
// already-committed claims of a branch.
//
// TODO(perf): the Sequencer could maintain the committed-id set per
// branch incrementally, turning this O(closure) walk into O(1).
func (a *archive) closureIds(ctx context.Context, head Id) ([]Id, error) {
	seen := map[string]struct{}{}
	out := make([]Id, 0)
	stack := []Id{head}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		k := id.String()
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, id)
		c, ok := a.claims[k]
		if !ok {
			var err error
			c, err = loadClaimAs(ctx, a.u, id)
			if err != nil {
				return nil, err
			}
		}
		for _, e := range c.edges {
			stack = append(stack, e.reference)
		}
	}
	return out, nil
}

// containsLimitingClaim reports whether g carries a limiting claim — one
// that retroactively narrows validity (early key expiry, revocation),
// the only case that makes verification non-monotonic and would force
// Submit's re-verify path (§Sequencer).
//
// verifyOne enforces no expiry/revocation semantics yet, so nothing is
// validity-non-monotonic today: this returns false and the clean path is
// always taken. When expiry/revocation land in verifyOne, extend this to
// detect their claim types and to yield the affected-slice lookup the
// dirty path needs (contributor/pubkey → affected incoming claims).
func containsLimitingClaim(_ *graph) bool { return false }
