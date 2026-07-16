// package: ranke / archive
// type:    logic
// job:     an immutable Ranke-Archive snapshot RA_k = (𝒰, k); reads claims and branches through the head claim, delegating closure ops to the Universe
// limits:  advances nothing — writes go through the Sequencer (-> sequencer); closure traversal belongs to the Universe (-> universe)
package ranke

import (
	"context"
	"io"
	"sort"
	"sync"
)

// Archive is an immutable read snapshot of a Ranke-Archive: the tuple
// RA_k = (𝒰, k), held as the Universe plus the head claim 𝒰(k). Reads
// resolve against the head's closure — membership and lookup are
// delegated to the Universe (engine-dependent). It advances nothing; a
// contribution is the Sequencer's job.
//
// The head claim 𝒰(k) plays the paper's *branch table* role (spec
// §Branches): its named contribution/branch edges are the archive's
// branches, and it may be a contribution/diff over previous tables. The
// Archive implements that branch-table concept — materialising the diff
// chain and exposing each named edge as a Branch. No other type needs it.
type Archive interface {
	HasClaim(ctx context.Context, id Id) (bool, error)
	GetClaim(ctx context.Context, id Id) (Claim, error)
	GetClaimContent(ctx context.Context, id Id) (io.Reader, error)

	HasBranch(ctx context.Context, name string) (bool, error)
	GetBranch(ctx context.Context, name string) (Branch, error)
	GetBranches(ctx context.Context) ([]Branch, error)

	Verify(ctx context.Context, opts ...VerifyOption) (VerificationRun, error)
}

type archive struct {
	u   Universe
	bth Claim // the head claim 𝒰(k) — a contribution/branches claim

	// The archive is an immutable snapshot, so the branch set is stable —
	// read it off the (already-materialised) head claim once and memoise it
	// (safe for concurrent readers). Each entry is the naming
	// contribution/branch edge, keyed by branch name.
	branchOnce sync.Once
	branches   map[string]Edge
}

// loadBranches reads the branch set off the head claim once and caches it.
// The head is materialised at open (NewArchive), so its contribution/branch
// edges are already the merged set — no diff walk here.
func (a *archive) loadBranches() map[string]Edge {
	a.branchOnce.Do(func() {
		entries := map[string]Edge{}
		for _, e := range a.bth.Edges(EdgeFilterType{Type: EdgeTypeBranch}) {
			name, _ := e.GetField(FieldName)
			entries[name] = e
		}
		a.branches = entries
	})
	return a.branches
}

// NewArchive opens the immutable snapshot RA_k = (𝒰, k) by loading the
// head claim at k from u.
func NewArchive(ctx context.Context, u Universe, k Id) (Archive, error) {
	if u == nil {
		return nil, errNilUniverse
	}
	if k == nil {
		return nil, errNilHeadID
	}
	// GetClaim materialises the head's diff chain, so a.bth's
	// contribution/branch edges are already the merged branch set.
	c, err := GetClaim(ctx, u, k)
	if err != nil {
		return nil, wrapDetail(errArchiveLoadHead, k.String(), err)
	}
	return &archive{u: u, bth: c}, nil
}

func (a *archive) HasClaim(ctx context.Context, id Id) (bool, error) {
	if id == nil {
		return false, errNilID
	}
	return InClosure(ctx, a.u, BranchArchive, []Id{a.bth.ID()}, id)
}

func (a *archive) GetClaim(ctx context.Context, id Id) (Claim, error) {
	if id == nil {
		return nil, errNilID
	}
	// The Universe returns materialised claims (see GetClaims); the archive
	// does not materialise itself.
	return GetFromClosure(ctx, a.u, BranchArchive, []Id{a.bth.ID()}, id)
}

func (a *archive) GetClaimContent(ctx context.Context, id Id) (io.Reader, error) {
	c, err := a.GetClaim(ctx, id) // scope-checked; then read from the claim in hand
	if err != nil {
		return nil, err
	}
	return c.GetContent(ctx, a.u)
}

// --- Branches ---
//
// A branch table is a claim whose contribution/branch edges each carry a
// branch name (the `name` field) and reference that branch's subgraph root.
// A table may be a contribution/diff over previous tables, restating only
// changed entries. The diff overlay is done by the generic claim
// materialisation (name-keyed: inherit / overwrite-by-name / omit via
// edges_diff_omit) at load time, so the head claim's contribution/branch
// edges are already the full merged set — the Archive just reads them and
// wraps each as a Branch.

func (a *archive) HasBranch(_ context.Context, name string) (bool, error) {
	_, ok := a.loadBranches()[name]
	return ok, nil
}

func (a *archive) GetBranch(_ context.Context, name string) (Branch, error) {
	e, ok := a.loadBranches()[name]
	if !ok {
		return nil, errBranchNotFound
	}
	return newBranch(a.u, e), nil
}

func (a *archive) GetBranches(_ context.Context) ([]Branch, error) {
	entries := a.loadBranches()
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Branch, 0, len(names))
	for _, n := range names {
		out = append(out, newBranch(a.u, entries[n]))
	}
	return out, nil
}

// Verify lives in verify.go.
