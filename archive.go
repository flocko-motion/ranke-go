package ranke

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// NewArchive composes a Universe (𝒰) with a BranchTableHead (B_h)
// into a Ranke-Archive (spec §4.8). The Archive holds no resources
// of its own — closing it does not close u or bth; the caller manages
// their lifetimes, which is what lets multiple Archives share one
// Universe.
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
	return &archive{
		u:            u,
		bth:          bth,
		branchesHead: bh,
		claims:       make(map[string]*claim),
		content:      make(map[string][]byte),
	}, nil
}

type archive struct {
	u            Universe
	bth          BranchTableHead
	branchesHead Id

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
	cl, err := a.u.LoadClaim(ctx, id)
	if err != nil {
		return nil, err
	}
	c, ok := cl.(*claim)
	if !ok {
		return nil, errors.New("foreign Claim returned by Universe")
	}
	a.claims[k] = c
	if err := a.loadClaimContent(ctx, c); err != nil {
		delete(a.claims, k)
		return nil, err
	}
	if err := a.wireContributor(ctx, c); err != nil {
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

func (a *archive) wireContributor(ctx context.Context, c *claim) error {
	if len(c.edges) == 0 {
		c.contributor = c
		return nil
	}
	for _, e := range c.edges {
		if e.typeClass == EdgeContribution && e.typeSub == "contributor" {
			cc, err := a.lookupClaim(ctx, e.reference)
			if err != nil {
				return fmt.Errorf("wire contributor: %w", err)
			}
			c.contributor = cc
			return nil
		}
	}
	return errors.New("non-root claim missing contribution/contributor edge")
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

func (a *archive) HasGraph(ctx context.Context, head Id) bool {
	if head == nil {
		return false
	}
	_, err := a.lookupClaim(ctx, head)
	return err == nil
}

func (a *archive) GetGraph(ctx context.Context, head Id) (Graph, error) {
	if head == nil {
		return nil, errors.New("ranke.Archive.GetGraph: nil head")
	}
	root, err := a.lookupClaim(ctx, head)
	if err != nil {
		return nil, fmt.Errorf("ranke.Archive.GetGraph: head %s: %w", head.String(), err)
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
				return nil, fmt.Errorf("ranke.Archive.GetGraph: missing claim %s referenced by %s: %w", refKey, k, err)
			}
			queue = append(queue, next)
		}
	}
	return g, nil
}

func (a *archive) resolveBranchesTable(ctx context.Context) (*claim, error) {
	if a.branchesHead == nil {
		return nil, nil
	}
	return a.lookupClaim(ctx, a.branchesHead)
}

func (a *archive) findBranchEdge(ctx context.Context, name string) (*claim, *edge, error) {
	table, err := a.resolveBranchesTable(ctx)
	if err != nil || table == nil {
		return nil, nil, err
	}
	for _, e := range table.edges {
		if e.typeClass == EdgeContribution && e.typeSub == "branch" && string(e.content) == name {
			return table, e, nil
		}
	}
	return table, nil, nil
}

func (a *archive) HasBranch(ctx context.Context, name string) bool {
	_, e, err := a.findBranchEdge(ctx, name)
	return err == nil && e != nil
}

func (a *archive) GetBranch(ctx context.Context, name string) (Branch, error) {
	table, e, err := a.findBranchEdge(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("ranke.Archive.GetBranch: %w", err)
	}
	if e == nil {
		return nil, fmt.Errorf("ranke.Archive.GetBranch: branch %q not found", name)
	}
	return a.buildBranchView(ctx, table, e)
}

func (a *archive) buildBranchView(ctx context.Context, table *claim, branchEdge *edge) (Branch, error) {
	name := string(branchEdge.content)
	head := branchEdge.reference

	chain := make([]*branchEntry, 0)
	cur := table
	for {
		var prev *claim
		for _, e := range cur.edges {
			if e.typeClass == EdgeContribution && e.typeSub == "branches" {
				p, err := a.lookupClaim(ctx, e.reference)
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
		table: table,
		chain: chain,
	}, nil
}

func (a *archive) VerifyBranch(ctx context.Context, name string) error {
	b, err := a.GetBranch(ctx, name)
	if err != nil {
		return fmt.Errorf("ranke.Archive.VerifyBranch: %w", err)
	}
	g, err := a.GetGraph(ctx, b.Latest().Head())
	if err != nil {
		return fmt.Errorf("ranke.Archive.VerifyBranch: GetGraph %s: %w", b.Latest().Head().String(), err)
	}
	return g.Validate()
}

func (a *archive) Branches(ctx context.Context) []Branch {
	table, err := a.resolveBranchesTable(ctx)
	if err != nil || table == nil {
		return nil
	}
	out := make([]Branch, 0)
	for _, e := range table.edges {
		if e.typeClass == EdgeContribution && e.typeSub == "branch" {
			b, err := a.buildBranchView(ctx, table, e)
			if err == nil {
				out = append(out, b)
			}
		}
	}
	return out
}

func (a *archive) SetBranch(ctx context.Context, name string, g Graph, contributor Contributor, createdAt ...time.Time) error {
	if g == nil {
		return errors.New("ranke.Archive.SetBranch: nil graph")
	}
	if contributor == nil {
		return errors.New("ranke.Archive.SetBranch: contributor required")
	}
	if len(g.Heads()) == 0 {
		return errors.New("ranke.Archive.SetBranch: graph has no open heads to bind")
	}
	cg, ok := g.(*graph)
	if !ok {
		return errors.New("ranke.Archive.SetBranch: graph from foreign implementation")
	}

	for _, c := range cg.claims {
		if err := a.absorbClaim(ctx, c); err != nil {
			return fmt.Errorf("ranke.Archive.SetBranch: absorb claim: %w", err)
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
			return fmt.Errorf("ranke.Archive.SetBranch: build head edge: %w", err)
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
		return fmt.Errorf("ranke.Archive.SetBranch: build head claim: %w", err)
	}
	headC := headClaim.(*claim)
	if err := a.absorbClaim(ctx, headC); err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: absorb head claim: %w", err)
	}

	var prevTable *claim
	if a.branchesHead != nil {
		prevTable, err = a.lookupClaim(ctx, a.branchesHead)
		if err != nil {
			return fmt.Errorf("ranke.Archive.SetBranch: load previous table: %w", err)
		}
	}

	tableEdges := make([]Edge, 0)
	if prevTable != nil {
		for _, e := range prevTable.edges {
			if e.typeClass == EdgeContribution && e.typeSub == "branch" {
				if string(e.content) == name {
					continue
				}
				copyE, err := NewEdge(EdgeConfig{
					Reference: e.reference,
					TypeClass: EdgeContribution,
					TypeSub:   "branch",
					Content:   e.content,
				})
				if err != nil {
					return fmt.Errorf("ranke.Archive.SetBranch: copy branch edge: %w", err)
				}
				tableEdges = append(tableEdges, copyE)
			}
		}
	}
	newBranch, err := NewEdge(EdgeConfig{
		Reference: headC.node.id,
		TypeClass: EdgeContribution,
		TypeSub:   "branch",
		Content:   []byte(name),
	})
	if err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: build new branch edge: %w", err)
	}
	tableEdges = append(tableEdges, newBranch)
	if prevTable != nil {
		chain, err := NewEdge(EdgeConfig{
			Reference: prevTable.node.id,
			TypeClass: EdgeContribution,
			TypeSub:   "branches",
		})
		if err != nil {
			return fmt.Errorf("ranke.Archive.SetBranch: build chain edge: %w", err)
		}
		tableEdges = append(tableEdges, chain)
	}

	tableClaim, err := ClaimBuilder{
		Type:        NodeBranches,
		Contributor: contributor,
		Edges:       tableEdges,
		CreatedAt:   at,
	}.Sign()
	if err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: build branches claim: %w", err)
	}
	tableC := tableClaim.(*claim)
	if err := a.absorbClaim(ctx, tableC); err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: absorb branches claim: %w", err)
	}

	a.branchesHead = tableC.node.id
	if err := a.bth.Save(ctx, a.branchesHead); err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: persist B_h: %w", err)
	}
	return nil
}
