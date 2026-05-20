package ranke

import "time"

// branch / branchEntry are projections of one (name, head) binding
// in a contribution/branches claim. All values are derived from the
// underlying table claim, so they're self-contained and survive
// Archive reload.

type branch struct {
	name  string
	head  Id             // contribution/head claim this branch points at
	table *claim         // contribution/branches claim that holds this binding
	chain []*branchEntry // historical entries from prior tables, most-recent first
}

type branchEntry struct {
	name  string
	head  Id
	table *claim
}

// --- Branch ---

func (b *branch) Name() string { return b.name }

func (b *branch) Latest() BranchEntry {
	return &branchEntry{name: b.name, head: b.head, table: b.table}
}

func (b *branch) Provenance() []BranchEntry {
	out := make([]BranchEntry, len(b.chain))
	for i, e := range b.chain {
		out[i] = e
	}
	return out
}

// --- BranchEntry ---

func (e *branchEntry) Head() Id { return e.head }

func (e *branchEntry) Time() time.Time { return e.table.node.createdAt }

func (e *branchEntry) Contributor() Contributor {
	if e.table.contributor == nil {
		return nil
	}
	return e.table.contributor
}

func (e *branchEntry) Claim() Claim { return e.table }
