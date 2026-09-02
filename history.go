// package: ranke / history
// type:    logic
// job:     History — the Head History (foundation paper §Head Index): a sequence of
// contribution/history claims addressed by id_seq(i,s) rather than content, letting a
// Sequencer find its archive's current head by search over the Universe alone
// limits:  the one implementation; no separate persistence port. HistoryItem (below) is
// the value shape it returns.
package ranke

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strconv"
	"time"
)

// History is a light holder over u's Head History (§Head Index): contribution/history
// claims addressed by id_seq(i, seed) (`V-IDSEQ`) rather than content. It carries no
// signing capability of its own — Append takes self per call, the Sequencer's key.
type History struct {
	u     Universe
	seed  string       // "" until the first Append mints one, or one is given (OpenHistory)
	known *HistoryItem // this instance's own last Append; nil until it has made one
}

// OpenHistory attaches to a Head History under seed — given, never discovered
// (paper §Backup); "" for one not yet bootstrapped, whose Append(0,...) mints it.
func OpenHistory(u Universe, seed string) *History {
	return &History{u: u, seed: seed}
}

// Seed returns the value id_seq(i,s) is keyed on — "" until the first Append
// mints one. Worth persisting externally to reopen this sequence (paper §Backup).
func (h *History) Seed() string { return h.seed }

// Append records id as the new head, signed under self — supplied per call, never
// stored. The revision is never taken from the caller: it is always one past
// what this instance itself last wrote (`Len`), so nothing can skip a slot or
// clobber one already written. Revision 0 mints a fresh seed when none is set
// (`V-HISTCLAIM0`); the result is cached as this instance's own known-latest.
func (h *History) Append(ctx context.Context, self Contributor, id Id, height int, createdAt time.Time) (HistoryItem, error) {
	if id == nil {
		return HistoryItem{}, errHistoryNilID
	}
	if self == nil {
		return HistoryItem{}, errHistoryNoSigner
	}
	revision, err := h.Len(ctx)
	if err != nil {
		return HistoryItem{}, err
	}
	seed := h.seed
	if revision == 0 && seed == "" {
		if seed, err = randomSeed(); err != nil {
			return HistoryItem{}, err
		}
	}
	if seed == "" {
		return HistoryItem{}, errHistoryNoSeed
	}
	b, err := NewHistoryClaimBuilder(self, id, revision, seed)
	if err != nil {
		return HistoryItem{}, err
	}
	c, err := b.WithAutoHeight(ctx, h.u).WithCreatedAt(createdAt).Sign()
	if err != nil {
		return HistoryItem{}, err
	}
	if err := h.u.PutClaims(ctx, []Claim{c}); err != nil {
		return HistoryItem{}, err
	}
	h.seed = seed
	item := NewHistoryItem(id, revision, height, c.Node().CreatedAt())
	h.known = &item
	return item, nil
}

// NewHistoryClaimBuilder returns the ClaimBuilder for a Head History entry naming
// headID at revision under self, with the head edge, required fields
// (`V-HISTCLAIM`/`V-HISTCLAIM0`) and id_seq(revision, seed) identity (`V-IDSEQ`)
// already set. The caller still supplies height and CreatedAt before Sign.
func NewHistoryClaimBuilder(self Contributor, headID Id, revision int, seed string) (ClaimBuilder, error) {
	headEdge, err := NewEdge(EdgeConfig{Reference: headID, Type: EdgeTypeHead})
	if err != nil {
		return ClaimBuilder{}, err
	}
	historyID, err := idSeq(uint64(revision), seed)
	if err != nil {
		return ClaimBuilder{}, err
	}
	b := NewClaim(NodeTypeHistory, self).
		WithField(FieldHistoryIndex, strconv.Itoa(revision)).
		WithEdges(headEdge)
	if revision == 0 {
		b = b.WithField(FieldHistorySeed, seed)
	}
	b.historyID = historyID
	return b, nil
}

// Latest returns kₙ, or the zero item when empty. Cached (Append) or else
// doubling then binary search over presence (§Head Index).
func (h *History) Latest(ctx context.Context) (HistoryItem, error) {
	if h.known != nil {
		return *h.known, nil
	}
	if h.seed == "" {
		return HistoryItem{}, nil
	}
	if _, ok, err := h.fetchAt(ctx, 0); err != nil {
		return HistoryItem{}, err
	} else if !ok {
		return HistoryItem{}, nil
	}
	lo, hi := 0, 1
	for {
		_, ok, err := h.fetchAt(ctx, hi)
		if err != nil {
			return HistoryItem{}, err
		}
		if !ok {
			break
		}
		lo, hi = hi, hi*2
	}
	for lo+1 < hi {
		mid := lo + (hi-lo)/2
		_, ok, err := h.fetchAt(ctx, mid)
		if err != nil {
			return HistoryItem{}, err
		}
		if ok {
			lo = mid
		} else {
			hi = mid
		}
	}
	return h.GetAtRevision(ctx, lo)
}

// GetAtRevision returns kᵢ; an out-of-range i is an error.
func (h *History) GetAtRevision(ctx context.Context, revision int) (HistoryItem, error) {
	if revision < 0 {
		return HistoryItem{}, WithDetail(errHistoryRevisionRange, strconv.Itoa(revision))
	}
	c, ok, err := h.fetchAt(ctx, revision)
	if err != nil {
		return HistoryItem{}, err
	}
	if !ok {
		return HistoryItem{}, WithDetail(errHistoryRevisionRange, strconv.Itoa(revision))
	}
	headID, err := headFromHistoryClaim(c)
	if err != nil {
		return HistoryItem{}, err
	}
	heights, err := h.u.GetClaimHeights(ctx, []Id{headID})
	if err != nil {
		return HistoryItem{}, err
	}
	return NewHistoryItem(headID, revision, int(heights[0]), c.Node().CreatedAt()), nil
}

// GetBulk returns the half-open revision range [from, toExcluding).
func (h *History) GetBulk(ctx context.Context, from, toExcluding int) ([]HistoryItem, error) {
	if from < 0 || from > toExcluding {
		return nil, WithDetail(errHistoryRangeInvalid, strconv.Itoa(from)+".."+strconv.Itoa(toExcluding))
	}
	items := make([]HistoryItem, 0, toExcluding-from)
	for i := from; i < toExcluding; i++ {
		item, err := h.GetAtRevision(ctx, i)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// Len returns the number of entries (n+1).
func (h *History) Len(ctx context.Context) (int, error) {
	latest, err := h.Latest(ctx)
	if err != nil {
		return 0, err
	}
	if latest.GetId() == nil {
		return 0, nil
	}
	return latest.GetRevision() + 1, nil
}

// fetchAt fetches the claim at id_seq(i, h.seed). ok is false only for the two
// absences `V-IDSEQVERIFY` allows: nothing stored, or a history_index disagreeing
// with i. Damage errors — a hole would end the head search early (§Head Index).
func (h *History) fetchAt(ctx context.Context, i int) (Claim, bool, error) {
	key, err := idSeq(uint64(i), h.seed)
	if err != nil {
		return nil, false, err
	}
	raws, err := h.u.GetClaimsRaw(ctx, []Id{key})
	if errors.Is(err, ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	raw := raws[0]
	c, err := DecodeClaim(key, raw)
	if err != nil {
		return nil, false, WrapDetail(errHistorySlotDecode, "i="+strconv.Itoa(i), err)
	}
	if c.Node().Type() != NodeTypeHistory {
		return nil, false, WithDetail(errHistorySlotType, "i="+strconv.Itoa(i)+" holds "+c.Node().Type())
	}
	// id_seq(i,s) grants access to the slot, not trust in what sits there.
	if err := verifyClaim(ctx, c, raw, newVerifyConfig(), h.u); err != nil {
		return nil, false, WrapDetail(errHistorySlotVerify, "i="+strconv.Itoa(i), err)
	}
	idx, err := c.Node().GetField(FieldHistoryIndex)
	if err != nil || idx != strconv.Itoa(i) {
		return nil, false, nil // the one absence `V-IDSEQVERIFY` sanctions
	}
	return c, true, nil
}

// headFromHistoryClaim reads the head a contribution/history claim records
// (`V-HISTCLAIM`'s contribution/head edge).
func headFromHistoryClaim(c Claim) (Id, error) {
	for _, e := range c.Edges() {
		if e.Type() == EdgeTypeHead {
			return e.Reference(), nil
		}
	}
	return nil, errHistoryNoHeadEdge
}

// randomSeed mints history_seed (`V-HISTCLAIM0`): 16 random bytes, hex-encoded.
func randomSeed() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", WrapDetail(errHistorySeedGen, "crypto/rand", err)
	}
	return hex.EncodeToString(b), nil
}

// idSeq computes id_seq(i, s) := H(S([i, s])) (`V-IDSEQ`) — a two-element array,
// distinct by CBOR major type alone from any claim's map encoding (`V-SER`).
func idSeq(i uint64, s string) (Id, error) {
	b, err := encodingMode.Marshal([]any{i, s})
	if err != nil {
		return nil, WrapDetail(errID, "id_seq encode", err)
	}
	return hashContent(b)
}

// checkHistoryClaim enforces `V-HISTCLAIM`/`V-HISTCLAIM0` on a contribution/history
// claim; a no-op for every other type.
func checkHistoryClaim(typeClass NodeClass, typeSub string, fields map[string]string, edges []*edge) error {
	if typeClass != NodeClassContribution || typeSub != string(NodeSubtypeHistory) {
		return nil
	}
	idxStr, ok := fields[FieldHistoryIndex]
	if !ok {
		return WithDetail(ErrHistoryClaimForm, "history_index missing")
	}
	i, err := strconv.ParseUint(idxStr, 10, 64)
	if err != nil {
		return WithDetail(ErrHistoryClaimForm, "history_index="+idxStr)
	}
	hasHead := false
	for _, e := range edges {
		if e.Type() == EdgeTypeHead {
			hasHead = true
			break
		}
	}
	if !hasHead {
		return WithDetail(ErrHistoryClaimForm, "no contribution/head edge")
	}
	if i == 0 {
		if seed, ok := fields[FieldHistorySeed]; !ok || seed == "" {
			return WithDetail(ErrHistoryClaim0Form, "history_seed missing at index 0")
		}
	}
	return nil
}

// SpliceHistory grafts tagged onto existing at tagged's first revision, dropping
// existing's entries from there on — correct for a forward advance and for a
// re-tag from an earlier revision alike. An empty tagged leaves existing as is.
func SpliceHistory(existing, tagged []HistoryItem) []HistoryItem {
	if len(tagged) == 0 {
		return existing
	}
	at := tagged[0].GetRevision()
	out := make([]HistoryItem, 0, at+len(tagged))
	for _, it := range existing {
		if it.GetRevision() >= at {
			continue // superseded by the tagged revisions
		}
		out = append(out, it)
	}
	return append(out, tagged...)
}
