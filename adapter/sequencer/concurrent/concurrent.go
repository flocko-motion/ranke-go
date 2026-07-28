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
	errNoBranch            = errors.New("concurrent.Sequencer: empty branch name")
	errSequencer           = errors.New("concurrent.Sequencer")
	errForeignContribution = errors.New("concurrent.Sequencer.Merge: contribution was not opened by this Sequencer")
)

// mainBranch is the branch NewContribution and AddClaims target when no branch
// is named (the ...ToBranch forms take an explicit one).
const mainBranch = "main"

// Clock is the time source the Sequencer stamps the claims it mints with
// (consolidating heads, branch tables). Kept a local interface so this adapter
// depends only on ranke — generator.Clock satisfies it. It need not be
// concurrency-safe: every Tick happens on the sequencing thread.
type Clock interface {
	Tick() time.Time
}

// Sequencer is the concurrent realisation of the Ranke-Archive write path
// (RankeDB §Sequencer). It splits the paper's six steps exactly where the paper
// says they split:
//
//	1 Opening    — sequencing thread, cheap: capture the base (k, t)
//	2–5 Adding / Completing / Verifying / Persisting — OFF the sequencing
//	             thread, any number in parallel, over immutable data
//	6 Merging    — sequencing thread, serialised, and BATCHED: one commit
//	             folds every contribution queued at that moment into a single
//	             branch-table claim, so N concurrent writers cost one archive
//	             revision rather than N
//
// The correctness argument for the concurrency is that step 6 folds against the
// branch's LIVE head, not against the base k the contribution was opened at. Two
// contributions opened at the same k both survive: whichever merges second
// consolidates the first's head in alongside its own, so the branch closure
// accumulates and no write is lost. The base (k, t) is only ever used to bound
// what step 2 admits and what step 4 has to re-walk — never to decide where the
// branch lands.
//
// Contrast the dev Sequencer (adapter/sequencer/dev), which runs all six steps
// serially inside one blocking call: correct, but one contribution at a time and
// one revision per contribution.
type Sequencer struct {
	u     ranke.Universe
	hist  ranke.History
	self  ranke.Contributor
	clock Clock

	// seq IS the sequencing thread. Steps 1 and 6 — the only two that read or
	// advance the head — hold it; steps 2–5 never do. Every clock.Tick also
	// happens under it, so an unsynchronised Clock is safe here.
	seq   sync.Mutex
	head  ranke.Id            // current archive head k (a contribution/branches claim)
	heads map[string]ranke.Id // current consolidated head per branch name

	// queue holds persisted contributions waiting for step 6. Merge enqueues,
	// then races for seq; whoever wins drains the whole queue and commits it as
	// one advance — the paper's "merge an arbitrary number of contributions in
	// a single operation". No background goroutine, so nothing to shut down.
	qmu   sync.Mutex
	queue []*pending

	// committed is every claim id this Sequencer has merged. Step 4 prunes its
	// walk there (ranke.WithTrusted): a merged claim was verified before it
	// landed and immutability keeps it valid, so re-walking it is pure waste.
	cmu       sync.RWMutex
	committed map[string]struct{}
}

var _ ranke.Sequencer = (*Sequencer)(nil)

// pending is one persisted contribution queued for step 6, plus the slot the
// group commit writes its outcome back into.
type pending struct {
	branch string
	heads  []ranke.Id
	ids    []ranke.Id

	done chan struct{}
	head ranke.Id // the archive head k′ the commit advanced to
	err  error
}

// receipt is the outcome of a committed merge — the head the archive advanced
// to. Every contribution in one group commit receives the same head.
type receipt struct{ head ranke.Id }

// Head returns the archive head the merge advanced to.
func (r receipt) Head() ranke.Id { return r.head }

// NewSequencer bootstraps a fresh archive over u: it stores the self
// contributor (an initial node) and mints an empty contribution/branches claim
// whose id is the archive head k₀ (foundation §Ranke-Archive), recorded in
// history. self must carry its signing key (built via AsContributor with a key,
// or WithSigningKey) so the Sequencer can sign the branch-table claims it mints.
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

	// The self contributor is an initial node — store it so the branch-table
	// claims attributed to it resolve.
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

// GetContributor returns the contributor the Sequencer signs branch advances
// with.
func (s *Sequencer) GetContributor() ranke.Contributor { return s.self }

// Head returns the current archive head k.
func (s *Sequencer) Head() ranke.Id {
	s.seq.Lock()
	defer s.seq.Unlock()
	return s.head
}

// GetArchive returns the immutable snapshot RA_k at the current head. Safe to
// call while contributions are in flight: the snapshot is pinned to the head as
// it was read, and nothing in the archive is ever mutated.
func (s *Sequencer) GetArchive(ctx context.Context) (ranke.Archive, error) {
	return ranke.NewArchive(ctx, s.u, s.Head())
}

// NewContribution opens a contribution against "main" — the branch-defaulted
// form of NewContributionToBranch.
func (s *Sequencer) NewContribution(ctx context.Context) (ranke.Contribution, error) {
	return s.NewContributionToBranch(ctx, mainBranch)
}

// NewContributionToBranch is step 1, opening: it captures the base (k, t) — the
// current head and the current time — and hands back a contribution to fill.
// This is the one cheap thing the sequencing thread does per contribution; the
// expensive steps that follow run entirely off it, so opening never queues
// behind another writer's verification.
func (s *Sequencer) NewContributionToBranch(ctx context.Context, branch string) (ranke.Contribution, error) {
	if branch == "" {
		return nil, errNoBranch
	}
	s.seq.Lock()
	defer s.seq.Unlock()
	return &contribution{s: s, branch: branch, baseHead: s.head, baseTime: s.clock.Tick()}, nil
}

// AddClaims advances "main" — the branch-defaulted form of AddClaimsToBranch.
func (s *Sequencer) AddClaims(ctx context.Context, claims []ranke.Claim) (ranke.Id, error) {
	return s.AddClaimsToBranch(ctx, mainBranch, claims)
}

// AddClaimsToBranch drives all six steps for one batch of claims — a sub-graph
// in topological order (a claim's references precede it) — and returns the new
// archive head k′. It is the convenience front door over NewContributionToBranch
// / CompleteAndVerify / Persist / Merge, and matches the dev Sequencer's
// signature so the two are interchangeable. Call it from many goroutines: only
// steps 1 and 6 serialise.
func (s *Sequencer) AddClaimsToBranch(ctx context.Context, branch string, claims []ranke.Claim) (ranke.Id, error) {
	if len(claims) == 0 {
		return s.Head(), nil
	}
	c, err := s.NewContributionToBranch(ctx, branch)
	if err != nil {
		return nil, err
	}
	if err := c.AddClaims(claims); err != nil {
		return nil, err
	}
	v, err := c.CompleteAndVerify(ctx)
	if err != nil {
		return nil, err
	}
	m, err := v.Persist(ctx)
	if err != nil {
		return nil, err
	}
	r, err := s.Merge(ctx, m)
	if err != nil {
		return nil, err
	}
	return r.Head(), nil
}

// Merge is step 6: it queues the persisted contribution and then races every
// other waiting writer for the sequencing thread. The winner drains the whole
// queue and commits it as a single branch-table advance; the losers find an
// empty queue and simply collect the head that commit produced. Enqueueing
// strictly before contending for the lock is what makes that safe — by the time
// a caller holds the lock its own entry is either already committed or in the
// batch it is about to drain, so no contribution can be stranded.
//
// Only a contribution this Sequencer opened can be merged: the branch it targets
// is not on the MergableContribution contract, and merging into the wrong
// archive would be silent corruption.
func (s *Sequencer) Merge(ctx context.Context, mc ranke.MergableContribution) (ranke.Receipt, error) {
	m, ok := mc.(*mergable)
	if !ok || m.s != s {
		return nil, errForeignContribution
	}
	p := &pending{branch: m.branch, heads: m.heads, ids: m.ids, done: make(chan struct{})}

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
// whole batch as one advance. An empty queue means another caller already
// committed this caller's entry — nothing to do.
//
// The batch shares one outcome: a commit failure fails every contribution in it.
// That is honest for the failures that can still occur here — minting, history,
// or storage — since all of them are infrastructural and would hit any batch
// composition equally. It does mean the draining caller's ctx governs the commit
// on everyone's behalf.
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

// commit performs step 6 for a whole batch, on the sequencing thread. It folds
// each touched branch's contributions into that branch's new head, mints ONE
// branch-table claim restating every branch the batch advanced, appends it to
// history, and only then publishes the new state.
//
// Nothing mutates the Sequencer until the last fallible operation has succeeded,
// so a failed commit leaves the head and the branch heads exactly where they
// were — the archive never publishes a state it could not record.
func (s *Sequencer) commit(ctx context.Context, batch []*pending) error {
	byBranch := map[string][]ranke.Id{}
	for _, p := range batch {
		byBranch[p.branch] = append(byBranch[p.branch], p.heads...)
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
	}
	return nil
}

// foldBranch computes a branch's new head from its CURRENT head plus every head
// the batch contributes to it. Folding the current head in is what makes
// concurrent writes safe: the branch's closure accumulates across commits, so a
// contribution opened against an older k cannot displace one that merged in the
// meantime.
//
// The fold set is deduplicated (content addressing means two contributions can
// legitimately produce the same head) and ordered, so the same set always yields
// the same consolidating claim. A single-element set needs no wrapper — that
// head IS the branch head, consolidate(RG) = RG.
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
	// The folded heads and the self contributor are all in 𝒰, so WithAutoHeight
	// resolves the new head's height (§4.1) from their committed heights.
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

// mintBranchTable builds and stores the next contribution/branches claim — the
// new archive head. Except for the bootstrap it is a contribution/diff over the
// previous table (s.head), restating ONLY the branches this commit advanced; the
// rest are inherited by overlaying the diff chain back to the initial empty table
// (§Branches). So the prior branch tables stay in the head's provenance — the
// spine (§Archive) — reachable and taggable, rather than each revision orphaning
// the last.
//
// Restating several branches at once is what lets one commit serve a batch that
// touched more than one of them: the table is a set of named edges, so N branches
// cost N edges on one claim, not N revisions. Bootstrap (s.head nil, no changed
// branches) is the empty table.
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
	// The self contributor, the previous table, and the branch heads are in 𝒰;
	// resolve height from them (§4.1).
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

// isCommitted reports whether id has already been merged by this Sequencer —
// the prune predicate step 4 hands to ranke.WithTrusted. Read-locked, because it
// is called from every in-flight verification at once.
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
