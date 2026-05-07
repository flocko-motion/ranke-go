package ranke

import "time"

// branch / branchEntry are projections of contribution/branch claims.
// All interface methods derive their answers from the underlying
// claim (and chain, for Branch), so the values are self-contained
// and survive Store reload.

// --- Branch ---

func (b *branch) Name() string {
	return string(b.claim.node.content)
}

func (b *branch) Latest() BranchEntry {
	return &branchEntry{claim: b.claim}
}

func (b *branch) Provenance() []BranchEntry {
	out := make([]BranchEntry, len(b.chain))
	for i, e := range b.chain {
		out[i] = e
	}
	return out
}

// --- BranchEntry ---

func (e *branchEntry) Head() Id {
	// The contribution/head edge points at the bound head; the
	// contribution/branch edge (if any) points at the prior branch
	// claim. Walk the claim's edges and pick the head one.
	for _, edge := range e.claim.edges {
		if edge.typeClass == EdgeContribution && edge.typeSub == "head" {
			return edge.reference
		}
	}
	return nil
}

func (e *branchEntry) Time() time.Time {
	return e.claim.node.createdAt
}

func (e *branchEntry) Contributor() Contributor {
	if e.claim.contributor == nil {
		return nil
	}
	return e.claim.contributor
}

func (e *branchEntry) Claim() Claim {
	return e.claim
}
