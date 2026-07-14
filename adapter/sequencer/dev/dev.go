// package: adapter/sequencer/dev / testkit
// type:    adapter
// job:     a blocking, single-threaded reference Sequencer for tests and development — the sole writer that advances a Ranke-Archive by driving the paper's six steps one contribution at a time
// limits:  not concurrent (steps run serially per AddClaims); manages a single "main" branch for now; stamps minted claims from the injected Clock so heads are deterministic. NOT for production (-> a concurrent Sequencer adapter).
package dev

import (
	"context"
	"fmt"
	"time"

	"github.com/flocko-motion/ranke-go"
)

// mainBranch is the single branch this Sequencer manages for now. The
// production Sequencer contract will carry a branch name through the write
// path; until it does, the dev Sequencer advances one implicit branch.
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

	head     ranke.Id // current archive head k (a contribution/branches claim)
	mainHead ranke.Id // current consolidated head of the "main" branch (nil until first add)
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
	s := &Sequencer{u: u, hist: hist, self: self, clock: clock}

	// The self contributor is an initial node — store it so branch-table
	// claims attributed to it resolve.
	if err := s.u.PutClaims(ctx, []ranke.Claim{self}); err != nil {
		return nil, fmt.Errorf("dev.NewSequencer: store contributor: %w", err)
	}
	// Empty branch table → archive head k₀.
	k0, err := s.mintBranchTable(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := s.hist.Append(ctx, k0); err != nil {
		return nil, fmt.Errorf("dev.NewSequencer: append history: %w", err)
	}
	s.head = k0
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

// AddClaims advances the "main" branch with a batch of claims — a sub-graph in
// topological order (a claim's references precede it). It runs the six steps
// blocking:
//
//	2 Populate  — load the batch into a graph (also computes open heads)
//	3–4 Verify  — walk + verify the batch's closure
//	  Consolidate— fold the batch's open heads (and the previous main head)
//	              into one head so the branch closure accumulates
//	5 Seed      — write the batch (and any consolidation claims) to 𝒰
//	6 Merge     — mint a new branch-table claim (main → new head), advance k,
//	              record it in history
//
// It returns the new archive head k′.
func (s *Sequencer) AddClaims(ctx context.Context, claims []ranke.Claim) (ranke.Id, error) {
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

	// Consolidate the batch's open heads into one head.
	toPersist := append([]ranke.Claim(nil), claims...)
	batchHead, extra, err := s.consolidateGraph(ctx, g)
	if err != nil {
		return nil, err
	}
	toPersist = append(toPersist, extra...)

	// Fold the previous main head in, so the branch closure accumulates across
	// successive adds.
	newMain := batchHead
	if s.mainHead != nil {
		hc, err := s.consolidateHeads(s.mainHead, batchHead)
		if err != nil {
			return nil, err
		}
		toPersist = append(toPersist, hc)
		newMain = hc.ID()
	}

	// Step 5 — seed 𝒰.
	if err := s.u.PutClaims(ctx, toPersist); err != nil {
		return nil, fmt.Errorf("dev.Sequencer: seed: %w", err)
	}

	// Step 6 — merge: new branch table main → newMain, advance head, record it.
	k, err := s.mintBranchTable(ctx, newMain)
	if err != nil {
		return nil, err
	}
	if _, err := s.hist.Append(ctx, k); err != nil {
		return nil, fmt.Errorf("dev.Sequencer: append history: %w", err)
	}
	s.head = k
	s.mainHead = newMain
	return k, nil
}

// consolidateGraph returns the single open head of g, consolidating via a
// contribution/head claim when g has several. The returned claims are the ones
// that consolidation newly created (empty when g was already single-headed).
func (s *Sequencer) consolidateGraph(ctx context.Context, g ranke.Graph) (ranke.Id, []ranke.Claim, error) {
	if g.IsConsolidated() {
		return g.Heads()[0], nil, nil
	}
	head, err := g.Consolidate(ctx, s.self, s.clock.Tick())
	if err != nil {
		return nil, nil, fmt.Errorf("dev.Sequencer: consolidate: %w", err)
	}
	return head.ID(), []ranke.Claim{head}, nil
}

// consolidateHeads builds a contribution/head claim wrapping the given heads —
// the same construction Graph.Consolidate performs, but over heads that live
// in 𝒰 rather than in one in-memory graph.
func (s *Sequencer) consolidateHeads(heads ...ranke.Id) (ranke.Claim, error) {
	edges := make([]ranke.Edge, 0, len(heads))
	for _, h := range heads {
		e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: h, Type: ranke.EdgeTypeHead})
		if err != nil {
			return nil, fmt.Errorf("dev.Sequencer: head edge: %w", err)
		}
		edges = append(edges, e)
	}
	return ranke.NewClaim(ranke.NodeHead, s.self).
		WithEdges(edges...).
		WithCreatedAt(s.clock.Tick()).
		Sign()
}

// mintBranchTable builds and stores a contribution/branches claim — the
// archive head. With a nil mainHead it is the empty table (bootstrap);
// otherwise it names the single "main" branch pointing at mainHead. Returns
// its id (the new archive head k). The table is restated in full each time
// (no contribution/diff overlay yet — a corner for later).
func (s *Sequencer) mintBranchTable(ctx context.Context, mainHead ranke.Id) (ranke.Id, error) {
	b := ranke.NewClaim(ranke.NodeBranches, s.self).WithCreatedAt(s.clock.Tick())
	if mainHead != nil {
		e, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference: mainHead,
			Type:      ranke.EdgeTypeBranch,
			Fields:    map[string]string{ranke.FieldName: mainBranch},
		})
		if err != nil {
			return nil, fmt.Errorf("dev.Sequencer: branch edge: %w", err)
		}
		b = b.WithEdges(e)
	}
	table, err := b.Sign()
	if err != nil {
		return nil, fmt.Errorf("dev.Sequencer: mint branch table: %w", err)
	}
	if err := s.u.PutClaims(ctx, []ranke.Claim{table}); err != nil {
		return nil, fmt.Errorf("dev.Sequencer: store branch table: %w", err)
	}
	return table.ID(), nil
}
