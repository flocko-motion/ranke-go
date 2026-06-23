// package: ranke / branch
// type:    data
// job:     read-only views over one (name, head) binding and its history, projected from a contribution/branches claim
// limits:  does not mutate branches (-> archive); does not load the table (-> branch_table)
package ranke

import "time"

// Branch is a convenience view over a contribution/branch claim.
// Mutation goes through Archive.AddGraph / AddClaim.
type Branch interface {
	Name() string
	// Latest is the current binding, exposed as a BranchEntry so the
	// accessors are the same as for prior bindings.
	Latest() BranchEntry
	// Provenance returns previously-bound entries (most-recent first),
	// walking the contribution/branch edges back through prior branch
	// claims. Latest is not included.
	Provenance() []BranchEntry
}

// BranchEntry is one entry in a branch's history — the head it was
// bound to, the time of that binding, and the contributor that made
// it.
type BranchEntry interface {
	Head() Id
	Time() time.Time
	Contributor() Contributor
	Claim() Claim
}

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
