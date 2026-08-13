package ranke

import (
	"crypto"
	"errors"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/require"
)

// V-TYPE, V-REL, V-PROV and V-CONTENT's XOR were enforced at construction alone, so a
// record that arrived as bytes or through AssembleClaim broke them and verified clean.
// Those fixtures are built AROUND the builder — NewClaim refuses them — by assembling
// parts or marshalling a record literal.
//
// R-DREQUEST is the exception at the end: nothing enforced it anywhere, construction
// included, so NewClaim builds its malformed shapes directly.

// fired reports whether want is among the failures, so a fixture that also breaks
// V-ID (a hand-built id is not a signature over these bytes) still tests its own rule.
func fired(fs []Failure, want error) bool {
	for _, f := range fs {
		if errors.Is(f.Err, want) {
			return true
		}
	}
	return false
}

// assembled rebuilds parts into a claim the way the neo4j adapter does — the door
// that checks none of these rules, and the reason they are reachable.
func assembled(t *testing.T, parts ClaimParts) Claim {
	t.Helper()
	c, err := AssembleClaim(parts)
	require.NoError(t, err, "AssembleClaim takes ids as given, so it builds what NewClaim refuses")
	return c
}

// signedShape assembles parts and gives them a REAL id: the record's own hash, signed
// as the claim's own signer would. Verification stops a claim at its first failing
// rule, so a fixture with a hand-made id would never reach the rule under test — this
// one is valid in every respect but the shape it is built to break.
func signedShape(t *testing.T, ctr Contributor, parts ClaimParts) Claim {
	t.Helper()
	parts.ID = ctr.ID() // a placeholder: the id is not part of the record it names
	staged := assembled(t, parts)
	sc := staged.(*claim)
	encoded, err := encodeNode(sc.node, sc.edges)
	require.NoError(t, err)
	hash, err := hashContent(encoded)
	require.NoError(t, err)

	var key crypto.Signer // an edge-less claim is initial and identity-signed (§5.7)
	if len(parts.Edges) > 0 {
		key = ctr.SigningKey()
	}
	payload, err := signHash(key, idBytes(hash))
	require.NoError(t, err)
	id, err := idFromBytes(payload)
	require.NoError(t, err)
	parts.ID = id
	return assembled(t, parts)
}

// contributorEdge is the edge a non-initial fixture needs for its signature to
// resolve, so the signer is ctr rather than the claim itself.
func contributorEdge(t *testing.T, ctr Contributor) EdgeParts {
	t.Helper()
	e, err := NewEdge(EdgeConfig{Reference: ctr.ID(), Type: EdgeTypeContributor})
	require.NoError(t, err)
	return EdgeParts{ID: e.ID(), Reference: ctr.ID(), Type: EdgeTypeContributor}
}

// TestVerifyUnknownNodeClass: `V-TYPE` — the node's class must be one of the fixed
// five. AssembleClaim splits the type without judging the class, so this arrives.
func TestVerifyUnknownNodeClass(t *testing.T) {
	ctr := contributor(t)
	bad := signedShape(t, ctr, ClaimParts{
		Type:      "sorce/note", // a typo, not a class
		CreatedAt: ctr.Node().CreatedAt(),
	})
	require.True(t, fired(verifyOne(t, bad, ctr, bad), ErrUnknownTypeClass),
		"an unknown node class must fail verification, not only construction")
}

// TestVerifyUnknownEdgeClass: `V-TYPE` — an edge's class must be one of the fixed
// three, and `source` is a node class only.
func TestVerifyUnknownEdgeClass(t *testing.T) {
	ctr := contributor(t)
	badEdge := EdgeParts{ID: ctr.ID(), Reference: ctr.ID(), Type: "source/note"}
	bad := signedShape(t, ctr, ClaimParts{
		Type: "source/note", CreatedAt: ctr.Node().CreatedAt(), Height: 1,
		Edges: []EdgeParts{contributorEdge(t, ctr), badEdge},
	})
	require.True(t, fired(verifyOne(t, bad, ctr, bad), ErrUnknownTypeClass),
		"an edge carrying a node class must fail verification")
}

// TestVerifyTypeClassesPass is the control: a well-formed claim does not trip the
// rule, so the test above fails for the reason it names.
func TestVerifyTypeClassesPass(t *testing.T) {
	ctr := contributor(t)
	good := srcClaim(t, ctr, "fine")
	require.False(t, fired(verifyOne(t, good, ctr, good), ErrUnknownTypeClass))
}

// TestVerifyRelationDirectionMissing: `V-REL` — a relation/* edge carries 1 or -1.
// NewEdge refuses direction 0 on a relation edge; assembling does not.
func TestVerifyRelationDirectionMissing(t *testing.T) {
	ctr := contributor(t)
	derivation, err := NewEdge(EdgeConfig{Reference: ctr.ID(), Type: TypeDerivation("source")})
	require.NoError(t, err)
	bad := signedShape(t, ctr, ClaimParts{
		Type: "relation/knows", CreatedAt: ctr.Node().CreatedAt(), Height: 1,
		Edges: []EdgeParts{
			contributorEdge(t, ctr),
			{ID: derivation.ID(), Reference: ctr.ID(), Type: TypeDerivation("source")},
			{ID: ctr.ID(), Reference: ctr.ID(), Type: "relation/knows"}, // direction 0
		},
	})
	require.True(t, fired(verifyOne(t, bad, ctr, bad), ErrRelationDirection),
		"a relation/* edge without a direction must fail verification")
}

// TestVerifyRelationDirectionOnOtherClass: `V-REL`'s other half — an edge of any
// class but relation/* carries 0.
func TestVerifyRelationDirectionOnOtherClass(t *testing.T) {
	ctr := contributor(t)
	bad := signedShape(t, ctr, ClaimParts{
		Type: "source/note", CreatedAt: ctr.Node().CreatedAt(), Height: 1,
		Edges: []EdgeParts{{ID: ctr.ID(), Reference: ctr.ID(),
			Type: EdgeTypeContributor, RelationDirection: RelationFrom}},
	})
	require.True(t, fired(verifyOne(t, bad, ctr, bad), ErrRelationDirection),
		"a contribution edge carrying a relation_direction must fail verification")
}

// TestVerifyProvenanceMissing: `V-PROV` — a derivation/*, entity/* or relation/*
// node carries at least one derivation/* edge, and a contributor edge is not one.
func TestVerifyProvenanceMissing(t *testing.T) {
	ctr := contributor(t)
	for _, typ := range []string{"derivation/summary", "entity/person", "relation/knows"} {
		t.Run(typ, func(t *testing.T) {
			bad := signedShape(t, ctr, ClaimParts{
				Type: typ, CreatedAt: ctr.Node().CreatedAt(), Height: 1,
				Edges: []EdgeParts{contributorEdge(t, ctr)}, // a contributor edge is not provenance
			})
			require.True(t, fired(verifyOne(t, bad, ctr, bad), ErrProvenanceMissing),
				"%s without a derivation edge must fail verification", typ)
		})
	}
}

// TestVerifyProvenanceSatisfied is the control: the same shape with a derivation
// edge passes, so the rule is not simply always firing.
func TestVerifyProvenanceSatisfied(t *testing.T) {
	ctr := contributor(t)
	derivation, err := NewEdge(EdgeConfig{Reference: ctr.ID(), Type: TypeDerivation("source")})
	require.NoError(t, err)
	good := signedShape(t, ctr, ClaimParts{
		Type: "entity/person", CreatedAt: ctr.Node().CreatedAt(), Height: 1,
		Edges: []EdgeParts{
			contributorEdge(t, ctr),
			{ID: derivation.ID(), Reference: ctr.ID(), Type: TypeDerivation("source")},
		},
	})
	require.False(t, fired(verifyOne(t, good, ctr, good), ErrProvenanceMissing))
}

// bothSlotsRecord marshals a claim record carrying content AND content_hash, which
// no encoder here produces — the malformed bytes another implementation might store.
func bothSlotsRecord(t *testing.T, edgeToo bool) []byte {
	t.Helper()
	hash, err := hashContent([]byte("external bytes"))
	require.NoError(t, err)
	en := encNode{
		TypeClass: "source", TypeSub: "note",
		EncodingClass: "text", EncodingSub: "plain",
		CreatedAt: "2026-01-02T03:04:05.000000000Z",
	}
	if edgeToo {
		ee := encEdge{
			TypeClass: "derivation", TypeSub: "source",
			Reference:     idBytes(hash),
			Content:       []byte("inline bytes"),
			ContentHash:   idBytes(hash),
			ContentSize:   uint64(len("inline bytes")),
			EncodingClass: "text", EncodingSub: "plain",
		}
		raw, err := encodingMode.Marshal(ee)
		require.NoError(t, err)
		en.Edges = []cbor.RawMessage{raw}
	} else {
		en.Content = []byte("inline bytes")
		en.ContentHash = idBytes(hash)
		en.ContentSize = uint64(len("inline bytes"))
	}
	raw, err := encodingMode.Marshal(encClaimFile{Node: en})
	require.NoError(t, err)

	// The fixture has to really carry both, or the test proves nothing.
	var back encClaimFile
	require.NoError(t, cbor.Unmarshal(raw, &back))
	rec := back.Node
	if edgeToo {
		var ee encEdge
		require.NoError(t, cbor.Unmarshal(back.Node.Edges[0], &ee))
		require.NotEmpty(t, ee.Content)
		require.NotEmpty(t, ee.ContentHash)
	} else {
		require.NotEmpty(t, rec.Content)
		require.NotEmpty(t, rec.ContentHash)
	}
	return raw
}

// TestDecodeRefusesBothContentSlots: `V-CONTENT` forbids both slots, and the decoder
// refused neither — it took the inline arm and dropped content_hash in silence, so a
// malformed record became a well-formed claim that denied what its bytes said.
func TestDecodeRefusesBothContentSlots(t *testing.T) {
	for _, edgeToo := range []bool{false, true} {
		name := "node"
		if edgeToo {
			name = "edge"
		}
		t.Run(name, func(t *testing.T) {
			raw := bothSlotsRecord(t, edgeToo)
			id, err := hashContent(raw)
			require.NoError(t, err)
			_, err = DecodeClaim(id, raw)
			require.ErrorIs(t, err, ErrContentBothSlots)
		})
	}
}

// TestAssembleRefusesBothContentSlots: the same XOR at the door neo4j rebuilds
// through. It matters more here — these parts carry no canonical bytes, so the two
// doors used to resolve the ambiguity in OPPOSITE directions: decode read the record
// as inline, assemble reported it external.
func TestAssembleRefusesBothContentSlots(t *testing.T) {
	ctr := contributor(t)
	hash, err := hashContent([]byte("external bytes"))
	require.NoError(t, err)

	t.Run("node", func(t *testing.T) {
		_, err := AssembleClaim(ClaimParts{
			ID: ctr.ID(), Type: "source/note", Encoding: "text/plain",
			ContentHash: hash, ContentSize: 12, InlineContent: []byte("inline bytes"),
		})
		require.ErrorIs(t, err, ErrContentBothSlots)
	})
	t.Run("edge", func(t *testing.T) {
		_, err := AssembleClaim(ClaimParts{
			ID: ctr.ID(), Type: "source/note", Height: 1,
			Edges: []EdgeParts{{ID: ctr.ID(), Reference: ctr.ID(),
				Type: "derivation/source", Encoding: "text/plain",
				ContentHash: hash, ContentSize: 12, InlineContent: []byte("inline bytes")}},
		})
		require.ErrorIs(t, err, ErrContentBothSlots)
	})
}

// TestBothSlotsWouldHaveLostItsOwnBytes states what the refusal prevents, on the
// assemble door where there is no stored record to fall back on: the claim the old
// code built re-encoded to bytes that hash to neither its id nor the record it came
// from. A decoded claim was spared only because it keeps its raw bytes verbatim.
func TestBothSlotsWouldHaveLostItsOwnBytes(t *testing.T) {
	ctr := contributor(t)
	hash, err := hashContent([]byte("external bytes"))
	require.NoError(t, err)

	// The two halves the refused parts would have become, built one at a time so each
	// is legal, and shown to disagree — which is why neither may stand for the pair.
	inline := assembled(t, ClaimParts{
		ID: ctr.ID(), Type: "source/note", Encoding: "text/plain",
		InlineContent: []byte("inline bytes"), ContentSize: 12,
	})
	external := assembled(t, ClaimParts{
		ID: ctr.ID(), Type: "source/note", Encoding: "text/plain",
		ContentHash: hash, ContentSize: 12,
	})
	inlineBytes, err := inline.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	externalBytes, err := external.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	require.NotEqual(t, inlineBytes, externalBytes,
		"the two readings of a both-slots record encode differently, so resolving it silently picks one and loses the other")
	require.Equal(t, ContentInline, inline.Node().ContentKind())
	require.Equal(t, ContentExternal, external.Node().ContentKind())
}

// TestVerifyDeleteMarkWithoutTarget: `R-DREQUEST` — a contribution/delete claim
// documents a deletion by carrying a contribution/delete edge to its target. Unlike the
// four rules above, this one had no construction gate either: NewClaim builds both
// malformed shapes, so the fixtures need no hand assembly.
func TestVerifyDeleteMarkWithoutTarget(t *testing.T) {
	ctr := contributor(t)

	t.Run("no delete edge at all", func(t *testing.T) {
		bad, err := NewClaim(NodeDelete, ctr).WithHeight(HeightOf(ctr)).Sign()
		require.NoError(t, err, "NewClaim builds it, which is the hole")
		require.True(t, fired(verifyOne(t, bad, ctr, bad), ErrDeleteMarkNoTarget),
			"a mark naming no target must fail verification")
	})

	t.Run("a derivation edge in its place", func(t *testing.T) {
		e, err := NewEdge(EdgeConfig{Reference: ctr.ID(), Type: TypeDerivation("source")})
		require.NoError(t, err)
		bad, err := NewClaim(NodeDelete, ctr).WithEdges(e).WithHeight(HeightOf(ctr)).Sign()
		require.NoError(t, err)
		require.True(t, fired(verifyOne(t, bad, ctr, bad), ErrDeleteMarkNoTarget),
			"an edge of another class does not document a deletion")
	})
}

// TestVerifyDeleteMarkWithTargetPasses is the control, in the shape the Sequencer and
// the generator both build: the mark carries a contribution/delete edge to its target.
func TestVerifyDeleteMarkWithTargetPasses(t *testing.T) {
	ctr := contributor(t)
	target := srcClaim(t, ctr, "the claim being deleted")
	e, err := NewEdge(EdgeConfig{Reference: target.ID(), Type: EdgeTypeDelete})
	require.NoError(t, err)
	good, err := NewClaim(NodeDelete, ctr).
		WithEdges(e).
		WithHeight(HeightOf(ctr, target)).
		Sign()
	require.NoError(t, err)
	require.False(t, fired(verifyOne(t, good, ctr, target, good), ErrDeleteMarkNoTarget))
}

// TestVerifyOrdinaryClaimNeedsNoDeleteEdge: the rule is scoped to the class, so it must
// not ask a source or a derivation for a delete edge.
func TestVerifyOrdinaryClaimNeedsNoDeleteEdge(t *testing.T) {
	ctr := contributor(t)
	plain := srcClaim(t, ctr, "no mark, no target")
	require.False(t, fired(verifyOne(t, plain, ctr, plain), ErrDeleteMarkNoTarget))
}
