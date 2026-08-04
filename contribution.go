// package: ranke / contribution
// type:    logic
// job:     the contribution contract — the staged → verified → mergable advance of a Ranke-Archive
// (RA_k → RA_k')
// limits:  the contract plus the shared wire drain; opening, verifying and merging a contribution
// are a Sequencer's (-> adapter/sequencer/dev, adapter/sequencer/concurrent)
package ranke

import (
	"context"
	"slices"
	"time"
)

// Constraints are what a contribution may do: the caller declares them and the
// Sequencer guarantees them, so an access system is built by composing them.
type Constraints struct {
	lifted       map[string]bool // reserved node types this contribution may carry
	referencable []string        // branches it may reference claims from
}

// ContributionOption sets one constraint.
type ContributionOption func(*Constraints)

// NewConstraints applies opts. The zero value is the strictest reading: every
// reserved type refused.
func NewConstraints(opts ...ContributionOption) Constraints {
	var c Constraints
	for _, opt := range opts {
		opt(&c)
	}
	return c
}

// WithLiftedTypes admits node types otherwise reserved to the Sequencer, so a caller
// holding it can create what it would mint.
func WithLiftedTypes(types ...string) ContributionOption {
	return func(c *Constraints) {
		if c.lifted == nil {
			c.lifted = make(map[string]bool, len(types))
		}
		for _, t := range types {
			c.lifted[t] = true
		}
	}
}

// WithReferencableBranches names the branches a contribution may reference claims
// from (step 3). BranchArchive admits every branch, BranchUniverse the whole 𝒰.
func WithReferencableBranches(branches ...string) ContributionOption {
	return func(c *Constraints) { c.referencable = append(c.referencable, branches...) }
}

// AdmitReferences enforces step 3's read requirement: every claim the contribution's
// own reference, other than each other, must lie in a scope it may read.
func (c Constraints) AdmitReferences(ctx context.Context, u Universe, base Archive, branch string, own []Claim) error {
	if c.unconfined() {
		return nil
	}
	mine := make(map[string]bool, len(own))
	for _, cl := range own {
		mine[cl.ID().String()] = true
	}
	var refs []Id
	outside := map[string]bool{}
	for _, cl := range own {
		for _, e := range cl.Edges() {
			k := e.Reference().String()
			if mine[k] || outside[k] {
				continue
			}
			outside[k] = true
			refs = append(refs, e.Reference())
		}
	}
	if len(refs) == 0 {
		return nil
	}

	allowed, err := c.allowedHeads(ctx, base, branch)
	if err != nil {
		return err
	}
	held, err := u.ClaimsInBranches(ctx, allowed, refs)
	if err != nil {
		return Wrap(errContributionRefs, err)
	}
	for i, ok := range held {
		if !ok {
			return WithDetail(ErrUnreadableReference, refs[i].String())
		}
	}
	return nil
}

// allowedHeads resolves each readable scope to the head whose closure it is, the
// branch being written included: a claim already on it needs no read grant.
func (c Constraints) allowedHeads(ctx context.Context, base Archive, branch string) (map[string]Id, error) {
	names := append([]string{branch}, c.referencable...)
	// The archive is every branch's superset, so it answers for all of them at once.
	if slices.Contains(names, BranchArchive) {
		return map[string]Id{BranchArchive: base.Head()}, nil
	}
	heads := make(map[string]Id, len(names))
	for _, name := range names {
		b, err := base.GetBranch(ctx, name)
		if err != nil {
			continue
		}
		heads[name] = b.Head()
	}
	return heads, nil
}

// unconfined reports BranchUniverse among the referencable branches, where every
// claim in 𝒰 is readable.
func (c Constraints) unconfined() bool {
	return slices.Contains(c.referencable, BranchUniverse)
}

// reservedTypes are the Sequencer's alone (paper 2 §Sequencer step 2): the branch
// table it mints, and the limiting claims that restrict another claim.
var reservedTypes = map[string]bool{
	NodeBranches: true,
	NodeDelete:   true,
	NodeExpiry:   true,
}

// AdmitType reports whether a claim of nodeType may be added under these
// constraints.
func (c Constraints) AdmitType(nodeType string) error {
	if reservedTypes[nodeType] && !c.lifted[nodeType] {
		return WithDetail(ErrReservedType, nodeType)
	}
	return nil
}

// Contribution is an in-progress advance of a Ranke-Archive: open it against a
// base RA_k, fill it, then CompleteAndVerify seals and verifies it.
type Contribution interface {
	// Base is the (k, t) the contribution was opened against.
	Base() (head Id, t time.Time)
	// AddClaims fills the contribution (step 2), naming the branch the claims join.
	// Several may be named, and an empty one is an error. Passing the claims is what
	// lets step 2's rules reach every one of them.
	AddClaims(branch string, claims []Claim) error
	// AddWire fills from a WireMediaType stream, which declares its own branches.
	// It takes the reader, so a caller that checked Branches hands on the same one.
	AddWire(ctx context.Context, wr *WireReader) error
	// CompleteAndVerify closes the contribution over its base and verifies it
	// (steps 3–4), yielding a sealed VerifiedContribution.
	CompleteAndVerify(ctx context.Context) (VerifiedContribution, error)
}

// DrainWire fills c as records arrive: content lands in u under its hash, each
// claim stages under its branch, and an undeclared branch stops the fill.
func DrainWire(ctx context.Context, c Contribution, u Universe, wr *WireReader) error {
	for wr.Next() {
		switch rec := wr.Record(); rec.Kind {
		case WireContent:
			if err := u.PutContents(ctx, []ContentBlob{rec.Blob}); err != nil {
				return err
			}
		case WireClaim:
			if err := c.AddClaims(rec.Branch, []Claim{rec.Claim}); err != nil {
				return err
			}
		}
	}
	return wr.Err()
}

// VerifiedContribution is a sealed, verified contribution: its contents
// are fixed, so by immutability whatever verified stays valid.
type VerifiedContribution interface {
	// Ids are the claim ids the contribution adds.
	Ids() []Id
	// Persist writes the sealed closure to 𝒰 (step 5), yielding a
	// MergableContribution the Sequencer can merge.
	Persist(ctx context.Context) (MergableContribution, error)
}

// MergableContribution is ready for step 6: its claims are durably in 𝒰 and its
// head claim(s) are known.
type MergableContribution interface {
	// Heads are the open head ids per branch, which the new branch-table claim
	// references under those names.
	Heads() map[string][]Id
}
