package ranke

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// hitem builds a HistoryItem whose id is derived from tag (so replaced entries
// are distinguishable) at the given revision.
func hitem(t *testing.T, tag string, revision int) HistoryItem {
	t.Helper()
	id, err := HashContent([]byte(tag))
	require.NoError(t, err)
	return NewHistoryItem(id, revision, revision, time.Unix(int64(revision), 0).UTC())
}

func revsOf(items []HistoryItem) []int {
	out := make([]int, len(items))
	for i, it := range items {
		out[i] = it.GetRevision()
	}
	return out
}

// TestSpliceHistory covers the graft points: a plain forward advance (nothing
// dropped), a re-tag from a middle revision (superseded tail replaced), a full
// retag from 0, and the empty edge cases.
func TestSpliceHistory(t *testing.T) {
	base := []HistoryItem{hitem(t, "a", 0), hitem(t, "b", 1), hitem(t, "c", 2), hitem(t, "d", 3)}

	// Forward advance: tagged begins one past the end, so nothing is dropped.
	fwd := SpliceHistory(base, []HistoryItem{hitem(t, "e", 4)})
	require.Equal(t, []int{0, 1, 2, 3, 4}, revsOf(fwd))

	// Re-tag from revision 2: keep 0,1; replace 2.. with the re-tagged items.
	retag := SpliceHistory(base, []HistoryItem{hitem(t, "C2", 2), hitem(t, "D2", 3), hitem(t, "E2", 4)})
	require.Equal(t, []int{0, 1, 2, 3, 4}, revsOf(retag))
	require.True(t, retag[2].GetId().Equal(hitem(t, "C2", 2).GetId()), "revision 2 replaced by the re-tagged item")
	require.True(t, retag[1].GetId().Equal(hitem(t, "b", 1).GetId()), "revision 1 kept from the base")

	// Full retag from 0: everything replaced.
	full := SpliceHistory(base, []HistoryItem{hitem(t, "x", 0), hitem(t, "y", 1)})
	require.Equal(t, []int{0, 1}, revsOf(full))
	require.True(t, full[0].GetId().Equal(hitem(t, "x", 0).GetId()))

	// Empty tagged leaves the base untouched; empty base yields tagged as-is.
	require.Equal(t, revsOf(base), revsOf(SpliceHistory(base, nil)))
	require.Equal(t, []int{4}, revsOf(SpliceHistory(nil, []HistoryItem{hitem(t, "z", 4)})))
}

// --- History: Append, Latest, the search, and the two integrity checks
// V-IDSEQVERIFY and V-SIG give a fetched entry (history.go). ---

// historyHead builds a minimal (empty) branch table dated at, standing in for
// the recorded head — the shape ruleBranchTableReference's carve-out and a
// fresh archive's k₀ both take.
func historyHead(t *testing.T, self Contributor, at time.Time) Claim {
	t.Helper()
	c, err := NewClaim(NodeBranches, self).WithCreatedAt(at).WithHeight(HeightOf(self)).Sign()
	require.NoError(t, err)
	return c
}

// appendN appends n entries to h under self, each dated strictly after the
// head it records (`V-MONO`) and strictly before the next, and returns the
// recorded head ids in revision order.
func appendN(t *testing.T, ctx context.Context, u Universe, h *History, self Contributor, base time.Time, n int) []Id {
	t.Helper()
	ids := make([]Id, n)
	for i := range n {
		head := historyHead(t, self, base.Add(time.Duration(2*i)*time.Second))
		putClaims(t, u, head)
		item, err := h.Append(ctx, self, head.ID(), int(head.Node().Height()), base.Add(time.Duration(2*i+1)*time.Second))
		require.NoError(t, err)
		require.Equal(t, i, item.GetRevision(), "Append derives the next revision itself, never taking one from the caller")
		ids[i] = head.ID()
	}
	return ids
}

// TestHistoryAppendAndLatest is the basic round trip: what Append writes,
// Latest/GetAtRevision/Len read back — both on the writer's own (cached)
// instance and on an independent reader that must search for it.
func TestHistoryAppendAndLatest(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)

	h := OpenHistory(u, "")
	ids := appendN(t, ctx, u, h, self, time.Now(), 3)

	n, err := h.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, n)

	latest, err := h.Latest(ctx)
	require.NoError(t, err)
	require.True(t, latest.GetId().Equal(ids[2]))
	require.Equal(t, 2, latest.GetRevision())

	r := OpenHistory(u, h.Seed())
	rn, err := r.Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 3, rn, "an independent reader must search to the writer's own answer")
	for i, id := range ids {
		item, err := r.GetAtRevision(ctx, i)
		require.NoError(t, err)
		require.True(t, item.GetId().Equal(id), "revision %d", i)
	}
}

// TestHistoryLatestSearchBoundaries exercises the doubling/binary search
// (§Head Index) at the counts most likely to expose an off-by-one: empty,
// exactly one, exactly at a doubling boundary, and one past it.
func TestHistoryLatestSearchBoundaries(t *testing.T) {
	for _, n := range []int{0, 1, 2, 3, 4, 5, 7, 8, 9, 16, 17} {
		t.Run("n="+strconv.Itoa(n), func(t *testing.T) {
			ctx := context.Background()
			u := NewMemoryUniverse()
			self := contributor(t)
			putClaims(t, u, self)

			w := OpenHistory(u, "")
			if n == 0 {
				// Nothing was ever appended, so no seed was ever minted — a reader
				// given "" must report empty without a search to run at all.
				r := OpenHistory(u, "")
				latest, err := r.Latest(ctx)
				require.NoError(t, err)
				require.Nil(t, latest.GetId())
				ln, err := r.Len(ctx)
				require.NoError(t, err)
				require.Equal(t, 0, ln)
				return
			}
			ids := appendN(t, ctx, u, w, self, time.Now(), n)

			// A fresh reader has no cache: Latest/Len here always run the search.
			r := OpenHistory(u, w.Seed())
			latest, err := r.Latest(ctx)
			require.NoError(t, err)
			require.True(t, latest.GetId().Equal(ids[n-1]), "the search must land on the true last entry")
			require.Equal(t, n-1, latest.GetRevision())

			ln, err := r.Len(ctx)
			require.NoError(t, err)
			require.Equal(t, n, ln)

			_, err = OpenHistory(u, w.Seed()).GetAtRevision(ctx, n)
			require.Error(t, err, "one past the end must be refused, not found by an overshooting search")
		})
	}
}

// TestHistoryAppendResumesFromASearchedRevision: a History instance opened
// cold on an already-populated sequence — no cache of its own — must still
// compute the right next revision via Len's search before its first Append,
// not only once its own cache is warm from a prior write.
func TestHistoryAppendResumesFromASearchedRevision(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)

	base := time.Now()
	w := OpenHistory(u, "")
	appendN(t, ctx, u, w, self, base, 3) // revisions 0, 1, 2

	resumed := OpenHistory(u, w.Seed()) // cold: never appended, nothing cached
	head := historyHead(t, self, base.Add(100*time.Second))
	putClaims(t, u, head)
	item, err := resumed.Append(ctx, self, head.ID(), int(head.Node().Height()), base.Add(101*time.Second))
	require.NoError(t, err)
	require.Equal(t, 3, item.GetRevision(), "a cold instance must search before computing the next slot")

	final, err := OpenHistory(u, w.Seed()).Len(ctx)
	require.NoError(t, err)
	require.Equal(t, 4, final)
}

// TestHistoryFetchAtRejectsRelocatedClaim is `V-IDSEQVERIFY`: a claim whose own
// declared history_index disagrees with the slot it was fetched at is treated
// as absent there, not accepted under a borrowed identity — guarding against a
// validly-signed claim relocated to another slot (§Verifiability).
func TestHistoryFetchAtRejectsRelocatedClaim(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)
	const seed = "test-seed-relocated"

	head := historyHead(t, self, time.Now())
	putClaims(t, u, head)
	b, err := NewHistoryClaimBuilder(self, head.ID(), 5, seed)
	require.NoError(t, err)
	relocated, err := b.WithHeight(1).WithCreatedAt(time.Now()).Sign()
	require.NoError(t, err)
	env, err := relocated.Envelope()
	require.NoError(t, err)

	// Filed under id_seq(3, seed) by hand — PutClaims would file it under its own
	// id_seq(5, seed) instead, so this is the one way to force the relocation a
	// storage bug or a write-access attacker could produce.
	slot3, err := idSeq(3, seed)
	require.NoError(t, err)
	mu, ok := u.(*memoryUniverse)
	require.True(t, ok)
	mu.mu.Lock()
	mu.claims[slot3.String()] = env
	mu.mu.Unlock()

	_, err = OpenHistory(u, seed).GetAtRevision(ctx, 3)
	require.Error(t, err, "a claim declaring index 5 must not be accepted at slot 3")
}

// TestHistoryFetchAtRejectsUnverifiableClaim is V-SIG applying unchanged to a
// history claim (`V-IDSEQVERIFY`): id_seq(i,s) grants access to the slot, not
// trust in whatever sits there. A claim correctly indexed and correctly keyed,
// but signed by a contributor this archive never registered, is refused — and
// refused loudly, since a slot the search skips over reads to it as the end of
// the sequence, and Latest would report the entry before it as current.
func TestHistoryFetchAtRejectsUnverifiableClaim(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)

	base := time.Now()
	w := OpenHistory(u, "")
	ids := appendN(t, ctx, u, w, self, base, 1) // the one legitimate entry, revision 0

	// An attacker who knows the (non-secret) seed can compute id_seq(1, seed)
	// and plant anything there. Here: a claim signed under a key this archive
	// never stored anywhere, so its signature cannot be resolved, let alone
	// checked.
	attacker := contributor(t) // never stored in u
	rogueHead := historyHead(t, attacker, base.Add(10*time.Second))
	fb, err := NewHistoryClaimBuilder(attacker, rogueHead.ID(), 1, w.Seed())
	require.NoError(t, err)
	forged, err := fb.WithHeight(1).WithCreatedAt(base.Add(11 * time.Second)).Sign()
	require.NoError(t, err)
	require.NoError(t, u.PutClaims(ctx, []Claim{forged}))

	r := OpenHistory(u, w.Seed())
	_, err = r.Latest(ctx)
	require.ErrorIs(t, err, errHistorySlotVerify, "a damaged slot must be reported, not read as the end of the sequence")

	_, err = r.GetAtRevision(ctx, 1)
	require.ErrorIs(t, err, errHistorySlotVerify)
	require.NotErrorIs(t, err, errHistoryRevisionRange, "the claim is present and unverifiable, not out of range")

	// The legitimate entry below the damage is still readable on its own.
	item, err := r.GetAtRevision(ctx, 0)
	require.NoError(t, err)
	require.True(t, item.GetId().Equal(ids[0]))
}

// TestHistoryFetchAtRejectsUndecodableSlot: bytes that are not a claim at all
// occupy the slot. Nothing about them says "end of sequence", so the search
// must not treat them as one.
func TestHistoryFetchAtRejectsUndecodableSlot(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)

	w := OpenHistory(u, "")
	appendN(t, ctx, u, w, self, time.Now(), 1)

	slot1, err := idSeq(1, w.Seed())
	require.NoError(t, err)
	mu, ok := u.(*memoryUniverse)
	require.True(t, ok)
	mu.mu.Lock()
	mu.claims[slot1.String()] = []byte("not cbor at all")
	mu.mu.Unlock()

	_, err = OpenHistory(u, w.Seed()).Latest(ctx)
	require.ErrorIs(t, err, errHistorySlotDecode)
}

// TestHistoryFetchAtRejectsForeignClaimInSlot: a well-formed claim of another
// type sits at id_seq(1,s). It carries no history_index to disagree with the
// slot, so the sanctioned absence of `V-IDSEQVERIFY` does not cover it.
func TestHistoryFetchAtRejectsForeignClaimInSlot(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)

	base := time.Now()
	w := OpenHistory(u, "")
	appendN(t, ctx, u, w, self, base, 1)

	foreign := historyHead(t, self, base.Add(10*time.Second))
	env, err := foreign.Envelope()
	require.NoError(t, err)
	slot1, err := idSeq(1, w.Seed())
	require.NoError(t, err)
	mu, ok := u.(*memoryUniverse)
	require.True(t, ok)
	mu.mu.Lock()
	mu.claims[slot1.String()] = env
	mu.mu.Unlock()

	_, err = OpenHistory(u, w.Seed()).Latest(ctx)
	require.ErrorIs(t, err, errHistorySlotType)
}

// TestIdSeqWireShape pins id_seq(i,s) := H(S([i,s])) (`V-IDSEQ`) to an exact,
// known value — the byte-for-byte reproducibility cross-implementation ids
// depend on, not just "it encodes to something".
func TestIdSeqWireShape(t *testing.T) {
	id, err := idSeq(0, "ranke-history-fixture")
	require.NoError(t, err)
	require.Equal(t, "bciqajyweg4clyv73lziotpyon4l6ufu4vuxedpp4z7t4bekyg345sri", id.String())

	id1, err := idSeq(1, "ranke-history-fixture")
	require.NoError(t, err)
	require.False(t, id.Equal(id1), "a different i must yield a different id")

	id2, err := idSeq(0, "a-different-seed")
	require.NoError(t, err)
	require.False(t, id.Equal(id2), "a different s must yield a different id")
}
