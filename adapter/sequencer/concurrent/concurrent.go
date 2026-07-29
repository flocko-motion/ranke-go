// package: adapter/sequencer/concurrent / adapter
// type:    adapter
// job:     the concurrent Sequencer — the paper's six steps with 2–5 run in parallel off the sequencing thread, and step 6 a serialised group commit folding a whole batch of contributions into ONE branch-table advance
// limits:  single-process (the sequencing thread is a mutex, not a consensus protocol); holds its committed-id set in memory, so that set grows with the archive; does NOT propagate changes between branches (paper 2's cross-branch merge) and mints no limiting/expiry claims
package concurrent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/flocko-motion/ranke-go"
)

var (
	errNilArg              = errors.New("concurrent.NewSequencer: nil argument")
	errNoSigningKey        = errors.New("concurrent.NewSequencer: self contributor carries no signing key")
	errNewSequencer        = errors.New("concurrent.NewSequencer")
	errSequencer           = errors.New("concurrent.Sequencer")
	errForeignContribution = errors.New("concurrent.Sequencer.Merge: contribution was not opened by this Sequencer")
)

// Clock stamps the claims the Sequencer mints. A local interface, so this adapter
// depends only on ranke; every Tick happens on the sequencing thread.
type Clock interface {
	Tick() time.Time
}

// Sequencer is the concurrent Ranke-Archive write path (RankeDB §Sequencer).
// Step 6 folds against the branch's LIVE head, not the base k a contribution
// opened at, so two contributions opened at one k both survive: the second
// consolidates the first's head alongside its own.
type Sequencer struct {
	u     ranke.Universe
	hist  ranke.History
	self  ranke.Contributor
	clock Clock

	// seq IS the sequencing thread: steps 1 and 6 hold it, as does every
	// clock.Tick, which is what makes an unsynchronised Clock safe.
	seq   sync.Mutex
	head  ranke.Id            // current archive head k (a contribution/branches claim)
	heads map[string]ranke.Id // current consolidated head per branch name

	// queue holds persisted contributions waiting for step 6. Merge enqueues then
	// races for seq; the winner drains the whole queue as one advance.
	qmu   sync.Mutex
	queue []*pending

	// committed is every claim id this Sequencer merged. Step 4 prunes its walk
	// there (ranke.WithTrusted), a merged claim being verified and immutable.
	cmu       sync.RWMutex
	committed map[string]struct{}
}

var _ ranke.Sequencer = (*Sequencer)(nil)

// pending is one persisted contribution queued for step 6 — the heads it
// contributes per branch, plus the slot the commit writes its outcome into.
type pending struct {
	heads map[string][]ranke.Id
	ids   []ranke.Id

	done chan struct{}
	head ranke.Id // the archive head k′ the commit advanced to
	err  error
}

// receipt is the outcome of a committed merge — the head the archive advanced
// to. Every contribution in one group commit receives the same head.
type receipt struct{ head ranke.Id }

// Head returns the archive head the merge advanced to.
func (r receipt) Head() ranke.Id { return r.head }

// NewSequencer bootstraps a fresh archive over u: it stores self and mints the
// empty branch table whose id is the archive head k₀ (foundation §Ranke-Archive).
// self must carry a signing key, which every branch table it mints is signed with.
func NewSequencer(ctx context.Context, u ranke.Universe, hist ranke.History, self ranke.Contributor, clock Clock) (*Sequencer, error) {
	if u == nil || hist == nil || self == nil || clock == nil {
		return nil, errNilArg
	}
	if self.SigningKey() == nil {
		return nil, errNoSigningKey
	}
	s := &Sequencer{
		u: u, hist: hist, self: self, clock: clock,
		heads:     map[string]ranke.Id{},
		committed: map[string]struct{}{},
	}

	// Stored so the branch-table claims attributed to it resolve.
	if err := s.u.PutClaims(ctx, []ranke.Claim{self}); err != nil {
		return nil, fmt.Errorf("%w: store contributor: %w", errNewSequencer, err)
	}
	// Empty branch table → archive head k₀, revision 0.
	bt0, err := s.mintBranchTable(ctx, nil, nil)
	if err != nil {
		return nil, err
	}
	if _, err := s.hist.Append(ctx, bt0.ID(), int(bt0.Node().Height()), 0); err != nil {
		return nil, fmt.Errorf("%w: append history: %w", errNewSequencer, err)
	}
	s.head = bt0.ID()
	s.markCommitted(self.ID(), bt0.ID())
	return s, nil
}

// GetContributor returns the contributor branch advances are signed with.
func (s *Sequencer) GetContributor() ranke.Contributor { return s.self }

// Head returns the current archive head k.
func (s *Sequencer) Head() ranke.Id {
	s.seq.Lock()
	defer s.seq.Unlock()
	return s.head
}

// GetArchive returns the immutable snapshot RA_k at the current head, pinned to
// the head as read — so it is safe while contributions are in flight.
func (s *Sequencer) GetArchive(ctx context.Context) (ranke.Archive, error) {
	return ranke.NewArchive(ctx, s.u, s.Head())
}

// NewContribution is step 1 and the only work the sequencing thread does per
// writer: it captures (k, t) for a contribution whose claims name their branches.
func (s *Sequencer) NewContribution(ctx context.Context) (ranke.Contribution, error) {
	s.seq.Lock()
	defer s.seq.Unlock()
	return &contribution{
		s:        s,
		baseHead: s.head,
		baseTime: s.clock.Tick(),
		staged:   map[string][]ranke.Claim{},
		adopted:  map[string][]ranke.Id{},
	}, nil
}

// Merge is step 6: it queues the contribution, then races the other writers for
// the sequencing thread. Enqueueing strictly before contending for the lock
// strands nothing — a caller holding it finds its entry committed or in its batch.
func (s *Sequencer) Merge(ctx context.Context, mc ranke.MergableContribution) (ranke.Receipt, error) {
	m, ok := mc.(*mergable)
	if !ok || m.s != s {
		return nil, errForeignContribution
	}
	// The queue entry takes its own map of the branches the contribution named.
	heads := make(map[string][]ranke.Id, len(m.branches))
	for _, b := range m.branches {
		heads[b] = m.heads[b]
	}
	p := &pending{heads: heads, ids: m.ids, done: make(chan struct{})}

	s.qmu.Lock()
	s.queue = append(s.queue, p)
	s.qmu.Unlock()

	s.drain(ctx)

	<-p.done
	if p.err != nil {
		return nil, p.err
	}
	return receipt{head: p.head}, nil
}

// drain takes the sequencing thread, empties the merge queue, and commits the
// batch as one advance under the draining caller's ctx, one outcome for all.
// An empty queue means another caller took this entry.
func (s *Sequencer) drain(ctx context.Context) {
	s.seq.Lock()
	defer s.seq.Unlock()

	s.qmu.Lock()
	batch := s.queue
	s.queue = nil
	s.qmu.Unlock()

	if len(batch) == 0 {
		return
	}
	err := s.commit(ctx, batch)
	for _, p := range batch {
		if err != nil {
			p.err = err
		} else {
			p.head = s.head
		}
		close(p.done)
	}
}

// commit performs step 6 for a batch: fold each touched branch into a new head,
// mint ONE branch table restating them, append to history, then publish — the
// mutation coming last, so a failure changes nothing.
func (s *Sequencer) commit(ctx context.Context, batch []*pending) error {
	byBranch := map[string][]ranke.Id{}
	for _, p := range batch {
		for b, hs := range p.heads {
			byBranch[b] = append(byBranch[b], hs...)
		}
	}
	changed := make([]string, 0, len(byBranch))
	for b := range byBranch {
		changed = append(changed, b)
	}
	sort.Strings(changed) // a deterministic branch order → a deterministic table id

	newHeads := make(map[string]ranke.Id, len(changed))
	for _, b := range changed {
		h, err := s.foldBranch(ctx, b, byBranch[b])
		if err != nil {
			return err
		}
		newHeads[b] = h
	}

	bt, err := s.mintBranchTable(ctx, changed, newHeads)
	if err != nil {
		return err
	}
	revision, err := s.hist.Len(ctx)
	if err != nil {
		return fmt.Errorf("%w: history length: %w", errSequencer, err)
	}
	if _, err := s.hist.Append(ctx, bt.ID(), int(bt.Node().Height()), revision); err != nil {
		return fmt.Errorf("%w: append history: %w", errSequencer, err)
	}

	for b, h := range newHeads {
		s.heads[b] = h
		s.markCommitted(h)
	}
	s.head = bt.ID()
	s.markCommitted(bt.ID())
	for _, p := range batch {
		s.markCommitted(p.ids...)
		for _, hs := range p.heads { // the per-branch heads step 4 consolidated
			s.markCommitted(hs...)
		}
	}
	return nil
}

// foldBranch computes a branch's new head from its CURRENT head plus the heads the
// batch contributes, deduplicated and ordered so one set yields one claim.
func (s *Sequencer) foldBranch(ctx context.Context, branch string, contributed []ranke.Id) (ranke.Id, error) {
	set := map[string]ranke.Id{}
	if prior, ok := s.heads[branch]; ok {
		set[prior.String()] = prior
	}
	for _, h := range contributed {
		if h != nil {
			set[h.String()] = h
		}
	}
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: branch %q advanced with no heads", errSequencer, branch)
	}
	if len(keys) == 1 {
		return set[keys[0]], nil
	}

	edges := make([]ranke.Edge, 0, len(keys))
	for _, k := range keys {
		e, err := ranke.NewEdge(ranke.EdgeConfig{Reference: set[k], Type: ranke.EdgeTypeHead})
		if err != nil {
			return nil, fmt.Errorf("%w: head edge: %w", errSequencer, err)
		}
		edges = append(edges, e)
	}
	// The folded heads and self are in 𝒰, so WithAutoHeight resolves height (§4.1).
	head, err := ranke.NewClaim(ranke.NodeHead, s.self).
		WithEdges(edges...).
		WithCreatedAt(s.clock.Tick()).
		WithAutoHeight(ctx, s.u).
		Sign()
	if err != nil {
		return nil, fmt.Errorf("%w: fold heads: %w", errSequencer, err)
	}
	if err := s.u.PutClaims(ctx, []ranke.Claim{head}); err != nil {
		return nil, fmt.Errorf("%w: store folded head: %w", errSequencer, err)
	}
	return head.ID(), nil
}

// mintBranchTable stores the next contribution/branches claim, the new archive
// head: past the bootstrap a contribution/diff over the previous table restating
// only the advanced branches, which keeps every prior table in the head's
// provenance — the spine (§Archive, §Branches).
func (s *Sequencer) mintBranchTable(ctx context.Context, changed []string, heads map[string]ranke.Id) (ranke.Claim, error) {
	b := ranke.NewClaim(ranke.NodeBranches, s.self).WithCreatedAt(s.clock.Tick())
	if s.head != nil {
		b = b.WithDiff(s.head) // diff over the previous table — build the spine
	}
	for _, name := range changed {
		// The branch edge is named (its branch name), as diff-claim edges must be.
		e, err := ranke.NewEdge(ranke.EdgeConfig{
			Reference: heads[name],
			Type:      ranke.EdgeTypeBranch,
			Fields:    map[string]string{ranke.FieldName: name},
		})
		if err != nil {
			return nil, fmt.Errorf("%w: branch edge %q: %w", errSequencer, name, err)
		}
		b = b.WithEdges(e)
	}
	// self, the previous table, and the branch heads are in 𝒰 (§4.1).
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

// isCommitted reports whether this Sequencer merged id — the predicate step 4
// hands to ranke.WithTrusted. Read-locked: every in-flight verification calls it.
func (s *Sequencer) isCommitted(id ranke.Id) bool {
	if id == nil {
		return false
	}
	s.cmu.RLock()
	defer s.cmu.RUnlock()
	_, ok := s.committed[id.String()]
	return ok
}

// markCommitted records ids as merged, so later contributions prune their walks
// at them.
func (s *Sequencer) markCommitted(ids ...ranke.Id) {
	s.cmu.Lock()
	defer s.cmu.Unlock()
	for _, id := range ids {
		if id != nil {
			s.committed[id.String()] = struct{}{}
		}
	}
}
