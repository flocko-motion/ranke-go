// package: ranke / bookmark
// type:    crypto
// job:     the bookmark record (`V-BMENV`) — a COSE_Sign1 over S([i, s, k]) that 𝒰_hist holds
// under id_seq(i, s) — plus id_seq itself, the minted seed, and the checks a fetched record
// is held to (`V-BMSIG`, `V-BMSLOT`, `V-BMREF`)
// limits:  stores nothing; the store is bookmark_store.go's and the list its searches walk is
// bookmarks.go's (-> bookmarks)
package ranke

import (
	"bytes"
	"context"
	"crypto/rand"
	"strconv"

	"github.com/fxamacker/cbor/v2"
	cose "github.com/veraison/go-cose"
)

// seedBytes is the entropy a minted seed carries — 128 bits, the floor `V-BMENV` asks
// a seed to reach.
const seedBytes = 16

// Bookmark is one entry of a bookmark list: the index i it sits at, the seed s its list
// is keyed on, and the head id k it records (`V-BMENV`). Signer is the
// contribution/contributor claim its envelope names as kid, whose key signed it.
type Bookmark struct {
	index  uint64
	seed   []byte
	head   Id
	signer Id
}

// NewBookmark builds a bookmark value, for a reader reconstructing one it holds the
// parts of. The record a store keeps is SignBookmark's.
func NewBookmark(index uint64, seed []byte, head, signer Id) Bookmark {
	return Bookmark{index: index, seed: bytes.Clone(seed), head: head, signer: signer}
}

// Index returns i, the bookmark's position in its list.
func (b Bookmark) Index() uint64 { return b.index }

// Seed returns s, fixed once per list and carried by every entry, so any one of them
// opens the list.
func (b Bookmark) Seed() []byte { return bytes.Clone(b.seed) }

// Head returns k, the archive head this bookmark records.
func (b Bookmark) Head() Id { return b.head }

// Signer returns the contributor claim whose key signed this bookmark.
func (b Bookmark) Signer() Id { return b.signer }

// Slot returns id_seq(i, s), the key 𝒰_hist holds this bookmark under.
func (b Bookmark) Slot() (Id, error) { return IdSeq(b.index, b.seed) }

// encBookmark is the payload's wire shape: S([i, s, k]), a three-element CBOR
// Deterministic array (`V-BMENV`). The arity is the struct's, so a record of another
// length fails to decode rather than being read short.
type encBookmark struct {
	_     struct{} `cbor:",toarray"`
	Index uint64
	Seed  []byte
	Head  []byte
}

// IdSeq computes id_seq(i, s) := H(S([i, s])) (`V-IDSEQ`) — a two-element array of an
// unsigned integer and a byte string, which CBOR's major type alone separates from any
// claim's map encoding (`V-SER`).
func IdSeq(i uint64, s []byte) (Id, error) {
	b, err := encodingMode.Marshal([]any{i, s})
	if err != nil {
		return nil, WrapDetail(errID, "id_seq encode", err)
	}
	return hashContent(b)
}

// mintSeed returns a fresh bookmark seed: seedBytes of crypto/rand, distinct from every
// other list's.
func mintSeed() ([]byte, error) {
	s := make([]byte, seedBytes)
	if _, err := rand.Read(s); err != nil {
		return nil, WrapDetail(errBookmarkSeedGen, "crypto/rand", err)
	}
	return s, nil
}

// SignBookmark returns the record 𝒰_hist holds at id_seq(index, seed): S([i, s, k])
// signed under self, whose id the protected header carries as kid (`V-BMENV`,
// `V-BMSIG`).
func SignBookmark(self Contributor, index uint64, seed []byte, head Id) ([]byte, error) {
	switch {
	case self == nil || self.SigningKey() == nil:
		return nil, errBookmarkNoSigner
	case head == nil:
		return nil, errBookmarkNoHead
	case len(seed) == 0:
		return nil, errBookmarkNoSeed
	}
	payload, err := encodingMode.Marshal(encBookmark{Index: index, Seed: seed, Head: head.rawBytes()})
	if err != nil {
		return nil, WrapDetail(errBookmarkEncode, "payload", err)
	}
	extra := cose.ProtectedHeader{cose.HeaderLabelKeyID: self.ID().rawBytes()}
	raw, err := signCOSE(self.SigningKey(), payload, extra)
	if err != nil {
		return nil, WrapDetail(errBookmarkEncode, "envelope", err)
	}
	return raw, nil
}

// DecodeBookmark reads a stored record as a bookmark and holds it to `V-BMENV`: a
// tagged COSE_Sign1 whose protected header carries alg and kid alone, over a
// three-element S([i, s, k]). The signature is another rule's (`V-BMSIG`), since
// checking it needs the kid's contributor claim.
func DecodeBookmark(raw []byte) (Bookmark, error) {
	bm, _, err := decodeBookmark(raw)
	return bm, err
}

// decodeBookmark is DecodeBookmark keeping the parsed message, so a caller that goes
// on to check the signature does not parse the bytes twice.
func decodeBookmark(raw []byte) (Bookmark, *cose.Sign1Message, error) {
	var msg cose.Sign1Message
	if err := msg.UnmarshalCBOR(raw); err != nil {
		return Bookmark{}, nil, Wrap(errBookmarkForm, err)
	}
	if len(msg.Headers.Unprotected) != 0 {
		return Bookmark{}, nil, WithDetail(errBookmarkHeaders, "unprotected header carries "+
			strconv.Itoa(len(msg.Headers.Unprotected))+" parameter(s), want none")
	}
	if len(msg.Headers.Protected) != 2 {
		return Bookmark{}, nil, WithDetail(errBookmarkHeaders, "protected header carries "+
			strconv.Itoa(len(msg.Headers.Protected))+" parameters, want alg and kid")
	}
	if _, ok := msg.Headers.Protected[cose.HeaderLabelAlgorithm]; !ok {
		return Bookmark{}, nil, WithDetail(errBookmarkHeaders, "protected header names no algorithm")
	}
	// The kid is a bstr, so another type reaches idFromBytes as nil and fails there —
	// which is also how a header of the right arity carrying no kid at all is caught.
	kid, _ := msg.Headers.Protected[cose.HeaderLabelKeyID].([]byte)
	signer, err := idFromBytes(kid)
	if err != nil {
		return Bookmark{}, nil, WrapDetail(errBookmarkHeaders, "kid", err)
	}
	var rec encBookmark
	if err := cbor.Unmarshal(msg.Payload, &rec); err != nil {
		return Bookmark{}, nil, WrapDetail(errBookmarkForm, "payload", err)
	}
	if len(rec.Seed) == 0 {
		return Bookmark{}, nil, WithDetail(errBookmarkForm, "payload carries an empty seed")
	}
	head, err := idFromBytes(rec.Head)
	if err != nil {
		return Bookmark{}, nil, WrapDetail(errBookmarkForm, "head id", err)
	}
	return Bookmark{index: rec.Index, seed: rec.Seed, head: head, signer: signer}, &msg, nil
}

// VerifyBookmark reads the record offered at slot and holds it to the three rules a
// fetch can afford: its shape (`V-BMENV`), its signature against the pubkey of the
// contributor its kid names (`V-BMSIG`), and id_seq(i, s) recomputed from its own
// payload reproducing the slot it came from (`V-BMSLOT`).
func VerifyBookmark(ctx context.Context, u Universe, slot Id, raw []byte) (Bookmark, error) {
	if slot == nil {
		return Bookmark{}, errBookmarkNilSlot
	}
	bm, msg, err := decodeBookmark(raw)
	if err != nil {
		return Bookmark{}, err
	}
	signer, err := GetClaim(ctx, u, bm.signer)
	if err != nil {
		return Bookmark{}, WrapDetail(errBookmarkSignature, "resolve kid "+bm.signer.String(), err)
	}
	// The kid names a contribution/contributor claim (`V-BMENV`). Any other claim's
	// content read as a pubkey would answer for a key nobody published.
	if signer.Node().Type() != NodeContributor {
		return Bookmark{}, WithDetail(errBookmarkSignature, "kid names "+signer.Node().Type())
	}
	pubkey, err := resolveClaimPubkey(ctx, signer, false, u)
	if err != nil {
		return Bookmark{}, WrapDetail(errBookmarkSignature, "resolve pubkey", err)
	}
	if err := verifySign1(pubkey, msg); err != nil {
		return Bookmark{}, Wrap(errBookmarkSignature, err)
	}
	recomputed, err := bm.Slot()
	if err != nil {
		return Bookmark{}, err
	}
	if !recomputed.Equal(slot) {
		return Bookmark{}, WithDetail(errBookmarkSlot, "carries i="+strconv.FormatUint(bm.index, 10)+
			", keying id_seq to "+recomputed.String())
	}
	return bm, nil
}

// CheckBookmarkHead holds a bookmark's k to `V-BMREF`: it resolves to a
// contribution/branches claim. One 𝒰 read, which is why a fetch leaves it to the
// explicit verification a list offers (-> Bookmarks.Verify).
func CheckBookmarkHead(ctx context.Context, u Universe, bm Bookmark) error {
	head, err := GetClaim(ctx, u, bm.head)
	if err != nil {
		return WrapDetail(errBookmarkReference, bm.head.String(), err)
	}
	if head.Node().Type() != NodeBranches {
		return WithDetail(errBookmarkReference, bm.head.String()+" is "+head.Node().Type())
	}
	return nil
}
