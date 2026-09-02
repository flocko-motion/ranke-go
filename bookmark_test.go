package ranke

import (
	"context"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/require"
	cose "github.com/veraison/go-cose"
)

// --- The bookmark record: id_seq, the envelope `V-BMENV` fixes, and the two rules
// a record is held to on its own — `V-BMSIG` and `V-BMREF` (bookmark.go). ---

// bmHead builds a minimal empty branch table dated at — the shape `V-BMREF` requires
// a bookmark's k to resolve to, and a fresh archive's k₀.
func bmHead(t *testing.T, self Contributor, at time.Time) Claim {
	t.Helper()
	c, err := NewClaim(NodeBranches, self).WithCreatedAt(at).WithHeight(HeightOf(self)).Sign()
	require.NoError(t, err)
	return c
}

// TestIdSeqWireShape pins id_seq(i,s) := H(S([i,s])) (`V-IDSEQ`) to an exact, known
// value — the byte-for-byte reproducibility cross-implementation slots depend on, not
// just "it encodes to something". s is a byte string, so the same bytes read as text
// must not key the same slot as some other encoding of them.
func TestIdSeqWireShape(t *testing.T) {
	seed := []byte("ranke-bookmark-fixture")
	id, err := IdSeq(0, seed)
	require.NoError(t, err)
	require.Equal(t, "bciqbsr5ldbqbknahr6z3rydn66qgc5sm4ltsywe5fzac5gqtzmdxgbq", id.String())

	id1, err := IdSeq(1, seed)
	require.NoError(t, err)
	require.False(t, id.Equal(id1), "a different i must yield a different id")

	id2, err := IdSeq(0, []byte("a-different-seed"))
	require.NoError(t, err)
	require.False(t, id.Equal(id2), "a different s must yield a different id")
}

// TestSignBookmarkRoundTrip: what SignBookmark writes, DecodeBookmark reads back —
// index, seed and head, plus the kid naming the contributor whose key signed it.
func TestSignBookmarkRoundTrip(t *testing.T) {
	self := contributor(t)
	head := bmHead(t, self, time.Now())

	raw, err := SignBookmark(self, 7, []byte("round-trip-seed"), head.ID())
	require.NoError(t, err)

	bm, err := DecodeBookmark(raw)
	require.NoError(t, err)
	require.Equal(t, uint64(7), bm.Index())
	require.Equal(t, []byte("round-trip-seed"), bm.Seed())
	require.True(t, bm.Head().Equal(head.ID()))
	require.True(t, bm.Signer().Equal(self.ID()), "the kid names the contributor that signed")

	slot, err := bm.Slot()
	require.NoError(t, err)
	expected, err := IdSeq(7, []byte("round-trip-seed"))
	require.NoError(t, err)
	require.True(t, slot.Equal(expected), "a bookmark keys its own slot")
}

// TestSignBookmarkRequiresItsParts: without a signing key, a head, or a seed there is
// no bookmark to write, and each is refused by name rather than producing a record
// that reads as one.
func TestSignBookmarkRequiresItsParts(t *testing.T) {
	self := contributor(t)
	head := bmHead(t, self, time.Now())

	_, err := SignBookmark(nil, 0, []byte("s"), head.ID())
	require.ErrorIs(t, err, errBookmarkNoSigner)

	_, err = SignBookmark(self, 0, []byte("s"), nil)
	require.ErrorIs(t, err, errBookmarkNoHead)

	_, err = SignBookmark(self, 0, nil, head.ID())
	require.ErrorIs(t, err, errBookmarkNoSeed)
}

// TestDecodeBookmarkRejectsOtherShapes is `V-BMENV` clause by clause: the payload's
// arity, the two protected parameters and no more, and an empty unprotected header.
// Each defect stands alone, so a rejection names the one thing that is wrong.
func TestDecodeBookmarkRejectsOtherShapes(t *testing.T) {
	self := contributor(t)
	head := bmHead(t, self, time.Now())
	kid := self.ID().rawBytes()

	payload, err := encodingMode.Marshal(encBookmark{Index: 0, Seed: []byte("s"), Head: head.ID().rawBytes()})
	require.NoError(t, err)

	// A two-element payload: the id_seq input rather than a bookmark's own record.
	short, err := encodingMode.Marshal([]any{uint64(0), []byte("s")})
	require.NoError(t, err)
	raw, err := signCOSE(self.SigningKey(), short, cose.ProtectedHeader{cose.HeaderLabelKeyID: kid})
	require.NoError(t, err)
	_, err = DecodeBookmark(raw)
	require.ErrorIs(t, err, errBookmarkForm, "a payload of another arity is no bookmark")

	// No kid: nothing says whose key to check the signature against.
	raw, err = signCOSE(self.SigningKey(), payload, nil)
	require.NoError(t, err)
	_, err = DecodeBookmark(raw)
	require.ErrorIs(t, err, errBookmarkHeaders, "a bookmark's protected header carries a kid")

	// A third protected parameter, which would give one bookmark a second stored form.
	raw, err = signCOSE(self.SigningKey(), payload, cose.ProtectedHeader{
		cose.HeaderLabelKeyID: kid, cose.HeaderLabelContentType: "application/cbor",
	})
	require.NoError(t, err)
	_, err = DecodeBookmark(raw)
	require.ErrorIs(t, err, errBookmarkHeaders, "alg and kid are the whole protected header")

	// A parameter in the unprotected header, which no signature covers.
	raw = sealedBookmark(t, self, payload, func(m *cose.Sign1Message) {
		m.Headers.Protected[cose.HeaderLabelKeyID] = kid
		m.Headers.Unprotected[cose.HeaderLabelContentType] = "application/cbor"
	})
	_, err = DecodeBookmark(raw)
	require.ErrorIs(t, err, errBookmarkHeaders, "the unprotected header carries nothing")

	// An empty seed: no list is keyed on it, so it opens nothing.
	empty, err := encodingMode.Marshal(encBookmark{Index: 0, Seed: nil, Head: head.ID().rawBytes()})
	require.NoError(t, err)
	raw, err = signCOSE(self.SigningKey(), empty, cose.ProtectedHeader{cose.HeaderLabelKeyID: kid})
	require.NoError(t, err)
	_, err = DecodeBookmark(raw)
	require.ErrorIs(t, err, errBookmarkForm)

	// Two parameters, neither of them a kid: nothing says whose key signed.
	sealed := sealedBookmark(t, self, payload, func(m *cose.Sign1Message) {
		m.Headers.Protected[cose.HeaderLabelKeyID] = kid
	})
	raw = reheadered(t, sealed, cose.ProtectedHeader{
		cose.HeaderLabelAlgorithm: cose.AlgorithmEd25519, cose.HeaderLabelContentType: "application/cbor",
	})
	_, err = DecodeBookmark(raw)
	require.ErrorIs(t, err, errBookmarkHeaders, "a header of the right arity still needs a kid")

	// Two parameters, neither of them alg: nothing says which scheme to check under.
	raw = reheadered(t, sealed, cose.ProtectedHeader{
		cose.HeaderLabelKeyID: kid, cose.HeaderLabelContentType: "application/cbor",
	})
	_, err = DecodeBookmark(raw)
	require.ErrorIs(t, err, errBookmarkHeaders, "alg is the other half of the pair")
}

// reheadered rewrites a sealed record's protected header, which no signer here will
// produce: with the arity fixed at two, the kid's and alg's own clauses are reachable
// only this way. The signature stops covering the bytes, and nothing here checks one —
// DecodeBookmark judges the shape, and `V-BMSIG` is a rule of its own.
func reheadered(t *testing.T, raw []byte, protected cose.ProtectedHeader) []byte {
	t.Helper()
	var tag cbor.RawTag
	require.NoError(t, cbor.Unmarshal(raw, &tag))
	var parts []cbor.RawMessage
	require.NoError(t, cbor.Unmarshal(tag.Content, &parts))
	require.Len(t, parts, 4, "a COSE_Sign1 is protected, unprotected, payload, signature")

	// ProtectedHeader marshals to the bstr the structure holds it as.
	header, err := protected.MarshalCBOR()
	require.NoError(t, err)
	parts[0] = header

	body, err := encodingMode.Marshal(parts)
	require.NoError(t, err)
	out, err := encodingMode.Marshal(cbor.RawTag{Number: tag.Number, Content: body})
	require.NoError(t, err)
	return out
}

// sealedBookmark signs payload under self with the header shape returns, so a case can
// store a well-signed record of a shape signCOSE will not produce. The signature is
// real, which is the point: another shape is refused on its own terms.
func sealedBookmark(t *testing.T, self Contributor, payload []byte, shape func(*cose.Sign1Message)) []byte {
	t.Helper()
	signer, err := cose.NewSigner(cose.AlgorithmEd25519, self.SigningKey())
	require.NoError(t, err)
	msg := cose.NewSign1Message()
	msg.Payload = payload
	msg.Headers.Protected[cose.HeaderLabelAlgorithm] = cose.AlgorithmEd25519
	shape(msg)
	require.NoError(t, msg.Sign(nil, nil, signer))
	raw, err := msg.MarshalCBOR()
	require.NoError(t, err)
	return raw
}

// TestVerifyBookmarkRejectsAForgedSignature is `V-BMSIG`: id_seq(i,s) grants access to
// a slot, never trust in what sits there. A record naming one contributor as kid and
// signed under another's key is refused, however well-formed it is.
func TestVerifyBookmarkRejectsAForgedSignature(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self, attacker := contributor(t), contributor(t)
	putClaims(t, u, self)
	head := bmHead(t, self, time.Now())
	putClaims(t, u, head)

	payload, err := encodingMode.Marshal(encBookmark{
		Index: 0, Seed: []byte("forged-seed"), Head: head.ID().rawBytes(),
	})
	require.NoError(t, err)
	// The attacker's key over a payload whose kid names the registered contributor.
	raw, err := signCOSE(attacker.SigningKey(), payload, cose.ProtectedHeader{
		cose.HeaderLabelKeyID: self.ID().rawBytes(),
	})
	require.NoError(t, err)

	slot, err := IdSeq(0, []byte("forged-seed"))
	require.NoError(t, err)
	_, err = VerifyBookmark(ctx, u, slot, raw)
	require.ErrorIs(t, err, errBookmarkSignature)
}

// TestVerifyBookmarkRejectsAnUnresolvableSigner: a kid naming a claim this archive
// never registered resolves to no pubkey, so the signature cannot be checked at all —
// which is a refusal, not a pass.
func TestVerifyBookmarkRejectsAnUnresolvableSigner(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t) // never stored in u
	head := bmHead(t, self, time.Now())

	raw, err := SignBookmark(self, 0, []byte("absent-signer-seed"), head.ID())
	require.NoError(t, err)
	slot, err := IdSeq(0, []byte("absent-signer-seed"))
	require.NoError(t, err)
	_, err = VerifyBookmark(ctx, u, slot, raw)
	require.ErrorIs(t, err, errBookmarkSignature)
}

// TestVerifyBookmarkRejectsANonContributorKid: `V-BMENV` fixes the kid on a
// contribution/contributor claim. Another claim's inline content read as a pubkey would
// answer for a key nobody published, so the type is checked rather than assumed.
func TestVerifyBookmarkRejectsANonContributorKid(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)
	head := bmHead(t, self, time.Now())
	putClaims(t, u, head)

	// A source note whose inline content is a real multikey: read as a pubkey it would
	// pass for one, which is exactly what the type check rules out.
	pubkey, err := EncodePublicKey(self.SigningKey().Public())
	require.NoError(t, err)
	impostor, err := NewClaim(TypeSource("note"), self).
		WithInlineContent(pubkey).
		WithEncoding(EncodingOctetStream).
		WithHeight(HeightOf(self)).
		Sign()
	require.NoError(t, err)
	putClaims(t, u, impostor)

	seed := []byte("kid-type-seed")
	payload, err := encodingMode.Marshal(encBookmark{Index: 0, Seed: seed, Head: head.ID().rawBytes()})
	require.NoError(t, err)
	raw, err := signCOSE(self.SigningKey(), payload, cose.ProtectedHeader{
		cose.HeaderLabelKeyID: impostor.ID().rawBytes(),
	})
	require.NoError(t, err)

	slot, err := IdSeq(0, seed)
	require.NoError(t, err)
	_, err = VerifyBookmark(ctx, u, slot, raw)
	require.ErrorIs(t, err, errBookmarkSignature, "a kid must name a contribution/contributor claim")
}

// TestVerifyBookmarkHoldsTheSlot is `V-BMSLOT`, recomputed from the payload rather
// than trusted: a bookmark carrying i=1 offered at id_seq(3,s) is absent at slot 3,
// not accepted there under a borrowed index.
func TestVerifyBookmarkHoldsTheSlot(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)
	head := bmHead(t, self, time.Now())
	putClaims(t, u, head)

	seed := []byte("slot-mismatch-seed")
	raw, err := SignBookmark(self, 1, seed, head.ID())
	require.NoError(t, err)

	slot3, err := IdSeq(3, seed)
	require.NoError(t, err)
	_, err = VerifyBookmark(ctx, u, slot3, raw)
	require.ErrorIs(t, err, errBookmarkSlot, "a bookmark for index 1 does not answer for slot 3")

	// Its own slot accepts it, so the refusal above is the relocation and nothing else.
	slot1, err := IdSeq(1, seed)
	require.NoError(t, err)
	bm, err := VerifyBookmark(ctx, u, slot1, raw)
	require.NoError(t, err)
	require.Equal(t, uint64(1), bm.Index())

	_, err = VerifyBookmark(ctx, u, nil, raw)
	require.ErrorIs(t, err, errBookmarkNilSlot)
}

// TestCheckBookmarkHeadRequiresABranchTable is `V-BMREF`: k resolves to a
// contribution/branches claim, since nothing else heads an archive.
func TestCheckBookmarkHeadRequiresABranchTable(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	self := contributor(t)
	putClaims(t, u, self)

	note, err := NewClaim(TypeSource("note"), self).
		WithInlineContent([]byte("not a head")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(self)).
		Sign()
	require.NoError(t, err)
	putClaims(t, u, note)

	err = CheckBookmarkHead(ctx, u, NewBookmark(0, []byte("s"), note.ID(), self.ID()))
	require.ErrorIs(t, err, errBookmarkReference, "a source note is no archive head")

	head := bmHead(t, self, time.Now())
	putClaims(t, u, head)
	require.NoError(t, CheckBookmarkHead(ctx, u, NewBookmark(0, []byte("s"), head.ID(), self.ID())))

	// An unresolvable k says nothing about the head it names, which is also a refusal.
	err = CheckBookmarkHead(ctx, u, NewBookmark(0, []byte("s"), note.ID(), self.ID()))
	require.Error(t, err)
}

// TestMintedSeedCarriesEntropy: `V-BMENV` asks a seed for at least 128 bits, and two
// mints must not collide — a shared seed would put two archives in one list.
func TestMintedSeedCarriesEntropy(t *testing.T) {
	a, err := mintSeed()
	require.NoError(t, err)
	require.Len(t, a, seedBytes)
	require.GreaterOrEqual(t, seedBytes*8, 128, "a minted seed carries at least 128 bits")

	b, err := mintSeed()
	require.NoError(t, err)
	require.NotEqual(t, a, b)
}

// TestBookmarkSeedIsCopiedOut: the seed is construction-time and immutable, so a
// caller mutating what it was handed cannot re-key the list it came from.
func TestBookmarkSeedIsCopiedOut(t *testing.T) {
	seed := []byte("immutable-seed")
	bm := NewBookmark(0, seed, nil, nil)
	seed[0] = 'X'
	require.Equal(t, []byte("immutable-seed"), bm.Seed(), "the stored seed is a copy of the one given")

	out := bm.Seed()
	out[0] = 'Y'
	require.Equal(t, []byte("immutable-seed"), bm.Seed(), "and what it hands back is another copy")
}

// TestBookmarkPayloadIsThreeElements pins the payload's own wire shape: a
// three-element array, which is what separates it from id_seq's two-element input.
func TestBookmarkPayloadIsThreeElements(t *testing.T) {
	self := contributor(t)
	head := bmHead(t, self, time.Now())
	raw, err := SignBookmark(self, 2, []byte("arity-seed"), head.ID())
	require.NoError(t, err)

	var msg cose.Sign1Message
	require.NoError(t, msg.UnmarshalCBOR(raw))
	var elems []cbor.RawMessage
	require.NoError(t, cbor.Unmarshal(msg.Payload, &elems))
	require.Len(t, elems, 3, "S([i, s, k]) is three elements")
}
