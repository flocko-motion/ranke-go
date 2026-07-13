// package: ranke / sequencer
// type:    logic
// job:     the sole writer of a Ranke-Archive — owns B_h, hands out immutable read snapshots, and commits verified contributions serially
// limits:  verifies nothing itself (-> archive.Prepare); persists no bytes of its own beyond B_h (-> universe, branch_table_head)
package ranke

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrContributionStale is returned by Submit when a limiting claim
// landed after the proof was prepared, so the contribution's validity
// can no longer be trusted without re-verification (the dirty path).
// ranke-db surfaces it as HTTP 409. It never fires today: no
// validity-non-monotonic semantics are enforced yet (see
// containsLimitingClaim), so lastLimitingHeight never advances.
var ErrContributionStale = errors.New("ranke.Sequencer.Submit: contribution stale — a limiting claim intervened")

// Sequencer is the single writer of a Ranke-Archive (spec §4.7, §4.8).
type Sequencer interface {
	// GetArchive returns an immutable read snapshot pinned at the
	// current height.
	GetArchive(ctx context.Context) (Archive, error)

	GetContributor() Contributor

	NewContribution(ctx context.Context) (Contribution, error)

	// MergeContribution merges a completed, verified, persisted contribution to the sequencer.
	MergeContribution(ctx context.Context, contribution VerifiedContribution) (Receipt, error)
}

type sequencer struct {
	u    Universe
	self Contributor

	mu     sync.Mutex
	table  BranchTable // authoritative current table (guarded by mu)
	height Height      // monotone commit counter (guarded by mu)
	// lastLimitingHeight is the height at which the most recent limiting
	// claim committed. A proof prepared at H < lastLimitingHeight cannot
	// be trusted on the clean path. Advances only when a limiting claim
	// commits — which cannot happen yet (see containsLimitingClaim).
	lastLimitingHeight Height
}

type mergeRun struct {
	// TOOD: implement
}

func (s *sequencer) GetArchive(ctx context.Context) (Archive, Height, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// table is immutable (Set returns a new one), so sharing the pointer
	// with the snapshot is safe: a later Submit reassigns s.table without
	// disturbing this snapshot.
	return newArchiveSnapshot(s.u, s.table, s.height), s.height, nil
}

func (s *sequencer) MergeContribution(ctx context.Context, contribution VerifiedContribution) (MergeRun, error) {
	sp, ok := proof.(*sealedProof)
	if !ok || sp == nil {
		return nil, errors.New("ranke.Sequencer.Submit: foreign or nil proof")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Staleness: did a limiting claim land after the proof was prepared?
	// If so the delta's validity is no longer implied by the prepare-time
	// verification and the dirty path (re-verify the non-monotonic slice
	// vs the current head) is required. That path is not implemented —
	// it is dead until expiry/revocation land in verifyOne — so we reject
	// conservatively. This branch never executes today.
	if !sp.containsLimiting && s.lastLimitingHeight > sp.height {
		return nil, fmt.Errorf("%w (prepared at height %d, limiting claim at %d)", ErrContributionStale, sp.height, s.lastLimitingHeight)
	}

	// Detect a concurrent branch advance since the proof was prepared.
	var currentHead Id
	if s.table.Has(sp.branch) {
		b, err := s.table.Get(ctx, sp.branch)
		if err != nil {
			return nil, fmt.Errorf("ranke.Sequencer.Submit: %w", err)
		}
		currentHead = b.Latest().Head()
	}

	at := sp.createdAt
	if at.IsZero() {
		at = time.Now().UTC()
	}
	bindHead := sp.head
	if currentHead != nil && !idEqual(currentHead, sp.priorHead) {
		// The branch moved (or was created) under this contribution.
		// Both sides are independently verified and additive, so merge
		// them into one server-attested contribution/head — no re-verify
		// (the monotonicity law). Serial Submit always merges 2 → 1.
		merged, err := s.consolidateHeads(ctx, currentHead, sp.head, at)
		if err != nil {
			return nil, fmt.Errorf("ranke.Sequencer.Submit: consolidate moved head: %w", err)
		}
		bindHead = merged
	}

	nextTable, err := s.table.Set(ctx, sp.branch, bindHead, s.self, at)
	if err != nil {
		return nil, fmt.Errorf("ranke.Sequencer.Submit: mint branch table: %w", err)
	}
	if err := s.bth.Save(ctx, nextTable.Bh()); err != nil {
		return nil, fmt.Errorf("ranke.Sequencer.Submit: persist B_h: %w", err)
	}

	s.table = nextTable
	s.height++
	if sp.containsLimiting {
		s.lastLimitingHeight = s.height
	}
	return &receipt{height: s.height, branch: sp.branch, head: bindHead}, nil
}

// consolidateHeads builds a server-attested contribution/head that wraps
// two existing heads and persists it, returning its id. Both heads are
// already in 𝒰 (committed, or absorbed by Prepare), so this only mints
// the wrapper — no content is re-verified.
func (s *sequencer) consolidateHeads(ctx context.Context, a, b Id, at time.Time) (Id, error) {
	edges := make([]Edge, 0, 2)
	for _, h := range []Id{a, b} {
		e, err := NewEdge(EdgeConfig{
			Reference: h,
			TypeClass: EdgeContribution,
			TypeSub:   "head",
		})
		if err != nil {
			return nil, fmt.Errorf("build head edge: %w", err)
		}
		edges = append(edges, e)
	}
	headClaim, err := ClaimBuilder{
		Type:        NodeHead,
		Contributor: s.self,
		Edges:       edges,
		CreatedAt:   at,
	}.Sign()
	if err != nil {
		return nil, fmt.Errorf("build head claim: %w", err)
	}
	if err := PutClaim(ctx, s.u, headClaim); err != nil {
		return nil, fmt.Errorf("absorb head claim: %w", err)
	}
	return headClaim.ID(), nil
}

func (s *sequencer) Close() error {
	return s.bth.Close()
}

// --- Deprecated convenience shims ---
//
// AddGraph and AddClaim reproduce the pre-split one-call write on the
// Sequencer by running the Prepare → Submit pipeline internally. They
// keep existing callers compiling; new code should use GetArchive +
// Prepare + Submit directly. To be dropped a release after the split.

// AddGraph commits g to a branch in one call.
//
// Deprecated: use GetArchive + Archive.Prepare + Submit.
func (s *sequencer) AddGraph(ctx context.Context, branchName string, g Graph, contributor Contributor, createdAt ...time.Time) error {
	arch, _, err := s.GetArchive(ctx)
	if err != nil {
		return err
	}
	proof, err := arch.Prepare(ctx, branchName, g, contributor, createdAt...)
	if err != nil {
		return err
	}
	_, err = s.Submit(ctx, proof)
	return err
}

// AddClaim appends a single claim to a branch in one call, loading the
// branch's current closure first.
//
// Deprecated: use GetArchive + Archive.Prepare + Submit.
func (s *sequencer) AddClaim(ctx context.Context, branchName string, c Claim, createdAt ...time.Time) error {
	if c == nil {
		return errors.New("ranke.Sequencer.AddClaim: nil claim")
	}
	contributor := c.Contributor()
	if contributor == nil {
		return errors.New("ranke.Sequencer.AddClaim: claim has no contributor")
	}
	arch, _, err := s.GetArchive(ctx)
	if err != nil {
		return err
	}
	var g Graph
	if arch.HasBranch(ctx, branchName) {
		b, err := arch.GetBranch(ctx, branchName)
		if err != nil {
			return fmt.Errorf("ranke.Sequencer.AddClaim: %w", err)
		}
		head, err := arch.GetClaim(ctx, b.Latest().Head())
		if err != nil {
			return fmt.Errorf("ranke.Sequencer.AddClaim: load branch head: %w", err)
		}
		g, err = head.Graph(ctx)
		if err != nil {
			return fmt.Errorf("ranke.Sequencer.AddClaim: materialize branch closure: %w", err)
		}
	} else {
		g = NewGraph(contributor)
	}
	if err := g.Add(c); err != nil {
		return fmt.Errorf("ranke.Sequencer.AddClaim: %w", err)
	}
	proof, err := arch.Prepare(ctx, branchName, g, contributor, createdAt...)
	if err != nil {
		return err
	}
	_, err = s.Submit(ctx, proof)
	return err
}

// idEqual compares two ids, treating two nils as equal.
func idEqual(a, b Id) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(b)
}
