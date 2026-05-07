package ranke

import (
	"errors"
	"fmt"
)

// archiveBackend is the internal hook between *archive and its
// persistence layer. NewMemArchive uses backend == nil (cache IS the
// source of truth). NewFsArchive wires in an fsBackend that reads/
// writes the filesystem on cache miss/write.
//
// The branches model collapses to a single hash B_h on disk: the
// backend persists only the id of the current contribution/branches
// claim. Everything else (the table itself, prior tables, per-branch
// head claims) lives in U, durably persisted via saveClaim/saveContent.
type archiveBackend interface {
	loadClaim(idStr string) (*claim, error)
	saveClaim(c *claim) error
	loadContent(idStr string) ([]byte, error)
	saveContent(idStr string, b []byte) error
	saveBranchesHead(id Id) error // writes B_h
}

// errNotFound is returned by archiveBackend implementations when a
// requested record is not present.
var errNotFound = errors.New("not found")

// --- Lookup helpers ---

// lookupClaim returns the claim for id, consulting the cache first
// and falling back to the backend. Eager-loads content for the
// node and each edge that names a ContentHash (so e.g. branch-table
// walks can read edge content without a separate fetch). Wires the
// contributor field (recursively, if needed) before returning.
func (a *archive) lookupClaim(id Id) (*claim, error) {
	if id == nil {
		return nil, errors.New("nil id")
	}
	k := id.String()
	if c, ok := a.claims[k]; ok {
		return c, nil
	}
	if a.backend == nil {
		return nil, errNotFound
	}
	skel, err := a.backend.loadClaim(k)
	if err != nil {
		return nil, err
	}
	a.claims[k] = skel
	if err := a.loadClaimContent(skel); err != nil {
		delete(a.claims, k)
		return nil, err
	}
	if err := a.wireContributor(skel); err != nil {
		delete(a.claims, k)
		return nil, err
	}
	return skel, nil
}

// loadClaimContent fetches and attaches content bytes for the node
// and each edge that has a ContentHash but no in-memory content yet.
// Backed by fetchContent (cache + backend).
func (a *archive) loadClaimContent(c *claim) error {
	if c.node.contentHash != nil && c.node.content == nil {
		b, err := a.fetchContent(c.node.contentHash)
		if err != nil {
			return fmt.Errorf("node content %s: %w", c.node.contentHash.String(), err)
		}
		c.node.content = b
	}
	for _, e := range c.edges {
		if e.contentHash != nil && e.content == nil {
			b, err := a.fetchContent(e.contentHash)
			if err != nil {
				return fmt.Errorf("edge content %s: %w", e.contentHash.String(), err)
			}
			e.content = b
		}
	}
	return nil
}

// fetchContent returns content bytes for id, consulting the cache
// then the backend. Caches successful loads.
func (a *archive) fetchContent(id Id) ([]byte, error) {
	k := id.String()
	if b, ok := a.content[k]; ok {
		return b, nil
	}
	if a.backend == nil {
		return nil, errNotFound
	}
	b, err := a.backend.loadContent(k)
	if err != nil {
		return nil, err
	}
	a.content[k] = b
	return b, nil
}

// wireContributor sets c.contributor based on c's
// contribution/contributor edge, or self-attributes if c is the
// root (no edges).
func (a *archive) wireContributor(c *claim) error {
	if len(c.edges) == 0 {
		c.contributor = c
		return nil
	}
	for _, e := range c.edges {
		if e.typeClass == EdgeContribution && e.typeSub == "contributor" {
			cc, err := a.lookupClaim(e.reference)
			if err != nil {
				return fmt.Errorf("wire contributor: %w", err)
			}
			c.contributor = cc
			return nil
		}
	}
	return errors.New("non-root claim missing contribution/contributor edge")
}

// absorbClaim writes c (and any node/edge content it carries) to
// cache and the backend. Idempotent.
func (a *archive) absorbClaim(c *claim) error {
	k := c.node.id.String()
	if _, ok := a.claims[k]; !ok {
		a.claims[k] = c
	}
	if a.backend != nil {
		if err := a.backend.saveClaim(c); err != nil {
			return err
		}
	}
	if c.node.content != nil && c.node.contentHash != nil {
		if err := a.absorbContent(c.node.contentHash, c.node.content); err != nil {
			return err
		}
	}
	for _, e := range c.edges {
		if e.content != nil && e.contentHash != nil {
			if err := a.absorbContent(e.contentHash, e.content); err != nil {
				return err
			}
		}
	}
	return nil
}

func (a *archive) absorbContent(id Id, b []byte) error {
	k := id.String()
	a.content[k] = b
	if a.backend != nil {
		return a.backend.saveContent(k, b)
	}
	return nil
}

// --- Archive interface methods ---

func (a *archive) HasGraph(head Id) bool {
	if head == nil {
		return false
	}
	_, err := a.lookupClaim(head)
	return err == nil
}

func (a *archive) GetGraph(head Id) (Graph, error) {
	if head == nil {
		return nil, errors.New("ranke.Archive.GetGraph: nil head")
	}
	root, err := a.lookupClaim(head)
	if err != nil {
		return nil, fmt.Errorf("ranke.Archive.GetGraph: head %s: %w", head.String(), err)
	}
	g := &graph{
		claims:     make(map[string]*claim),
		referenced: make(map[string]struct{}),
	}
	queue := []*claim{root}
	for len(queue) > 0 {
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
			next, err := a.lookupClaim(e.reference)
			if err != nil {
				return nil, fmt.Errorf("ranke.Archive.GetGraph: missing claim %s referenced by %s: %w", refKey, k, err)
			}
			queue = append(queue, next)
		}
	}
	return g, nil
}

// --- Branches ---

// resolveBranchesTable returns the current contribution/branches
// claim, or nil if the archive has no branches yet.
func (a *archive) resolveBranchesTable() (*claim, error) {
	if a.branchesHead == nil {
		return nil, nil
	}
	return a.lookupClaim(a.branchesHead)
}

// findBranchEdge walks the current branch table for a
// contribution/branch edge whose content matches name. Returns the
// table claim and the edge, or (table, nil, nil) if the table exists
// but the name is not in it.
func (a *archive) findBranchEdge(name string) (*claim, *edge, error) {
	table, err := a.resolveBranchesTable()
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

func (a *archive) HasBranch(name string) bool {
	_, e, err := a.findBranchEdge(name)
	return err == nil && e != nil
}

func (a *archive) GetBranch(name string) (Branch, error) {
	table, e, err := a.findBranchEdge(name)
	if err != nil {
		return nil, fmt.Errorf("ranke.Archive.GetBranch: %w", err)
	}
	if e == nil {
		return nil, fmt.Errorf("ranke.Archive.GetBranch: branch %q not found", name)
	}
	return a.buildBranchView(table, e)
}

// buildBranchView returns a Branch projection of one (name, head)
// binding, with provenance pre-walked through prior branch tables.
func (a *archive) buildBranchView(table *claim, branchEdge *edge) (Branch, error) {
	name := string(branchEdge.content)
	head := branchEdge.reference

	chain := make([]*branchEntry, 0)
	cur := table
	for {
		var prev *claim
		for _, e := range cur.edges {
			if e.typeClass == EdgeContribution && e.typeSub == "branches" {
				p, err := a.lookupClaim(e.reference)
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
		// Find this branch's binding in the prior table.
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

func (a *archive) Branches() []Branch {
	table, err := a.resolveBranchesTable()
	if err != nil || table == nil {
		return nil
	}
	out := make([]Branch, 0)
	for _, e := range table.edges {
		if e.typeClass == EdgeContribution && e.typeSub == "branch" {
			b, err := a.buildBranchView(table, e)
			if err == nil {
				out = append(out, b)
			}
		}
	}
	return out
}

// SetBranch advances the named branch per paper §4.6. Two new claims
// are created and added to U:
//
//  1. A contribution/head claim that consolidates g's open heads.
//     One contribution/head edge per open head; the branch will
//     point at this claim.
//  2. A new contribution/branches claim — the new branch table —
//     carrying contribution/branch edges for every active branch
//     (the named one updated to the new head, all others copied
//     unchanged) plus a contribution/branches edge to the previous
//     table (if any).
//
// Then B_h is updated to the new table's id and persisted via the
// backend.
func (a *archive) SetBranch(name string, g Graph, contributor Contributor) error {
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

	// Absorb every claim in g first — atomic creation rule means
	// references must be in U before we build new claims that point
	// at them.
	for _, c := range cg.claims {
		if err := a.absorbClaim(c); err != nil {
			return fmt.Errorf("ranke.Archive.SetBranch: absorb claim: %w", err)
		}
	}

	// Step 1: build the contribution/head claim consolidating g's
	// open heads.
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
	headClaim, err := NewClaim(ClaimConfig{
		TypeClass:   NodeContribution,
		TypeSub:     "head",
		Contributor: contributor,
		Edges:       headEdges,
	})
	if err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: build head claim: %w", err)
	}
	headC := headClaim.(*claim)
	if err := a.absorbClaim(headC); err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: absorb head claim: %w", err)
	}

	// Step 2: build the new contribution/branches table.
	var prevTable *claim
	if a.branchesHead != nil {
		prevTable, err = a.lookupClaim(a.branchesHead)
		if err != nil {
			return fmt.Errorf("ranke.Archive.SetBranch: load previous table: %w", err)
		}
	}

	tableEdges := make([]Edge, 0)
	// Carry forward every other branch unchanged.
	if prevTable != nil {
		for _, e := range prevTable.edges {
			if e.typeClass == EdgeContribution && e.typeSub == "branch" {
				if string(e.content) == name {
					continue // updating this one
				}
				copyE, err := NewEdge(EdgeConfig{
					Reference:     e.reference,
					TypeClass:     EdgeContribution,
					TypeSub:       "branch",
					EncodingClass: EncodingText,
					EncodingSub:   "plain",
					Content:       e.content,
				})
				if err != nil {
					return fmt.Errorf("ranke.Archive.SetBranch: copy branch edge: %w", err)
				}
				tableEdges = append(tableEdges, copyE)
			}
		}
	}
	// New/updated branch edge.
	newBranch, err := NewEdge(EdgeConfig{
		Reference:     headC.node.id,
		TypeClass:     EdgeContribution,
		TypeSub:       "branch",
		EncodingClass: EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(name),
	})
	if err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: build new branch edge: %w", err)
	}
	tableEdges = append(tableEdges, newBranch)
	// Chain edge to previous table (if any).
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

	tableClaim, err := NewClaim(ClaimConfig{
		TypeClass:   NodeContribution,
		TypeSub:     "branches",
		Contributor: contributor,
		Edges:       tableEdges,
	})
	if err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: build branches claim: %w", err)
	}
	tableC := tableClaim.(*claim)
	if err := a.absorbClaim(tableC); err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: absorb branches claim: %w", err)
	}

	// Step 3: update B_h.
	a.branchesHead = tableC.node.id
	if a.backend != nil {
		if err := a.backend.saveBranchesHead(a.branchesHead); err != nil {
			return fmt.Errorf("ranke.Archive.SetBranch: persist B_h: %w", err)
		}
	}
	return nil
}
