// package: ranke / contribution
// type:    logic
// job:     the contribution — the six-step advance of a Ranke-Archive (RA_k → RA_k'), staged, sealed, and merged
// limits:  owns no storage of its own — reads and writes go through its base Archive's Universe and head store (-> archive)
package ranke

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type Contribution interface {
	// Base is the (k, t) the contribution was opened against.
	Base() (k ranke.Id, t time.Time)
	// Add fills the contribution with a graph of claims (step 2).
	// Refused once the contribution is sealed.
	AddGraph(graph ranke.Graph) error
	AddClaims(claims []ranke.Claim) error
	// Seal verifies the claims against the base closure and fixes the
	// contents (steps 3–4); Add is refused afterwards.
	CompleteAndVerify(ctx context.Context) (VerifiedContribution, error)
	// Commit persists the sealed closure and merges it, advancing the
	// head, and returns the new archive RA_k' (steps 5–6). Seals first if
	// the caller did not.
}

type VerifiedContribution interface{}
	Persist(ctx context.Context) (MergableContribution, error)
  Ids() ([]Id, error)
}
  

type contribution struct {
	base        *archive    // the RA_k this contribution advances from
	branch      string      // target branch
	contributor Contributor // signs the consolidating contribution/head
	baseHead    Id          // k at Opening — guards against a moved head
	t           time.Time   // time stamped at Opening

	graph  Graph // accumulated claims (step 2)
	sealed bool  // true once Seal ran — Add refused thereafter
	head   Id    // the contribution/head minted at Commit
}

// Base returns the (k, t) this contribution was opened against.
func (c *Contribution) Base() (head Id, t time.Time) { return c.baseHead, c.t }

// Add fills the contribution with a graph of claims (step 2, Filling). It
// is refused once sealed. Every claim is checked to be dated ≤ t and to
// use no Sequencer-reserved type (limiting and branch-table claims are
// the Sequencer's alone).
func (c *Contribution) Add(g Graph) error {
	if c.sealed {
		return errors.New("ranke.Contribution.Add: sealed — no more claims after verification begins")
	}
	if g == nil {
		return errors.New("ranke.Contribution.Add: nil graph")
	}
	cg, ok := g.(*graph)
	if !ok {
		return errors.New("ranke.Contribution.Add: graph from foreign implementation")
	}
	for _, cl := range cg.claims {
		if cl.node.createdAt.After(c.t) {
			return fmt.Errorf("ranke.Contribution.Add: claim %s dated after contribution time", cl.node.id.String())
		}
		if reserved := sequencerReservedType(cl); reserved != "" {
			return fmt.Errorf("ranke.Contribution.Add: claim %s uses Sequencer-reserved type %s", cl.node.id.String(), reserved)
		}
	}
	for _, cl := range topoOrder(cg) {
		if err := c.graph.Add(cl); err != nil {
			return fmt.Errorf("ranke.Contribution.Add: %w", err)
		}
	}
	return nil
}

// Seal runs Verifying + Completing (steps 3–4): it verifies the claims
// against the base branch's closure — pruning the walk at already
// committed claims (VerifyDelta) so only the contribution itself is
// checked — then fixes the contents. After Seal, Add is refused. Seal is
// idempotent.
func (c *Contribution) Seal(ctx context.Context) error {
	if c.sealed {
		return nil
	}
	cg, ok := c.graph.(*graph)
	if !ok {
		return errors.New("ranke.Contribution.Seal: graph from foreign implementation")
	}
	if len(cg.Heads()) == 0 {
		return errors.New("ranke.Contribution.Seal: empty contribution")
	}

	// Step 3 — Verifying: trust the base branch's committed closure.
	var trusted []Id
	if c.base.table.Has(c.branch) {
		b, err := c.base.table.Get(ctx, c.branch)
		if err != nil {
			return fmt.Errorf("ranke.Contribution.Seal: %w", err)
		}
		trusted, err = c.base.closureIds(ctx, b.Latest().Head())
		if err != nil {
			return fmt.Errorf("ranke.Contribution.Seal: base closure: %w", err)
		}
	}
	if err := cg.VerifyDelta(trusted...); err != nil {
		return fmt.Errorf("ranke.Contribution.Seal: %w", err)
	}

	// Step 4 — Completing: a materialized graph already carries its full
	// referenced closure (atomic-creation rule), so a single-branch
	// contribution draws in nothing. Cross-branch completion is a
	// server-layer concern.

	c.sealed = true
	return nil
}

// Commit runs Persisting + Merging (steps 5–6) and returns the advanced
// archive RA_k'. It seals first if the caller has not. Persisting seeds
// the sealed closure into 𝒰; Merging consolidates the open heads under
// one contribution/head (signed by the contributor), references it from a
// new branch-table claim (signed by the base's self attestor), and
// advances the head k → k'.
func (c *Contribution) Commit(ctx context.Context) (Archive, error) {
	if err := c.Seal(ctx); err != nil {
		return nil, err
	}
	// Guard: the base head must not have advanced under this contribution.
	// The naive sequencer serialises contributions, so this never trips; a
	// server that batches contributions handles a moved head itself.
	if !idEqual(c.base.table.Bh(), c.baseHead) {
		return nil, errors.New("ranke.Contribution.Commit: base archive advanced since it was opened")
	}
	cg := c.graph.(*graph)

	// Step 5 — Persisting: write the sealed closure to 𝒰. Content
	// addressing stores only what is not already present.
	for _, cl := range cg.claims {
		if err := c.base.seed(ctx, cl); err != nil {
			return nil, fmt.Errorf("ranke.Contribution.Commit: seed claim: %w", err)
		}
	}

	// Step 6 — Merging: consolidate the open heads (contributor-signed),
	// reference the consolidating head from a new branch-table claim
	// (self-signed), advance the head.
	headID, err := c.consolidate(ctx)
	if err != nil {
		return nil, fmt.Errorf("ranke.Contribution.Commit: %w", err)
	}
	c.head = headID
	nextTable, err := c.base.table.Set(ctx, c.branch, headID, c.base.self, c.t)
	if err != nil {
		return nil, fmt.Errorf("ranke.Contribution.Commit: mint branch table: %w", err)
	}
	if err := c.base.bth.Save(ctx, nextTable.Bh()); err != nil {
		return nil, fmt.Errorf("ranke.Contribution.Commit: persist head id: %w", err)
	}
	return newWritableArchive(c.base.u, nextTable, c.base.bth, c.base.self), nil
}

// consolidate mints the contribution/head wrapping the open heads (signed
// by the contributor, per §Consolidation), seeds it, and returns its id —
// the head the branch binds to.
func (c *Contribution) consolidate(ctx context.Context) (Id, error) {
	heads := c.graph.Heads()
	edges := make([]Edge, 0, len(heads))
	for _, h := range heads {
		edge, err := NewEdge(EdgeConfig{
			Reference: h,
			TypeClass: EdgeContribution,
			TypeSub:   "head",
		})
		if err != nil {
			return nil, fmt.Errorf("build head edge: %w", err)
		}
		edges = append(edges, edge)
	}
	headClaim, err := ClaimBuilder{
		Type:        NodeHead,
		Contributor: c.contributor,
		Edges:       edges,
		CreatedAt:   c.t,
	}.Sign()
	if err != nil {
		return nil, fmt.Errorf("build head claim: %w", err)
	}
	headC := headClaim.(*claim)
	if err := c.base.seed(ctx, headC); err != nil {
		return nil, fmt.Errorf("seed head claim: %w", err)
	}
	return headC.node.id, nil
}

// topoOrder returns g's claims with references before referrers, so they
// can be re-added to another graph under the atomic-creation rule.
func topoOrder(g *graph) []*claim {
	seen := map[string]bool{}
	out := make([]*claim, 0, len(g.claims))
	var visit func(c *claim)
	visit = func(c *claim) {
		if seen[c.node.id.String()] {
			return
		}
		seen[c.node.id.String()] = true
		for _, e := range c.edges {
			if ref, ok := g.claims[e.reference.String()]; ok {
				visit(ref)
			}
		}
		out = append(out, c)
	}
	for _, c := range g.claims {
		visit(c)
	}
	return out
}

// idEqual compares two ids, treating two nils as equal.
func idEqual(a, b Id) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(b)
}

// sequencerReservedType names the type a claim carries that only the
// Sequencer may mint — a branch-table claim or a limiting claim (early
// key expiry) — or "" when the claim is admissible in a contribution
// (step 2). A consolidating contribution/head is not reserved:
// contributors build those.
func sequencerReservedType(c *claim) string {
	if c.node.typeClass != NodeContribution {
		return ""
	}
	switch c.node.typeSub {
	case "branches":
		return NodeBranches
	case "expiry":
		return string(NodeContribution) + "/expiry"
	default:
		return ""
	}
}
