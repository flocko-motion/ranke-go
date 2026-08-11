// package: ranke / archive
// type:    logic
// job:     an immutable Ranke-Archive snapshot RA_k = (𝒰, k); reads claims and branches through the
// head claim, delegating closure ops to the Universe
// limits:  advances nothing — writes go through the Sequencer (-> sequencer); closure traversal
// belongs to the Universe (-> universe)
package ranke

import (
	"context"
	"io"
	"slices"
	"sort"
	"strconv"
	"sync"
)

// Archive is an immutable read snapshot RA_k = (𝒰, k): the Universe plus the
// head claim 𝒰(k), which plays the paper's branch-table role (spec §Branches).
type Archive interface {
	// Head is k, the half of the tuple that fixes which snapshot this is.
	Head() Id

	HasClaim(ctx context.Context, id Id) (bool, error)
	GetClaim(ctx context.Context, id Id) (Claim, error)
	GetClaimContent(ctx context.Context, id Id) (io.Reader, error)

	HasBranch(ctx context.Context, name string) (bool, error)
	GetBranch(ctx context.Context, name string) (Branch, error)
	GetBranches(ctx context.Context) ([]Branch, error)
	// MissingBranches are those of names this archive does not carry — the branches a
	// contribution would create, so a caller knows whether it needs the right to.
	MissingBranches(ctx context.Context, names []string) ([]string, error)

	// Query answers an RQL read: resolve q.Select.Branch to a Scope, delegate to 𝒰.
	Query(ctx context.Context, q Query) (ResultStream, error)

	Verify(ctx context.Context, opts ...VerifyOption) (VerificationRun, error)
}

type archive struct {
	u   Universe
	bth Claim // the head claim 𝒰(k) — a contribution/branches claim

	// An immutable snapshot has a stable branch set, so memoise it: the naming
	// contribution/branch edge per branch name, safe for concurrent readers.
	branchOnce sync.Once
	branches   map[string]Edge
}

// loadBranches caches the branch set off the head claim, materialised at open.
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

// NewArchive opens the snapshot RA_k = (𝒰, k) by loading the head claim at k.
func NewArchive(ctx context.Context, u Universe, k Id) (Archive, error) {
	if u == nil {
		return nil, errNilUniverse
	}
	if k == nil {
		return nil, errNilHeadID
	}
	// GetClaim materialises the diff chain, so a.bth's branch edges are merged.
	c, err := GetClaim(ctx, u, k)
	if err != nil {
		return nil, WrapDetail(errArchiveLoadHead, k.String(), err)
	}
	return &archive{u: u, bth: c}, nil
}

// Head is k, the head claim's id.
func (a *archive) Head() Id { return a.bth.ID() }

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
	// The Universe returns materialised claims (see GetClaims).
	return GetFromClosure(ctx, a.u, BranchArchive, []Id{a.bth.ID()}, id)
}

func (a *archive) GetClaimContent(ctx context.Context, id Id) (io.Reader, error) {
	c, err := a.GetClaim(ctx, id) // scope-checked; then read from the claim in hand
	if err != nil {
		return nil, err
	}
	return c.GetContent(ctx, a.u)
}

// Query resolves the branch scope and delegates to 𝒰.
func (a *archive) Query(ctx context.Context, q Query) (ResultStream, error) {
	// Every read passes ValidateQuery, so a Go-built query is held to what a wire one
	// is — one verdict, whichever way the query arrived.
	if err := ValidateQuery(q); err != nil {
		return nil, err
	}
	if err := validateSelect(q); err != nil {
		return nil, err
	}
	scope, err := a.resolveScope(ctx, q)
	if err != nil {
		return nil, err
	}
	return a.u.Query(ctx, q, scope)
}

// resolveScope maps Branch to its Scope. Height is always the branch-table
// height, so tag <= Height filters membership and point-in-time in one compare.
func (a *archive) resolveScope(ctx context.Context, q Query) (Scope, error) {
	if q.Select.Branch == "" {
		return Scope{}, ErrQueryNoScope
	}
	if q.Select.Branch == BranchUniverse {
		return Scope{Branch: BranchUniverse}, nil
	}
	scope := Scope{Branch: q.Select.Branch, Height: a.bth.Node().Height(), Revision: revisionOf(a.bth)}
	if q.Select.Branch == BranchArchive {
		scope.Head = a.bth.ID()
		return scope, nil
	}
	br, err := a.GetBranch(ctx, q.Select.Branch)
	if err != nil {
		return Scope{}, err
	}
	scope.Head = br.Head()
	return scope, nil
}

// validateSelect holds a read to what only a read can decide: a scan reaches claims
// by no stated route. Select.Claim is meaningful without a path — it anchors the
// frontier the closure is taken from (`R-QANCHOR`). The rest of a query's shape is
// ValidateQuery's, which Query calls first, so neither states a rule twice.
func validateSelect(q Query) error {
	if len(q.Select.Path) == 0 && q.Output.Shape == ShapePath {
		return ErrQueryScanShape
	}
	return nil
}

// revisionOf reads the spine revision
func revisionOf(c Claim) int {
	if v := c.Tag(SpineRevKey); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

// --- Branches ---
//
// A branch table's contribution/branch edges each carry a branch name and that
// branch's subgraph root; materialisation merges any diff overlay at load time.

func (a *archive) HasBranch(_ context.Context, name string) (bool, error) {
	_, ok := a.loadBranches()[name]
	return ok, nil
}

func (a *archive) GetBranch(_ context.Context, name string) (Branch, error) {
	e, ok := a.loadBranches()[name]
	if !ok {
		// Also ErrNotFound, so a caller classifies the absence from the error it holds
		// rather than asking the store a second time.
		return nil, alsoMatches(ErrBranchNotFound, ErrNotFound)
	}
	return newBranch(a.u, e), nil
}

func (a *archive) MissingBranches(_ context.Context, names []string) ([]string, error) {
	entries := a.loadBranches()
	var missing []string
	for _, n := range names {
		if _, ok := entries[n]; !ok && !slices.Contains(missing, n) {
			missing = append(missing, n)
		}
	}
	return missing, nil
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
