// package: ranke / bookmarks
// type:    logic
// job:     Bookmarks — one bookmark list over 𝒰_hist: the writer's Append at the next free index
// and the O(log n) searches that find a list's range from any one of its entries
// limits:  holds no signing key (Append takes the contributor per call) and no store of its own;
// the record and its rules are bookmark.go's (-> bookmark, bookmark_store)
package ranke

import (
	"bytes"
	"context"
	"errors"
	"strconv"
)

// gapProbe is how far past a settled top an initial load looks for an entry standing
// over a hole. Bounded because the index space is not, so no probe can settle it.
const gapProbe = 4

// Bookmarks is one bookmark list: entries under id_seq(i, seed) in hist, each recording
// an archive head in 𝒰.
type Bookmarks struct {
	hist     BookmarkStore
	u        Universe
	seed     []byte    // nil until the first Append mints one, or one is given (NewBookmarks)
	id       Id        // the entry it was opened at, or the first it wrote
	entry    int       // where the searches start, present once the list is non-empty
	lo, hi   int       // the settled range; hi < 0 for an empty list
	top      *Bookmark // the entry this instance wrote last
	anchored bool      // opened at a bookmark, its one way to locate the list
	wrote    bool      // has written the top itself
	loaded   bool      // the initial load, and so the gap probe, has happened
}

// NewBookmarks is the Sequencers' entry point, for minting a list or resuming its own.
// A nil seed lets the first Append mint one.
func NewBookmarks(hist BookmarkStore, u Universe, seed []byte) *Bookmarks {
	return &Bookmarks{hist: hist, u: u, seed: seed, hi: -1}
}

// OpenBookmarks recovers a list from the id of ANY of its entries (foundation paper
// §Backup): the verified record there yields the seed every entry carries, and its
// index seeds the search. No index is privileged — 0 may have been purged.
func OpenBookmarks(ctx context.Context, hist BookmarkStore, u Universe, id Id) (*Bookmarks, error) {
	if id == nil {
		return nil, errBookmarkNilSlot
	}
	record, err := hist.Get(ctx, id)
	if err != nil {
		return nil, WrapDetail(errBookmarkOpen, id.String(), err)
	}
	bm, err := VerifyBookmark(ctx, u, id, record)
	if err != nil {
		return nil, WrapDetail(errBookmarkOpen, id.String(), err)
	}
	b := &Bookmarks{hist: hist, u: u, seed: bm.seed, id: id, entry: int(bm.index), hi: -1, anchored: true}
	if _, _, err := b.bounds(ctx); err != nil {
		return nil, err
	}
	return b, nil
}

// Seed returns s, the value id_seq(i, s) keys this list on — a copy, the seed being
// fixed when the list is opened or minted.
func (b *Bookmarks) Seed() []byte { return bytes.Clone(b.seed) }

// BookmarkId returns the id of one entry of this list, which is the single value a
// bundle keeps to reopen the archive later (foundation paper §Backup).
func (b *Bookmarks) BookmarkId() Id { return b.id }

// Append records head as the next bookmark, signed under self. The index is one past
// the settled top rather than the caller's, so a write can neither skip a slot nor
// clobber one — which is how the list stays gapless (`R-C7BOOKMARK`, `V-BMGAPLESS`).
func (b *Bookmarks) Append(ctx context.Context, self Contributor, head Id) (Bookmark, error) {
	_, hi, err := b.bounds(ctx)
	if err != nil {
		return Bookmark{}, err
	}
	index := hi + 1
	seed := b.seed
	if index == 0 && len(seed) == 0 {
		if seed, err = mintSeed(); err != nil {
			return Bookmark{}, err
		}
	}
	if len(seed) == 0 {
		return Bookmark{}, errBookmarkNoSeed
	}
	record, err := SignBookmark(self, uint64(index), seed, head)
	if err != nil {
		return Bookmark{}, err
	}
	slot, err := IdSeq(uint64(index), seed)
	if err != nil {
		return Bookmark{}, err
	}
	if err := b.hist.Put(ctx, slot, record); err != nil {
		return Bookmark{}, err
	}
	b.seed = seed
	if b.id == nil {
		b.id, b.entry = slot, index
	}
	written := NewBookmark(uint64(index), seed, head, self.ID())
	b.lo, b.hi = min(b.lo, index), index
	b.top, b.wrote, b.loaded = &written, true, true
	return written, nil
}

// Latest returns the top entry, or the zero Bookmark when the list holds none. The
// writer's own last Append answers it, so a commit pays for no read.
func (b *Bookmarks) Latest(ctx context.Context) (Bookmark, error) {
	if b.wrote {
		return *b.top, nil
	}
	_, hi, err := b.bounds(ctx)
	if err != nil || hi < 0 {
		return Bookmark{}, err
	}
	return b.GetAtIndex(ctx, hi)
}

// GetAtIndex returns the entry at index i; a slot outside the list's range is an error.
func (b *Bookmarks) GetAtIndex(ctx context.Context, i int) (Bookmark, error) {
	bm, ok, err := b.fetchAt(ctx, i)
	if err != nil {
		return Bookmark{}, err
	}
	if !ok {
		return Bookmark{}, WithDetail(errBookmarkIndexRange, strconv.Itoa(i))
	}
	return bm, nil
}

// GetBulk returns the half-open index range [from, toExcluding).
func (b *Bookmarks) GetBulk(ctx context.Context, from, toExcluding int) ([]Bookmark, error) {
	if from < 0 || from > toExcluding {
		return nil, WithDetail(errBookmarkRangeInvalid, strconv.Itoa(from)+".."+strconv.Itoa(toExcluding))
	}
	out := make([]Bookmark, 0, toExcluding-from)
	for i := from; i < toExcluding; i++ {
		bm, err := b.GetAtIndex(ctx, i)
		if err != nil {
			return nil, err
		}
		out = append(out, bm)
	}
	return out, nil
}

// Len returns how many entries the list holds, counting from its lowest present index.
func (b *Bookmarks) Len(ctx context.Context) (int, error) {
	lo, hi, err := b.bounds(ctx)
	if err != nil || hi < 0 {
		return 0, err
	}
	return hi - lo + 1, nil
}

// Verify holds the list to the two rules a fetch cannot afford: contiguity
// (`V-BMGAPLESS`) and every entry's k (`V-BMREF`). Two reads per entry, so it is an
// explicit entry point.
func (b *Bookmarks) Verify(ctx context.Context) error {
	lo, hi, err := b.bounds(ctx)
	if err != nil {
		return err
	}
	for i := lo; i <= hi; i++ {
		bm, ok, err := b.fetchAt(ctx, i)
		if err != nil {
			return err
		}
		if !ok {
			return WithDetail(errBookmarkGap, "index "+strconv.Itoa(i)+" inside "+
				strconv.Itoa(lo)+".."+strconv.Itoa(hi))
		}
		if err := CheckBookmarkHead(ctx, b.u, bm); err != nil {
			return err
		}
	}
	return nil
}

// bounds settles the list's lowest and highest present index. A writer holds its own
// answer, being the only one; a reader searches again each call, since the list moves
// under it. The gap probe belongs to the initial load, so it runs once.
func (b *Bookmarks) bounds(ctx context.Context) (lo, hi int, err error) {
	if b.wrote {
		return b.lo, b.hi, nil
	}
	if len(b.seed) == 0 {
		return 0, -1, nil // no seed was ever minted, so there is no list to search
	}
	_, ok, err := b.fetchAt(ctx, b.entry)
	if err != nil {
		return 0, 0, err
	}
	if !ok {
		// A reader has no other way in, where a writer's index 0 is simply unwritten.
		if b.anchored {
			return 0, 0, WithDetail(errBookmarkOpen, "index "+strconv.Itoa(b.entry)+" is gone")
		}
		b.lo, b.hi = 0, -1
		return b.lo, b.hi, nil
	}
	if lo, err = b.descend(ctx, b.entry); err != nil {
		return 0, 0, err
	}
	if hi, err = b.ascend(ctx, b.entry); err != nil {
		return 0, 0, err
	}
	if !b.loaded {
		if err = b.probeGap(ctx, hi); err != nil {
			return 0, 0, err
		}
		b.loaded = true
	}
	b.lo, b.hi = lo, hi
	return lo, hi, nil
}

// ascend returns the highest present index at or above from, itself present: double the
// step until a miss, then bisect (§Bookmarks).
func (b *Bookmarks) ascend(ctx context.Context, from int) (int, error) {
	hit, miss, step := from, 0, 1
	for {
		probe := from + step
		ok, err := b.present(ctx, probe)
		if err != nil {
			return 0, err
		}
		if !ok {
			miss = probe
			break
		}
		hit, step = probe, step*2
	}
	for hit+1 < miss {
		mid := hit + (miss-hit)/2
		ok, err := b.present(ctx, mid)
		if err != nil {
			return 0, err
		}
		if ok {
			hit = mid
		} else {
			miss = mid
		}
	}
	return hit, nil
}

// descend is ascend run downward, and it is needed because 𝒰_hist may purge an entry:
// a valid list's range can start above 0.
func (b *Bookmarks) descend(ctx context.Context, from int) (int, error) {
	hit, miss, step := from, -1, 1
	for hit > 0 {
		probe := max(from-step, 0)
		ok, err := b.present(ctx, probe)
		if err != nil {
			return 0, err
		}
		if !ok {
			miss = probe
			break
		}
		hit = probe
		step *= 2
	}
	for miss+1 < hit {
		mid := miss + (hit-miss)/2
		ok, err := b.present(ctx, mid)
		if err != nil {
			return 0, err
		}
		if ok {
			hit = mid
		} else {
			miss = mid
		}
	}
	return hit, nil
}

// probeGap looks past the settled top, where an entry stands over a missing index and
// so falsifies `V-BMGAPLESS` outright.
func (b *Bookmarks) probeGap(ctx context.Context, hi int) error {
	for i := hi + 2; i <= hi+1+gapProbe; i++ {
		ok, err := b.present(ctx, i)
		if err != nil {
			return err
		}
		if ok {
			return WithDetail(errBookmarkGap, "index "+strconv.Itoa(i)+" is present above the top "+strconv.Itoa(hi))
		}
	}
	return nil
}

// present reports whether index i holds a well-formed bookmark of this list.
func (b *Bookmarks) present(ctx context.Context, i int) (bool, error) {
	_, ok, err := b.fetchAt(ctx, i)
	return ok, err
}

// fetchAt reads the record at id_seq(i, seed). ok is false for the two sanctioned
// absences — an empty slot, and one whose record keys another (`V-BMSLOT`). Damage
// errors: read as absence it would end the search early on a stale head.
func (b *Bookmarks) fetchAt(ctx context.Context, i int) (Bookmark, bool, error) {
	if i < 0 {
		return Bookmark{}, false, nil // no slot: uint64(i) would wrap onto a real one
	}
	slot, err := IdSeq(uint64(i), b.seed)
	if err != nil {
		return Bookmark{}, false, err
	}
	record, err := b.hist.Get(ctx, slot)
	if errors.Is(err, ErrNotFound) {
		return Bookmark{}, false, nil
	}
	if err != nil {
		return Bookmark{}, false, err
	}
	bm, err := VerifyBookmark(ctx, b.u, slot, record)
	if errors.Is(err, errBookmarkSlot) {
		return Bookmark{}, false, nil
	}
	if err != nil {
		return Bookmark{}, false, WrapDetail(errBookmarkSlotRead, "i="+strconv.Itoa(i), err)
	}
	return bm, true, nil
}
