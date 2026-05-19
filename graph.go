package ranke

import (
	"errors"
	"fmt"
	"time"
)

// NewGraph creates a graph rooted at the given contribution/contributor
// claim. The root is the only no-edge claim a graph may contain
// (§4.3); all subsequent AddClaim calls require at least one edge.
func NewGraph(root Contributor) Graph {
	g := &graph{
		claims:     make(map[string]*claim),
		referenced: make(map[string]struct{}),
	}
	if c := unwrapClaim(root); c != nil {
		g.claims[c.node.id.String()] = c
	}
	return g
}

// unwrapClaim peels any wrapper (e.g. *signedContributor) off a
// Contributor to reach the underlying persisted *claim. Returns nil
// if the chain doesn't end at our concrete type — i.e. the caller
// passed a foreign implementation.
func unwrapClaim(c Contributor) *claim {
	for c != nil {
		switch v := c.(type) {
		case *claim:
			return v
		case *signedContributor:
			c = v.contributor
		default:
			return nil
		}
	}
	return nil
}

func (g *graph) Add(claims ...Claim) error {
	for i, cl := range claims {
		if err := g.addOne(cl); err != nil {
			return fmt.Errorf("Graph.Add: claim %d: %w", i, err)
		}
	}
	return nil
}

func (g *graph) addOne(cl Claim) error {
	if cl == nil {
		return errors.New("nil claim")
	}
	c, ok := cl.(*claim)
	if !ok {
		return errors.New("claim from foreign implementation")
	}
	// Idempotency: same id ⇒ no-op.
	key := c.node.id.String()
	if _, exists := g.claims[key]; exists {
		return nil
	}
	// Only the root may have no edges; the root was set at NewGraph.
	if len(c.edges) == 0 {
		return errors.New("only the root contribution/contributor (set at NewGraph) may have no edges")
	}
	// Atomic creation rule (§4.3): every edge reference must already
	// be present in the graph.
	for _, e := range c.edges {
		refKey := e.reference.String()
		if _, ok := g.claims[refKey]; !ok {
			return fmt.Errorf("edge references unknown claim %s (atomic creation rule, §4.3)", refKey)
		}
	}
	// Insert the claim and mark each referenced claim.
	g.claims[key] = c
	for _, e := range c.edges {
		g.referenced[e.reference.String()] = struct{}{}
	}
	return nil
}

func (g *graph) Contains(id Id) bool {
	if id == nil {
		return false
	}
	_, ok := g.claims[id.String()]
	return ok
}

func (g *graph) Get(id Id) (Claim, bool) {
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

func (g *graph) Consolidate(contributor Contributor, createdAt ...time.Time) (Claim, error) {
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
	head, err := ClaimBuilder{
		Type:        NodeHead,
		Contributor: contributor,
		Edges:       edges,
		CreatedAt:   firstNonZero(createdAt),
	}.Sign()
	if err != nil {
		return nil, err
	}
	if err := g.Add(head); err != nil {
		return nil, fmt.Errorf("ranke.Graph.Consolidate: add head: %w", err)
	}
	return head, nil
}

// firstNonZero returns the first non-zero time in ts, or the zero
// time.Time if all are zero / ts is empty. Used to absorb the
// variadic createdAt parameter on Consolidate / SetBranch.
func firstNonZero(ts []time.Time) time.Time {
	for _, t := range ts {
		if !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}

func (g *graph) Validate(report ...func(c Claim, depth int, err error)) error {
	seen := map[string]bool{}
	var firstErr error
	for _, h := range g.Heads() {
		g.validateRecursive(h, 0, seen, report, &firstErr)
	}
	return firstErr
}

func (g *graph) ValidateWithExceptions(skip ...Id) error {
	skipSet := make(map[string]struct{}, len(skip))
	for _, s := range skip {
		if s != nil {
			skipSet[s.String()] = struct{}{}
		}
	}
	var firstErr error
	g.Validate(func(c Claim, _ int, err error) {
		if firstErr != nil || err == nil {
			return
		}
		if _, sk := skipSet[c.ID().String()]; sk {
			return
		}
		firstErr = fmt.Errorf("validate %s: %w", c.ID().String(), err)
	})
	return firstErr
}

func (g *graph) validateRecursive(id Id, depth int, seen map[string]bool, report []func(Claim, int, error), firstErr *error) {
	k := id.String()
	if seen[k] {
		return
	}
	seen[k] = true
	c, ok := g.claims[k]
	if !ok {
		return
	}
	err := g.verifyOne(c)
	for _, r := range report {
		r(c, depth, err)
	}
	if err != nil && *firstErr == nil {
		*firstErr = fmt.Errorf("validate %s: %w", k, err)
	}
	for _, e := range c.edges {
		g.validateRecursive(e.reference, depth+1, seen, report, firstErr)
	}
}

// verifyOne runs the §5.10 checks on a single claim:
//   - every edge reference resolves to a claim in the graph;
//   - canonical re-encode + recompute H(S(v)) + signature check
//     against id(v) (record integrity + authenticity, §5.2 + §5.7);
//   - if content_hash is set, re-hash the actual content bytes and
//     compare (content integrity).
func (g *graph) verifyOne(c *claim) error {
	for _, e := range c.edges {
		if _, ok := g.claims[e.reference.String()]; !ok {
			return fmt.Errorf("edge references missing claim %s", e.reference.String())
		}
	}
	encoded, err := encodeNode(c.node)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	recomputed, err := hashContent(encoded)
	if err != nil {
		return fmt.Errorf("hash: %w", err)
	}
	pubkey, err := g.resolveClaimPubkey(c)
	if err != nil {
		return fmt.Errorf("resolve pubkey: %w", err)
	}
	idH, ok := c.node.id.(*hash)
	if !ok {
		return errors.New("id not a *hash (foreign id type)")
	}
	if err := verifySignature(pubkey, recomputed.raw, idH.raw); err != nil {
		return fmt.Errorf("§5.7: %w", err)
	}
	// Content integrity (§5.10) is enforced at load time by the
	// backend's loadContent, which stream-verifies (size, hash)
	// against the values signed into the node. By the time a claim
	// reaches verifyOne, its bytes have already been validated — so
	// we don't re-hash here.
	return nil
}

// resolveClaimPubkey returns the pubkey whose matching private key
// signed this claim's id (§5.7). For the initial contributor (a
// claim with no edges, per §4.3), the pubkey is on the claim's own
// node. For every other claim, it is on the node of the contributor
// referenced by the claim's contribution/contributor edge.
func (g *graph) resolveClaimPubkey(c *claim) ([]byte, error) {
	if len(c.edges) == 0 {
		// Initial node (the only no-edge claim a graph may contain).
		return c.node.pubkey, nil
	}
	for _, e := range c.edges {
		if e.typeClass == EdgeContribution && e.typeSub == "contributor" {
			contributor, ok := g.claims[e.reference.String()]
			if !ok {
				return nil, fmt.Errorf("contributor claim %s not in graph", e.reference.String())
			}
			return contributor.node.pubkey, nil
		}
	}
	return nil, errors.New("non-initial claim missing contribution/contributor edge")
}
