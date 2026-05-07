package ranke

import (
	"errors"
	"fmt"
)

// archiveBackend is the internal hook between *archive and its persistence
// layer. NewMemArchive uses backend == nil (cache IS the source of
// truth). NewFsArchive wires in an fsBackend that reads/writes the
// filesystem on cache miss/write.
//
// Read paths consult the cache first, then fall back to the backend
// on miss. Write paths populate the cache and, when a backend is
// present, persist through it. Branches are always held fully in
// memory (B is small) and rewritten via saveBranches on change.
type archiveBackend interface {
	loadClaim(idStr string) (*claim, error)
	saveClaim(c *claim) error
	loadContent(idStr string) ([]byte, error)
	saveContent(idStr string, b []byte) error
	saveBranches(b map[string]Id) error
}

// errNotFound is returned by archiveBackend implementations when a
// requested record is not present.
var errNotFound = errors.New("not found")

// --- Lookup helpers (route through cache + backend) ---

// lookupClaim returns the claim for id, consulting the cache first
// and falling back to the backend. Wires the contributor field
// (recursively, if needed) before returning.
func (s *archive) lookupClaim(id Id) (*claim, error) {
	if id == nil {
		return nil, errors.New("nil id")
	}
	k := id.String()
	if c, ok := s.claims[k]; ok {
		return c, nil
	}
	if s.backend == nil {
		return nil, errNotFound
	}
	skel, err := s.backend.loadClaim(k)
	if err != nil {
		return nil, err
	}
	// Cache before wiring contributor (avoids infinite recursion if
	// a claim ever references itself transitively, though normally
	// the only self-reference is the root contributor).
	s.claims[k] = skel
	if err := s.wireContributor(skel); err != nil {
		delete(s.claims, k)
		return nil, err
	}
	return skel, nil
}

// wireContributor sets c.contributor based on c's
// contribution/contributor edge, or self-attributes if c is the
// root (no edges).
func (s *archive) wireContributor(c *claim) error {
	if len(c.edges) == 0 {
		// Root contributor: self-attribute.
		c.contributor = c
		return nil
	}
	for _, e := range c.edges {
		if e.typeClass == EdgeContribution && e.typeSub == "contributor" {
			contribClaim, err := s.lookupClaim(e.reference)
			if err != nil {
				return fmt.Errorf("wire contributor: %w", err)
			}
			c.contributor = contribClaim
			return nil
		}
	}
	// No contribution/contributor edge — invalid for non-root claims.
	return errors.New("non-root claim missing contribution/contributor edge")
}

// putClaim writes c to cache and (if backend) persists it.
func (s *archive) putClaim(c *claim) error {
	k := c.node.id.String()
	if _, exists := s.claims[k]; !exists {
		s.claims[k] = c
	}
	// Always persist (idempotent; the file will already exist if
	// cached from disk).
	if s.backend != nil {
		if err := s.backend.saveClaim(c); err != nil {
			return err
		}
	}
	return nil
}

// putContent writes content bytes to cache and (if backend) persists.
func (s *archive) putContent(id Id, b []byte) error {
	if id == nil || b == nil {
		return nil
	}
	k := id.String()
	s.content[k] = b
	if s.backend != nil {
		if err := s.backend.saveContent(k, b); err != nil {
			return err
		}
	}
	return nil
}

// persistBranches writes the current B map via the backend.
func (s *archive) persistBranches() error {
	if s.backend == nil {
		return nil
	}
	return s.backend.saveBranches(s.branches)
}

// --- Archive interface methods ---

func (s *archive) HasGraph(head Id) bool {
	if head == nil {
		return false
	}
	_, err := s.lookupClaim(head)
	return err == nil
}

func (s *archive) GetGraph(head Id) (Graph, error) {
	if head == nil {
		return nil, errors.New("ranke.Archive.GetGraph: nil head")
	}
	root, err := s.lookupClaim(head)
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
			next, err := s.lookupClaim(e.reference)
			if err != nil {
				return nil, fmt.Errorf("ranke.Archive.GetGraph: missing claim %s referenced by %s: %w", refKey, k, err)
			}
			queue = append(queue, next)
		}
	}
	return g, nil
}

func (s *archive) HasBranch(name string) bool {
	_, ok := s.branches[name]
	return ok
}

func (s *archive) GetBranch(name string) (Branch, error) {
	id, ok := s.branches[name]
	if !ok {
		return nil, fmt.Errorf("ranke.Archive.GetBranch: branch %q not found", name)
	}
	bc, err := s.lookupClaim(id)
	if err != nil {
		return nil, fmt.Errorf("ranke.Archive.GetBranch: branch claim %s: %w", id.String(), err)
	}
	return s.buildBranchView(bc)
}

func (s *archive) buildBranchView(bc *claim) (Branch, error) {
	chain := make([]*branchEntry, 0)
	cur := bc
	for {
		var prev *claim
		for _, e := range cur.edges {
			if e.typeClass == EdgeContribution && e.typeSub == "branch" {
				p, err := s.lookupClaim(e.reference)
				if err != nil {
					return nil, fmt.Errorf("provenance claim %s: %w", e.reference.String(), err)
				}
				prev = p
				break
			}
		}
		if prev == nil {
			break
		}
		chain = append(chain, &branchEntry{claim: prev})
		cur = prev
	}
	return &branch{claim: bc, chain: chain}, nil
}

func (s *archive) SetBranch(name string, g Graph, contributor Contributor) error {
	if g == nil {
		return errors.New("ranke.Archive.SetBranch: nil graph")
	}
	if !g.IsConsolidated() {
		return fmt.Errorf("ranke.Archive.SetBranch: graph must be single-headed (Heads()=%d); consolidate first via a contribution/head claim", len(g.Heads()))
	}
	if contributor == nil {
		return errors.New("ranke.Archive.SetBranch: contributor required")
	}
	cg, ok := g.(*graph)
	if !ok {
		return errors.New("ranke.Archive.SetBranch: graph from foreign implementation")
	}
	head := g.Heads()[0]

	// Absorb every claim and content blob.
	for _, c := range cg.claims {
		if err := s.putClaim(c); err != nil {
			return fmt.Errorf("ranke.Archive.SetBranch: persist claim: %w", err)
		}
		if c.node.content != nil && c.node.contentHash != nil {
			if err := s.putContent(c.node.contentHash, c.node.content); err != nil {
				return fmt.Errorf("ranke.Archive.SetBranch: persist node content: %w", err)
			}
		}
		for _, e := range c.edges {
			if e.content != nil && e.contentHash != nil {
				if err := s.putContent(e.contentHash, e.content); err != nil {
					return fmt.Errorf("ranke.Archive.SetBranch: persist edge content: %w", err)
				}
			}
		}
	}

	// Build the contribution/branch claim per the §4.6 algorithm.
	edges := make([]Edge, 0, 2)
	headEdge, err := NewEdge(EdgeConfig{
		Reference: head,
		TypeClass: EdgeContribution,
		TypeSub:   "head",
	})
	if err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: build head edge: %w", err)
	}
	edges = append(edges, headEdge)
	if prevID, ok := s.branches[name]; ok {
		prevEdge, err := NewEdge(EdgeConfig{
			Reference: prevID,
			TypeClass: EdgeContribution,
			TypeSub:   "branch",
		})
		if err != nil {
			return fmt.Errorf("ranke.Archive.SetBranch: build prev edge: %w", err)
		}
		edges = append(edges, prevEdge)
	}

	branchClaim, err := NewClaim(ClaimConfig{
		TypeClass:     NodeContribution,
		TypeSub:       "branch",
		EncodingClass: EncodingText,
		EncodingSub:   "plain",
		Content:       []byte(name),
		Contributor:   contributor,
		Edges:         edges,
	})
	if err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: build branch claim: %w", err)
	}
	bc := branchClaim.(*claim)
	if err := s.putClaim(bc); err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: persist branch claim: %w", err)
	}
	if bc.node.content != nil && bc.node.contentHash != nil {
		if err := s.putContent(bc.node.contentHash, bc.node.content); err != nil {
			return fmt.Errorf("ranke.Archive.SetBranch: persist branch content: %w", err)
		}
	}

	s.branches[name] = bc.node.id
	if err := s.persistBranches(); err != nil {
		return fmt.Errorf("ranke.Archive.SetBranch: persist branches: %w", err)
	}
	return nil
}

func (s *archive) Branches() []Branch {
	out := make([]Branch, 0, len(s.branches))
	for _, id := range s.branches {
		bc, err := s.lookupClaim(id)
		if err != nil {
			continue
		}
		b, err := s.buildBranchView(bc)
		if err != nil {
			continue
		}
		out = append(out, b)
	}
	return out
}
