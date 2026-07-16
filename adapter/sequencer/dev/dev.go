// package: adapter/sequencer/dev / testkit
// type:    adapter
// job:     a blocking, single-threaded reference Sequencer for tests and development — the sole writer that advances a Ranke-Archive by driving the paper's six steps one contribution at a time
// limits:  not concurrent (steps run serially per AddClaims); manages named branches but does NOT propagate changes between them (paper 2's cross-branch merge); stamps minted claims from the injected Clock so heads are deterministic. NOT for production (-> a concurrent Sequencer adapter).
package dev

import (
	"context"
	"fmt"
	"time"

	"github.com/flocko-motion/ranke-go"
)

// mainBranch is the default branch AddClaims advances when no branch is named
// (AddClaimsToBranch takes an explicit one).
const mainBranch = "main"

// Clock is the deterministic time source the Sequencer stamps the claims it
// mints with (branch tables, consolidations). Kept a local interface so this
// adapter depends only on ranke — generator.Clock satisfies it. Tick returns
// the current time and advances; monotonicity across a run relies on the
// caller sharing one Clock with whatever built the contributed claims.
type Clock interface {
	Tick() time.Time
}

// Sequencer is a minimal reference implementation of the Ranke-Archive write
// path (RankeDB §Sequencer): the single writer that advances the head k → k′
// by merging a contribution. It runs the six steps strictly serially in one
// blocking AddClaims call — unoptimised and deterministic, the honest slow
// version a concurrent server adapter would later replace. Every minted claim
// is stamped from the injected Clock, so a given sequence of adds yields
// identical head ids on every run and across implementations.
type Sequencer struct {
	u     ranke.Universe
	hist  ranke.History
	self  ranke.Contributor
	clock Clock

	head  ranke.Id            // current archive head k (a contribution/branches claim)
	heads map[string]ranke.Id // current consolidated head per branch name (empty until first add)
}

// NewSequencer bootstraps a fresh archive over u: it stores the self
// contributor (an initial node) and mints an empty contribution/branches
// claim whose id is the archive head k₀ (foundation §Ranke-Archive), recorded
// in history. self must carry its signing key (built via AsContributor with a
// key, or WithSigningKey) so the Sequencer can sign the branch-table claims it
// mints.
func NewSequencer(ctx context.Context, u ranke.Universe, hist ranke.History, self ranke.Contributor, clock Clock) (*Sequencer, error) {
	if u == nil || hist == nil || self == nil || clock == nil {
		return nil, fmt.Errorf("dev.NewSequencer: nil argument")
	}
	if self.SigningKey() == nil {
		return nil, fmt.Errorf("dev.NewSequencer: self contributor carries no signing key")
	}
	s := &Sequencer{u: u, hist: hist, self: self, clock: clock, heads: map[string]ranke.Id{}}

	// The self contributor is an initial node — store it so branch-table
	// claims attributed to it resolve.
	if err := s.u.PutClaims(ctx, []ranke.Claim{self}); err != nil {
		return nil, fmt.Errorf("dev.NewSequencer: store contributor: %w", err)
	}
	// Empty branch table → archive head k₀, revision 0.
	bt0, err := s.mintBranchTable(ctx, "")
	if err != nil {
		return nil, err
	}
	if _, err := s.hist.Append(ctx, bt0.ID(), int(bt0.Node().Height()), 0); err != nil {
		return nil, fmt.Errorf("dev.NewSequencer: append history: %w", err)
	}
	s.head = bt0.ID()
	return s, nil
}

// GetContributor returns the contributor the Sequencer signs branch advances
// with.
func (s *Sequencer) GetContributor() ranke.Contributor { return s.self }

// Head returns the current archive head k.
func (s *Sequencer) Head() ranke.Id { return s.head }

// GetArchive returns the immutable snapshot RA_k at the current head.
func (s *Sequencer) GetArchive(ctx context.Context) (ranke.Archive, error) {
	return ranke.NewArchive(ctx, s.u, s.head)
}

// AddClaims advances the "main" branch — the branch-defaulted form of
// AddClaimsToBranch.
func (s *Sequencer) AddClaims(ctx context.Context, claims []ranke.Claim) (ranke.Id, error) {
	return s.AddClaimsToBranch(ctx, mainBranch, claims)
}

// AddClaimsToBranch advances the named branch with a batch of claims — a
// sub-graph in topological order (a claim's references precede it), creating the
// branch on first use. It runs the six steps blocking:
//
//	2 Populate  — load the batch into a graph (also computes open heads)
//	3–4 Verify  — walk + verify the batch's closure
//	  Consolidate— fold the batch's open heads (and this branch's previous head)
//	              into one head so the branch's closure accumulates
//	5 Seed      — write the batch (and any consolidation claims) to 𝒰
//	6 Merge     — mint a new branch-table claim naming every branch, advance k,
//	              record it in history
//
// It returns the new archive head k′. Branches are independent heads the table
// names; the dev Sequencer does not propagate changes between them (paper 2's
// cross-branch merge) — a claim added to one branch does not enter another's.
func (s *Sequencer) AddClaimsToBranch(ctx context.Context, branch string, claims []ranke.Claim) (ranke.Id, error) {
	if len(claims) == 0 {
		return s.head, nil
	}

	// Step 2 — populate. Open a graph over 𝒰 (which already holds the self
	// contributor and prior heads) and add the batch; AddClaims stores each
	// claim and tracks the open-head frontier.
	g, err := ranke.NewGraph(ctx, s.u)
	if err != nil {
		return nil, fmt.Errorf("dev.Sequencer: new graph: %w", err)
	}
	if err := g.AddClaims(ctx, claims...); err != nil {
		return nil, fmt.Errorf("dev.Sequencer: populate: %w", err)
	}

	// Steps 3–4 — verify the batch over its closure.
	run := g.Verify()
	run.Wait()
	if err := run.Err(); err != nil {
		return nil, fmt.Errorf("dev.Sequencer: verify: %w", err)
	}
	if fs := run.Failures(); len(fs) > 0 {
		return nil, fmt.Errorf("dev.Sequencer: verify: %d failure(s), first: %v", len(fs), fs[0])
	}

	// Step 5 — seed. This Graph writes every added claim straight to 𝒰 (both
	// AddClaims above and Consolidate below), so populate already seeded the
	// batch: a separate PutClaims here would write each claim a second time.
	// Everything below is therefore added THROUGH the graph so 𝒰 is written
	// exactly once per claim.

	// Consolidate the batch's open heads into one head.
	batchHead, err := s.consolidateGraph(ctx, g)
	if err != nil {
		return nil, err
	}

	// Fold this branch's previous head in, so its closure accumulates across
	// successive adds. The folding head is added through the graph (one write).
	newHead := batchHead
	if prior, ok := s.heads[branch]; ok {
		hc, err := s.consolidateHeads(ctx, prior, batchHead)
		if err != nil {
			return nil, err
		}
		if err := g.AddClaims(ctx, hc); err != nil {
			return nil, fmt.Errorf("dev.Sequencer: fold heads: %w", err)
		}
		newHead = hc.ID()
	}
	s.heads[branch] = newHead

	// Step 6 — merge: mint the next branch table (a diff over the previous one,
	// restating this branch), advance head, record it at the next revision.
	bt, err := s.mintBranchTable(ctx, branch)
	if err != nil {
		return nil, err
	}
	revision, err := s.hist.Len(ctx)
	if err != nil {
		return nil, fmt.Errorf("dev.Sequencer: history length: %w", err)
	}
	if _, err := s.hist.Append(ctx, bt.ID(), int(bt.Node().Height()), revision); err != nil {
		return nil, fmt.Errorf("dev.Sequencer: append history: %w", err)
	}
	s.head = bt.ID()
	return s.head, nil
}

// consolidateGraph returns the single open head of g, consolidating via a
// contribution/head claim (added through the graph, so written to 𝒰) when g
// has several open heads.
func (s *Sequencer) consolidateGraph(ctx context.Context, g ranke.Graph) (ranke.Id, error) {
	if g.IsConsolidated() {
		return g.Heads()[0], nil
	}
	head, err := g.Consolidate(ctx, s.self, s.clock.Tick())
	if err != nil {
		return nil, fmt.Errorf("dev.Sequencer: consolidate: %w", err)
	}
	return head.ID(), nil
}

// consolidateHeads builds a contribution/head claim wrapping the given heads —
// the same construction Graph.Consolidate performs, but over heads that live
// in 𝒰 rather than in one in-memory graph.
func (s *Sequencer) consolidateHeads(ctx context.Context, heads ...ranke.Id) (ranke.Claim, error) {
	edges := make([]ranke.Edge, 0, len(heads))
	for _, h := range heads {
		e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: h, Type: ranke.EdgeTypeHead})
		if err != nil {
			return nil, fmt.Errorf("dev.Sequencer: head edge: %w", err)
		}
		edges = append(edges, e)
	}
	// The heads and the self contributor all live in 𝒰, so WithAutoHeight
	// resolves the new head's height (§4.1) from their committed heights.
	return ranke.NewClaim(ranke.NodeHead, s.self).
		WithEdges(edges...).
		WithCreatedAt(s.clock.Tick()).
		WithAutoHeight(ctx, s.u).
		Sign()
}

// mintBranchTable builds and stores the next contribution/branches claim — the
// new archive head. Except for the bootstrap it is a contribution/diff over the
// previous table (s.head), restating ONLY the branch this revision advanced
// (changed); the other branches are inherited by overlaying the diff chain back
// to the initial empty table (§Branches). So the prior branch tables stay in the
// head's provenance — the spine (§Archive) — reachable and taggable, rather than
// each revision orphaning the last. Bootstrap (s.head nil, changed "") is the
// empty table. Returns the new table (its id is the archive head k, its height
// the generation to record in History).
func (s *Sequencer) mintBranchTable(ctx context.Context, changed string) (ranke.Claim, error) {
	b := ranke.NewClaim(ranke.NodeBranches, s.self).WithCreatedAt(s.clock.Tick())
	if s.head != nil {
		b = b.WithDiff(s.head) // diff over the previous table — build the spine
	}
	if changed != "" {
		// Restate only the advanced branch; the diff inherits the rest. The
		// branch edge is named (its branch name), as diff-claim edges must be.
		e, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference: s.heads[changed],
			Type:      ranke.EdgeTypeBranch,
			Fields:    map[string]string{ranke.FieldName: changed},
		})
		if err != nil {
			return nil, fmt.Errorf("dev.Sequencer: branch edge: %w", err)
		}
		b = b.WithEdges(e)
	}
	// The self contributor, the previous table, and the branch head are in 𝒰;
	// resolve height from them (§4.1).
	b = b.WithAutoHeight(ctx, s.u)
	table, err := b.Sign()
	if err != nil {
		return nil, fmt.Errorf("dev.Sequencer: mint branch table: %w", err)
	}
	if err := s.u.PutClaims(ctx, []ranke.Claim{table}); err != nil {
		return nil, fmt.Errorf("dev.Sequencer: store branch table: %w", err)
	}
	return table, nil
}
