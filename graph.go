package ranke

import (
	"errors"
	"fmt"
)

// NewGraph creates a graph rooted at the given contribution/contributor
// claim. The root is the only no-edge claim a graph may contain
// (§4.3); all subsequent AddClaim calls require at least one edge.
func NewGraph(root Contributor) Graph {
	g := &graph{
		claims:     make(map[string]*claim),
		referenced: make(map[string]struct{}),
	}
	if root != nil {
		// Root is itself a Claim; just store it.
		if c, ok := root.(*claim); ok {
			g.claims[c.node.id.String()] = c
		}
	}
	return g
}

func (g *graph) AddClaim(cl Claim) (Id, error) {
	if cl == nil {
		return nil, errors.New("ranke.Graph.AddClaim: nil claim")
	}
	c, ok := cl.(*claim)
	if !ok {
		return nil, errors.New("ranke.Graph.AddClaim: claim from foreign implementation")
	}

	// Idempotency: same id ⇒ no-op.
	key := c.node.id.String()
	if _, exists := g.claims[key]; exists {
		return c.node.id, nil
	}

	// Only the root may have no edges; the root was set at NewGraph.
	if len(c.edges) == 0 {
		return nil, errors.New("ranke.Graph.AddClaim: only the root contribution/contributor (set at NewGraph) may have no edges")
	}

	// Atomic creation rule (§4.3): every edge reference must already
	// be present in the graph.
	for _, e := range c.edges {
		refKey := e.reference.String()
		if _, ok := g.claims[refKey]; !ok {
			return nil, fmt.Errorf("ranke.Graph.AddClaim: edge references unknown claim %s (atomic creation rule, §4.3)", refKey)
		}
	}

	// Insert the claim and mark each referenced claim.
	g.claims[key] = c
	for _, e := range c.edges {
		g.referenced[e.reference.String()] = struct{}{}
	}
	return c.node.id, nil
}

func (g *graph) ContainsClaim(id Id) bool {
	if id == nil {
		return false
	}
	_, ok := g.claims[id.String()]
	return ok
}

func (g *graph) GetClaim(id Id) (Claim, bool) {
	if id == nil {
		return nil, false
	}
	c, ok := g.claims[id.String()]
	if !ok {
		return nil, false
	}
	return c, true
}

func (g *graph) Heads() []Id {
	out := make([]Id, 0)
	for k, c := range g.claims {
		if _, ref := g.referenced[k]; ref {
			continue
		}
		out = append(out, c.node.id)
	}
	return out
}

func (g *graph) IsConsolidated() bool {
	return len(g.Heads()) == 1
}

func (g *graph) Consolidate(contributor Contributor) (Claim, error) {
	heads := g.Heads()
	if len(heads) == 0 {
		return nil, errors.New("ranke.Graph.Consolidate: empty graph")
	}
	if len(heads) == 1 {
		return nil, errors.New("ranke.Graph.Consolidate: graph is already consolidated")
	}
	edges := make([]Edge, 0, len(heads))
	for _, h := range heads {
		e, err := NewEdge(EdgeConfig{
			Reference: h,
			TypeClass: EdgeContribution,
			TypeSub:   "head",
		})
		if err != nil {
			return nil, fmt.Errorf("ranke.Graph.Consolidate: build head edge: %w", err)
		}
		edges = append(edges, e)
	}
	return NewClaim(ClaimConfig{
		TypeClass:   NodeContribution,
		TypeSub:     "head",
		Contributor: contributor,
		Edges:       edges,
	})
}

func (g *graph) Validate() error {
	return g.ValidateWithExceptions()
}

func (g *graph) ValidateWithExceptions(skip ...Id) error {
	skipSet := make(map[string]struct{}, len(skip))
	for _, s := range skip {
		if s != nil {
			skipSet[s.String()] = struct{}{}
		}
	}

	for k, c := range g.claims {
		if _, sk := skipSet[k]; sk {
			continue
		}
		// 1. Recompute the node's id from its canonical encoding and
		//    compare against the stored id (Merkle integrity, §5.2).
		encoded, err := encodeNode(c.node)
		if err != nil {
			return fmt.Errorf("validate %s: encode: %w", k, err)
		}
		recomputed, err := hashContent(encoded)
		if err != nil {
			return fmt.Errorf("validate %s: hash: %w", k, err)
		}
		if !recomputed.Equal(c.node.id) {
			return fmt.Errorf("validate %s: id mismatch (Merkle integrity, §5.2)", k)
		}
		// 2. Every edge reference must resolve in the graph.
		for _, e := range c.edges {
			if _, ok := g.claims[e.reference.String()]; !ok {
				return fmt.Errorf("validate %s: edge references missing claim %s", k, e.reference.String())
			}
		}
	}
	return nil
}
