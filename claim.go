package ranke

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"time"
)

// NewClaim constructs a Claim atomically per §4.3:
//
//   - validates type, encoding, content/contenthash exclusion
//   - rejects derivation/, entity/, relation/* nodes that lack a
//     derivation/* edge (provenance invariant, §3.5)
//   - auto-builds the contribution/contributor edge from
//     cfg.Contributor (omitted only for the root contribution/contributor
//     claim, which self-attributes per §4.3)
//   - sorts edges canonically (by id bytes), computes the node id
//     as H(canonical(node))
//
// The returned Claim is immutable.
func NewClaim(cfg ClaimConfig) (Claim, error) {
	if cfg.TypeClass == "" || cfg.TypeSub == "" {
		return nil, errors.New("ranke.NewClaim: TypeClass and TypeSub are required")
	}
	if !validNodeClass(cfg.TypeClass) {
		return nil, fmt.Errorf("ranke.NewClaim: unknown NodeClass %q", cfg.TypeClass)
	}
	if cfg.EncodingClass != "" && !validEncodingClass(cfg.EncodingClass) {
		return nil, fmt.Errorf("ranke.NewClaim: unknown EncodingClass %q", cfg.EncodingClass)
	}
	if cfg.Content != nil && cfg.ContentHash != nil {
		return nil, errors.New("ranke.NewClaim: Content and ContentHash are mutually exclusive")
	}

	isRootContributor := cfg.TypeClass == NodeContribution &&
		cfg.TypeSub == "contributor" &&
		cfg.Contributor == nil

	// Every non-root claim needs a contributor.
	if !isRootContributor && cfg.Contributor == nil {
		return nil, errors.New("ranke.NewClaim: Contributor is required (only the root contribution/contributor may omit it)")
	}

	// Collect the edges. NewClaim auto-builds the
	// contribution/contributor edge unless this is the root.
	edges := make([]*edge, 0, len(cfg.Edges)+1)
	for _, e := range cfg.Edges {
		ce, err := asConcreteEdge(e)
		if err != nil {
			return nil, fmt.Errorf("ranke.NewClaim: %w", err)
		}
		edges = append(edges, ce)
	}
	if !isRootContributor {
		ce, err := buildContributorEdge(cfg.Contributor)
		if err != nil {
			return nil, fmt.Errorf("ranke.NewClaim: build contribution/contributor edge: %w", err)
		}
		edges = append(edges, ce)
	}

	// Provenance invariant (§3.5): derivation/, entity/, and
	// relation/ claims must carry at least one derivation/* edge.
	if requiresProvenance(cfg.TypeClass) {
		if !hasDerivationEdge(edges) {
			return nil, fmt.Errorf("ranke.NewClaim: %s/%s claims must carry at least one derivation/* edge (§3.5 provenance invariant)", cfg.TypeClass, cfg.TypeSub)
		}
	}

	// Canonical edge order: by raw multihash bytes.
	sort.SliceStable(edges, func(i, j int) bool {
		return bytes.Compare(idBytes(edges[i].id), idBytes(edges[j].id)) < 0
	})

	createdAt := cfg.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	} else {
		createdAt = createdAt.UTC()
	}

	n := &node{
		typeClass:     cfg.TypeClass,
		typeSub:       cfg.TypeSub,
		encodingClass: cfg.EncodingClass,
		encodingSub:   cfg.EncodingSub,
		createdAt:     createdAt,
		fields:        cloneFields(cfg.Fields),
		content:       cfg.Content,
	}

	// Resolve content hash.
	if cfg.ContentHash != nil {
		n.contentHash = cfg.ContentHash
	} else if cfg.Content != nil {
		ch, err := hashContent(cfg.Content)
		if err != nil {
			return nil, fmt.Errorf("ranke.NewClaim: content hash: %w", err)
		}
		n.contentHash = ch
	}

	// Edge ids on the node, in sorted order.
	n.edges = make([]Id, len(edges))
	for i, e := range edges {
		n.edges[i] = e.id
	}

	// Compute the node id (= claim id).
	encoded, err := encodeNode(n)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewClaim: canonical encode: %w", err)
	}
	id, err := hashContent(encoded)
	if err != nil {
		return nil, fmt.Errorf("ranke.NewClaim: hash: %w", err)
	}
	n.id = id

	c := &claim{
		node:  n,
		edges: edges,
	}

	// Wire the contributor: self for root, else the supplied contributor.
	if isRootContributor {
		c.contributor = c // self-attribute
	} else {
		c.contributor = cfg.Contributor
	}
	return c, nil
}

func (c *claim) Node() Node { return c.node }
func (c *claim) ID() Id     { return c.node.id }

func (c *claim) Edges(filters ...Filter) []Edge {
	if len(filters) == 0 {
		out := make([]Edge, len(c.edges))
		for i, e := range c.edges {
			out[i] = e
		}
		return out
	}
	out := make([]Edge, 0, len(c.edges))
	for _, e := range c.edges {
		if matchAll(e, filters) {
			out = append(out, e)
		}
	}
	return out
}

func matchAll(e Edge, filters []Filter) bool {
	for _, f := range filters {
		if !f.Match(e) {
			return false
		}
	}
	return true
}

func (c *claim) Contributor() Contributor {
	if c.contributor == nil {
		return nil
	}
	return c.contributor
}

func (c *claim) IsContributor() bool {
	return c.node.typeClass == NodeContribution && c.node.typeSub == "contributor"
}

func (c *claim) AsContributor() (Contributor, error) {
	if !c.IsContributor() {
		return nil, fmt.Errorf("ranke.Claim.AsContributor: claim has type %s, not contribution/contributor", c.node.Type())
	}
	return c, nil
}

// --- helpers ---

// requiresProvenance reports whether claims of this class need at
// least one derivation/* edge per §3.5.
func requiresProvenance(c NodeClass) bool {
	switch c {
	case NodeDerivation, NodeEntity, NodeRelation:
		return true
	}
	return false
}

func hasDerivationEdge(edges []*edge) bool {
	for _, e := range edges {
		if e.typeClass == EdgeDerivation {
			return true
		}
	}
	return false
}

// asConcreteEdge unwraps an Edge interface into our concrete *edge
// type. We only know how to handle our own implementation.
func asConcreteEdge(e Edge) (*edge, error) {
	if e == nil {
		return nil, errors.New("nil edge")
	}
	ce, ok := e.(*edge)
	if !ok {
		return nil, errors.New("edge from foreign implementation")
	}
	return ce, nil
}

// buildContributorEdge constructs the contribution/contributor edge
// referencing the given contributor. NewClaim calls this when the
// caller doesn't supply it explicitly.
func buildContributorEdge(c Contributor) (*edge, error) {
	if c == nil {
		return nil, errors.New("nil contributor")
	}
	e, err := NewEdge(EdgeConfig{
		Reference: c.ID(),
		TypeClass: EdgeContribution,
		TypeSub:   "contributor",
	})
	if err != nil {
		return nil, err
	}
	return e.(*edge), nil
}
