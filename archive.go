package ranke

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// NewArchive composes a Universe (𝒰) with a BranchTableHead (B_h)
// into a Ranke-Archive (spec §4.8). The Archive holds no resources
// of its own — closing it does not close u or bth; the caller
// manages their lifetimes, which is what lets multiple Archives
// share one Universe.
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
	return &archive{
		u:       u,
		bth:     bth,
		table:   table,
		claims:  make(map[string]*claim),
		content: make(map[string][]byte),
	}, nil
}

type archive struct {
	u     Universe
	bth   BranchTableHead
	table BranchTable

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
	b, err := a.u.GetContent(ctx, id, expected)
	if err != nil {
		return nil, err
	}
	a.content[k] = b
	return b, nil
}

func (a *archive) absorbClaim(ctx context.Context, c *claim) error {
	k := c.node.id.String()
	if _, ok := a.claims[k]; !ok {
		a.claims[k] = c
	}
	if err := a.u.SaveClaim(ctx, c); err != nil {
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
	return a.u.SaveContent(ctx, id, b)
}

func (a *archive) HasClaim(ctx context.Context, id Id) bool {
	if id == nil {
		return false
	}
	_, err := a.lookupClaim(ctx, id)
	return err == nil
}

func (a *archive) GetClaim(ctx context.Context, head Id) (Graph, error) {
	if head == nil {
		return nil, errors.New("ranke.Archive.GetClaim: nil head")
	}
	root, err := a.lookupClaim(ctx, head)
	if err != nil {
		return nil, fmt.Errorf("ranke.Archive.GetClaim: head %s: %w", head.String(), err)
	}
	g := &graph{
		claims:     make(map[string]*claim),
		referenced: make(map[string]struct{}),
	}
	queue := []*claim{root}
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		c := queue[0]
		queue = queue[1:]
		k := c.node.id.String()
		if _, seen := g.claims[k]; seen {
			continue
		}
		g.claims[k] = c
		for _, e := range c.edges {
			refKey := e.reference.String()
			g.referenced[refKey] = struct{}{}
			next, err := a.lookupClaim(ctx, e.reference)
			if err != nil {
				return nil, fmt.Errorf("ranke.Archive.GetClaim: missing claim %s referenced by %s: %w", refKey, k, err)
			}
			queue = append(queue, next)
		}
	}
	return g, nil
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
	g, err := a.GetClaim(ctx, b.Latest().Head())
	if err != nil {
		return fmt.Errorf("ranke.Archive.VerifyBranch: GetClaim %s: %w", b.Latest().Head().String(), err)
	}
	return g.Validate()
}

func (a *archive) AddClaim(ctx context.Context, name string, c Claim, createdAt ...time.Time) error {
	if c == nil {
		return errors.New("ranke.Archive.AddClaim: nil claim")
	}
	contributor := c.Contributor()
	if contributor == nil {
		return errors.New("ranke.Archive.AddClaim: claim has no contributor")
	}
	var g Graph
	if a.table.Has(name) {
		b, err := a.table.Get(ctx, name)
		if err != nil {
			return fmt.Errorf("ranke.Archive.AddClaim: %w", err)
		}
		g, err = a.GetClaim(ctx, b.Latest().Head())
		if err != nil {
			return fmt.Errorf("ranke.Archive.AddClaim: load branch state: %w", err)
		}
	} else {
		g = NewGraph(contributor)
	}
	if err := g.Add(c); err != nil {
		return fmt.Errorf("ranke.Archive.AddClaim: %w", err)
	}
	return a.AddGraph(ctx, name, g, contributor, createdAt...)
}

// AddGraph advances the named branch per spec §4.7:
//  1. Absorb every claim in g into U (atomic-creation rule).
//  2. Build a contribution/head claim consolidating g's open heads.
//  3. Delegate the branches-claim mint to a.table.Set.
//  4. Persist the new B_h via a.bth.
func (a *archive) AddGraph(ctx context.Context, name string, g Graph, contributor Contributor, createdAt ...time.Time) error {
	if g == nil {
		return errors.New("ranke.Archive.AddGraph: nil graph")
	}
	if contributor == nil {
		return errors.New("ranke.Archive.AddGraph: contributor required")
	}
	if len(g.Heads()) == 0 {
		return errors.New("ranke.Archive.AddGraph: graph has no open heads to bind")
	}
	cg, ok := g.(*graph)
	if !ok {
		return errors.New("ranke.Archive.AddGraph: graph from foreign implementation")
	}

	for _, c := range cg.claims {
		if err := a.absorbClaim(ctx, c); err != nil {
			return fmt.Errorf("ranke.Archive.AddGraph: absorb claim: %w", err)
		}
	}

	headEdges := make([]Edge, 0, len(g.Heads()))
	for _, h := range g.Heads() {
		e, err := NewEdge(EdgeConfig{
			Reference: h,
			TypeClass: EdgeContribution,
			TypeSub:   "head",
		})
		if err != nil {
			return fmt.Errorf("ranke.Archive.AddGraph: build head edge: %w", err)
		}
		headEdges = append(headEdges, e)
	}
	at := firstNonZero(createdAt)
	headClaim, err := ClaimBuilder{
		Type:        NodeHead,
		Contributor: contributor,
		Edges:       headEdges,
		CreatedAt:   at,
	}.Sign()
	if err != nil {
		return fmt.Errorf("ranke.Archive.AddGraph: build head claim: %w", err)
	}
	headC := headClaim.(*claim)
	if err := a.absorbClaim(ctx, headC); err != nil {
		return fmt.Errorf("ranke.Archive.AddGraph: absorb head claim: %w", err)
	}

	nextTable, err := a.table.Set(ctx, name, headC.node.id, contributor, at)
	if err != nil {
		return fmt.Errorf("ranke.Archive.AddGraph: %w", err)
	}
	if err := a.bth.Save(ctx, nextTable.Bh()); err != nil {
		return fmt.Errorf("ranke.Archive.AddGraph: persist B_h: %w", err)
	}
	a.table = nextTable
	return nil
}
