// package: adapter/sequencer/dev / testkit
// type:    adapter
// job:     a blocking, single-threaded reference Sequencer for tests and development — the sole writer that advances a Ranke-Archive by driving the paper's six steps one contribution at a time
// limits:  blocking and single-threaded — every step runs inline, one contribution at a time; manages named branches without propagating between them (paper 2's cross-branch merge); mints from the injected Clock, so heads are deterministic. NOT for production (-> adapter/sequencer/concurrent).
package dev

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flocko-motion/ranke-go"
)

var (
	errNilArg            = errors.New("dev.NewSequencer: nil argument")
	errNoSigningKey      = errors.New("dev.NewSequencer: self contributor carries no signing key")
	errNewSequencer      = errors.New("dev.NewSequencer")
	errSequencer         = errors.New("dev.Sequencer")
	errSealed            = errors.New("dev.Contribution: already sealed")
	errNoBranch          = errors.New("dev.Contribution: claims must name the branch they join")
	errNilClaim          = errors.New("dev.Contribution.AddClaims: nil claim")
	errNilGraph          = errors.New("dev.Contribution.AddGraph: nil graph")
	errEmptyContribution = errors.New("dev.Contribution.CompleteAndVerify: nothing was added")
	errForeign           = errors.New("dev.Sequencer.Merge: contribution came from another Sequencer")
)

var _ ranke.Sequencer = (*Sequencer)(nil)

// Clock is the deterministic time source for the claims the Sequencer mints,
// local to this adapter so it depends only on ranke. Tick returns now, then
// advances; one Clock shared with the caller keeps a whole run monotonic.
type Clock interface {
	Tick() time.Time
}

// Sequencer is the reference Ranke-Archive write path (RankeDB §Sequencer): the
// single writer advancing the head k → k′ by merging a contribution, running the
// six steps serially in one blocking AddClaims call.
type Sequencer struct {
	u     ranke.Universe
	hist  ranke.History
	self  ranke.Contributor
	clock Clock

	head  ranke.Id            // current archive head k (a contribution/branches claim)
	heads map[string]ranke.Id // current consolidated head per branch name (empty until first add)
}

// NewSequencer bootstraps a fresh archive over u, storing the key-carrying self
// contributor and minting the empty contribution/branches claim whose id is the
// archive head k₀ (foundation §Ranke-Archive), recorded in history.
func NewSequencer(ctx context.Context, u ranke.Universe, hist ranke.History, self ranke.Contributor, clock Clock) (*Sequencer, error) {
	if u == nil || hist == nil || self == nil || clock == nil {
		return nil, errNilArg
	}
	if self.SigningKey() == nil {
		return nil, errNoSigningKey
	}
	s := &Sequencer{u: u, hist: hist, self: self, clock: clock, heads: map[string]ranke.Id{}}

	// Store the self contributor so branch-table claims attributed to it resolve.
	if err := s.u.PutClaims(ctx, []ranke.Claim{self}); err != nil {
		return nil, fmt.Errorf("%w: store contributor: %w", errNewSequencer, err)
	}
	// Empty branch table → archive head k₀, revision 0.
	bt0, err := s.mintBranchTable(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := s.hist.Append(ctx, bt0.ID(), int(bt0.Node().Height()), 0); err != nil {
		return nil, fmt.Errorf("%w: append history: %w", errNewSequencer, err)
	}
	s.head = bt0.ID()
	return s, nil
}

// GetContributor returns the contributor the Sequencer signs branch advances with.
func (s *Sequencer) GetContributor() ranke.Contributor { return s.self }

// Head returns the current archive head k.
func (s *Sequencer) Head() ranke.Id { return s.head }

// GetArchive returns the immutable snapshot RA_k at the current head.
func (s *Sequencer) GetArchive(ctx context.Context) (ranke.Archive, error) {
	return ranke.NewArchive(ctx, s.u, s.head)
}

// NewContribution is step 1: it captures the base (k, t) and returns a
// contribution to fill, whose claims name the branches they join.
func (s *Sequencer) NewContribution(_ context.Context) (ranke.Contribution, error) {
	return &contribution{
		s:        s,
		baseHead: s.head,
		baseTime: s.clock.Tick(),
		staged:   map[string][]ranke.Claim{},
		adopted:  map[string][]ranke.Id{},
	}, nil
}

// Merge is step 6: it folds each named branch's head in, mints one branch table
// restating them all, records the revision, and publishes the new archive head.
func (s *Sequencer) Merge(ctx context.Context, mc ranke.MergableContribution) (ranke.Receipt, error) {
	m, ok := mc.(*mergable)
	if !ok || m.s != s {
		return nil, errForeign
	}

	for _, branch := range m.branches {
		// Fold the branch's previous head in so its closure accumulates across merges.
		folded := m.heads[branch]
		if prior, ok := s.heads[branch]; ok {
			folded = append(append([]ranke.Id{}, folded...), prior)
		}
		newHead := folded[0]
		if len(folded) > 1 {
			hc, err := s.consolidateHeads(ctx, folded...)
			if err != nil {
				return nil, err
			}
			if err := s.u.PutClaims(ctx, []ranke.Claim{hc}); err != nil {
				return nil, fmt.Errorf("%w: fold heads: %w", errSequencer, err)
			}
			newHead = hc.ID()
		}
		s.heads[branch] = newHead
	}

	bt, err := s.mintBranchTable(ctx, m.branches)
	if err != nil {
		return nil, err
	}
	revision, err := s.hist.Len(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: history length: %w", errSequencer, err)
	}
	if _, err := s.hist.Append(ctx, bt.ID(), int(bt.Node().Height()), revision); err != nil {
		return nil, fmt.Errorf("%w: append history: %w", errSequencer, err)
	}
	s.head = bt.ID()
	// The head advanced: signal storage to refresh its query accelerators. What a
	// layer indexes, and how, is the layer's own business.
	if err := s.u.Tag(ctx, s.head); err != nil {
		return nil, fmt.Errorf("%w: tag: %w", errSequencer, err)
	}
	return receipt{head: s.head}, nil
}

// consolidateGraph returns g's single open head, folding several into one via a
// contribution/head claim added through the graph.
func (s *Sequencer) consolidateGraph(ctx context.Context, g ranke.Graph) (ranke.Id, error) {
	if g.IsConsolidated() {
		return g.Heads()[0], nil
	}
	head, err := g.Consolidate(ctx, s.self, s.clock.Tick())
	if err != nil {
		return nil, fmt.Errorf("%w: consolidate: %w", errSequencer, err)
	}
	return head.ID(), nil
}

// consolidateHeads builds a contribution/head claim over heads living in 𝒰 —
// the construction Graph.Consolidate performs for one in-memory graph.
func (s *Sequencer) consolidateHeads(ctx context.Context, heads ...ranke.Id) (ranke.Claim, error) {
	edges := make([]ranke.Edge, 0, len(heads))
	for _, h := range heads {
		e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: h, Type: ranke.EdgeTypeHead})
		if err != nil {
			return nil, fmt.Errorf("%w: head edge: %w", errSequencer, err)
		}
		edges = append(edges, e)
	}
	// The heads and self contributor live in 𝒰, so WithAutoHeight resolves the
	// new head's height (§4.1) from their committed heights.
	return ranke.NewClaim(ranke.NodeHead, s.self).
		WithEdges(edges...).
		WithCreatedAt(s.clock.Tick()).
		WithAutoHeight(ctx, s.u).
		Sign()
}

// mintBranchTable builds and stores the next contribution/branches claim, whose
// id is the new archive head k: a diff over the previous table restating ONLY the
// changed branches, so the others are inherited by overlaying the chain (§Branches)
// and the prior tables stay in the head's provenance — the spine (§Archive).
func (s *Sequencer) mintBranchTable(ctx context.Context, changed []string) (ranke.Claim, error) {
	b := ranke.NewClaim(ranke.NodeBranches, s.self).WithCreatedAt(s.clock.Tick())
	if s.head != nil {
		b = b.WithDiff(s.head) // diff over the previous table — build the spine
	}
	for _, branch := range changed {
		// Restate each advanced branch, its edge named as diff edges must be.
		e, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference: s.heads[branch],
			Type:      ranke.EdgeTypeBranch,
			Fields:    map[string]string{ranke.FieldName: branch},
		})
		if err != nil {
			return nil, fmt.Errorf("%w: branch edge: %w", errSequencer, err)
		}
		b = b.WithEdges(e)
	}
	// Contributor, previous table and branch head are in 𝒰; resolve height (§4.1).
	b = b.WithAutoHeight(ctx, s.u)
	table, err := b.Sign()
	if err != nil {
		return nil, fmt.Errorf("%w: mint branch table: %w", errSequencer, err)
	}
	if err := s.u.PutClaims(ctx, []ranke.Claim{table}); err != nil {
		return nil, fmt.Errorf("%w: store branch table: %w", errSequencer, err)
	}
	return table, nil
}
