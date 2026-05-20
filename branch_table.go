package ranke

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// LoadBranchTable loads the contribution/branches claim at bh from u
// and returns it as a BranchTable. nil bh → empty table.
func LoadBranchTable(ctx context.Context, u Universe, bh Id) (BranchTable, error) {
	if u == nil {
		return nil, errors.New("ranke.LoadBranchTable: nil Universe")
	}
	bt := &branchTable{u: u}
	if bh == nil {
		return bt, nil
	}
	c, err := loadClaimAs(ctx, u, bh)
	if err != nil {
		return nil, fmt.Errorf("ranke.LoadBranchTable: %w", err)
	}
	bt.bh = bh
	bt.claim = c
	return bt, nil
}

// loadClaimAs is the shared "load + type-assert + wire contributor"
// used by archive.lookupClaim and branchTable's traversals. Every
// returned claim has .contributor populated — either self (root) or
// the recursively-wired claim referenced by its contribution/contributor
// edge.
func loadClaimAs(ctx context.Context, u Universe, id Id) (*claim, error) {
	cl, err := u.LoadClaim(ctx, id)
	if err != nil {
		return nil, err
	}
	c, ok := cl.(*claim)
	if !ok {
		return nil, errors.New("foreign Claim returned by Universe")
	}
	c.universe = u
	if err := wireContributor(ctx, u, c); err != nil {
		return nil, err
	}
	return c, nil
}

// wireContributor sets c.contributor by following its contribution/contributor
// edge (or self-attributing for the root claim). Idempotent: no-op
// when c.contributor is already set.
func wireContributor(ctx context.Context, u Universe, c *claim) error {
	if c.contributor != nil {
		return nil
	}
	if len(c.edges) == 0 {
		c.contributor = c
		return nil
	}
	for _, e := range c.edges {
		if e.typeClass == EdgeContribution && e.typeSub == "contributor" {
			cc, err := loadClaimAs(ctx, u, e.reference)
			if err != nil {
				return fmt.Errorf("wire contributor: %w", err)
			}
			c.contributor = cc
			return nil
		}
	}
	return errors.New("non-root claim missing contribution/contributor edge")
}

type branchTable struct {
	u     Universe
	bh    Id     // nil ⇒ empty table
	claim *claim // nil ⇒ empty table
}

func (b *branchTable) Bh() Id { return b.bh }

func (b *branchTable) Has(name string) bool {
	if b.claim == nil {
		return false
	}
	return b.findBranchEdge(name) != nil
}

func (b *branchTable) findBranchEdge(name string) *edge {
	for _, e := range b.claim.edges {
		if e.typeClass == EdgeContribution && e.typeSub == "branch" && string(e.content) == name {
			return e
		}
	}
	return nil
}

func (b *branchTable) Get(ctx context.Context, name string) (Branch, error) {
	if b.claim == nil {
		return nil, fmt.Errorf("ranke.BranchTable.Get: branch %q not found (empty table)", name)
	}
	e := b.findBranchEdge(name)
	if e == nil {
		return nil, fmt.Errorf("ranke.BranchTable.Get: branch %q not found", name)
	}
	return b.buildBranchView(ctx, e)
}

func (b *branchTable) List(ctx context.Context) ([]Branch, error) {
	if b.claim == nil {
		return nil, nil
	}
	var out []Branch
	for _, e := range b.claim.edges {
		if e.typeClass != EdgeContribution || e.typeSub != "branch" {
			continue
		}
		bv, err := b.buildBranchView(ctx, e)
		if err != nil {
			return nil, err
		}
		out = append(out, bv)
	}
	return out, nil
}

func (b *branchTable) History(ctx context.Context) ([]BranchTable, error) {
	if b.claim == nil {
		return nil, nil
	}
	var out []BranchTable
	cur := b
	for {
		prevId := cur.prevTableId()
		if prevId == nil {
			break
		}
		c, err := loadClaimAs(ctx, b.u, prevId)
		if err != nil {
			return nil, fmt.Errorf("ranke.BranchTable.History: %w", err)
		}
		prev := &branchTable{u: b.u, bh: prevId, claim: c}
		out = append(out, prev)
		cur = prev
	}
	return out, nil
}

func (b *branchTable) prevTableId() Id {
	if b.claim == nil {
		return nil
	}
	for _, e := range b.claim.edges {
		if e.typeClass == EdgeContribution && e.typeSub == "branches" {
			return e.reference
		}
	}
	return nil
}

func (b *branchTable) Set(ctx context.Context, name string, head Id, contributor Contributor, createdAt time.Time) (BranchTable, error) {
	if name == "" {
		return nil, errors.New("ranke.BranchTable.Set: empty branch name")
	}
	if head == nil {
		return nil, errors.New("ranke.BranchTable.Set: nil head id")
	}
	if contributor == nil {
		return nil, errors.New("ranke.BranchTable.Set: nil contributor")
	}

	edges := make([]Edge, 0)
	if b.claim != nil {
		for _, e := range b.claim.edges {
			if e.typeClass == EdgeContribution && e.typeSub == "branch" {
				if string(e.content) == name {
					continue // updating this one
				}
				copyE, err := NewEdge(EdgeConfig{
					Reference: e.reference,
					TypeClass: EdgeContribution,
					TypeSub:   "branch",
					Content:   e.content,
				})
				if err != nil {
					return nil, fmt.Errorf("ranke.BranchTable.Set: copy edge: %w", err)
				}
				edges = append(edges, copyE)
			}
		}
	}
	newE, err := NewEdge(EdgeConfig{
		Reference: head,
		TypeClass: EdgeContribution,
		TypeSub:   "branch",
		Content:   []byte(name),
	})
	if err != nil {
		return nil, fmt.Errorf("ranke.BranchTable.Set: new branch edge: %w", err)
	}
	edges = append(edges, newE)
	if b.bh != nil {
		chain, err := NewEdge(EdgeConfig{
			Reference: b.bh,
			TypeClass: EdgeContribution,
			TypeSub:   "branches",
		})
		if err != nil {
			return nil, fmt.Errorf("ranke.BranchTable.Set: chain edge: %w", err)
		}
		edges = append(edges, chain)
	}

	tableClaim, err := ClaimBuilder{
		Type:        NodeBranches,
		Contributor: contributor,
		Edges:       edges,
		CreatedAt:   createdAt,
	}.Sign()
	if err != nil {
		return nil, fmt.Errorf("ranke.BranchTable.Set: build claim: %w", err)
	}
	tc := tableClaim.(*claim)
	if err := b.u.SaveClaim(ctx, tc); err != nil {
		return nil, fmt.Errorf("ranke.BranchTable.Set: save claim: %w", err)
	}
	return &branchTable{u: b.u, bh: tc.node.id, claim: tc}, nil
}

// buildBranchView projects one (name, head) edge into a Branch with
// its provenance pre-walked through prior tables.
func (b *branchTable) buildBranchView(ctx context.Context, branchEdge *edge) (Branch, error) {
	name := string(branchEdge.content)
	head := branchEdge.reference

	chain := make([]*branchEntry, 0)
	cur := b.claim
	for {
		var prev *claim
		for _, e := range cur.edges {
			if e.typeClass == EdgeContribution && e.typeSub == "branches" {
				p, err := loadClaimAs(ctx, b.u, e.reference)
				if err != nil {
					return nil, fmt.Errorf("provenance table %s: %w", e.reference.String(), err)
				}
				prev = p
				break
			}
		}
		if prev == nil {
			break
		}
		for _, e := range prev.edges {
			if e.typeClass == EdgeContribution && e.typeSub == "branch" && string(e.content) == name {
				chain = append(chain, &branchEntry{
					name:  name,
					head:  e.reference,
					table: prev,
				})
				break
			}
		}
		cur = prev
	}
	return &branch{
		name:  name,
		head:  head,
		table: b.claim,
		chain: chain,
	}, nil
}
