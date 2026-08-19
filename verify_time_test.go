package ranke

import (
	"context"
	"testing"
	"time"

	"github.com/fxamacker/cbor/v2"
	"github.com/stretchr/testify/require"
)

// `V-TIME` governs delete_by and the two pubkey bounds as well as created_at, and
// none of the three was ever parsed. delete_by was compared for equality, copied as a
// string and tested for presence; the pubkey bounds were parsed only as a side effect
// of signing something, so a claim carrying its own bound was never judged.

// badTime is a value no reader can parse; canonicalTime is the form the rule requires.
const (
	badTime       = "whenever"
	canonicalTime = "2030-01-01T00:00:00.000000000Z"
)

// recordWith marshals a claim record carrying field=value, so the malformed timestamp
// arrives as bytes rather than through a builder that might refuse it.
func recordWith(t *testing.T, onEdge bool, field, value string) []byte {
	t.Helper()
	en := encNode{
		TypeClass: "source", TypeSub: "note",
		CreatedAt: "2026-01-02T03:04:05.000000000Z",
	}
	if onEdge {
		ref, err := hashContent([]byte("target"))
		require.NoError(t, err)
		ee := encEdge{
			TypeClass: "derivation", TypeSub: "source",
			Reference: idBytes(ref),
			Fields:    map[string]string{field: value},
		}
		raw, err := encodingMode.Marshal(ee)
		require.NoError(t, err)
		en.Edges = []cbor.RawMessage{raw}
	} else {
		en.Fields = map[string]string{field: value}
	}
	raw, err := encodingMode.Marshal(encClaimFile{Node: en})
	require.NoError(t, err)

	// The fixture must really carry the field, or the test proves nothing.
	var back encClaimFile
	require.NoError(t, cbor.Unmarshal(raw, &back))
	if onEdge {
		var ee encEdge
		require.NoError(t, cbor.Unmarshal(back.Node.Edges[0], &ee))
		require.Equal(t, value, ee.Fields[field])
	} else {
		require.Equal(t, value, back.Node.Fields[field])
	}
	return raw
}

// TestDecodeRefusesMalformedTimestamps: `V-TIME` at the door a foreign record comes
// through. Every governed field, on the node and on an edge.
func TestDecodeRefusesMalformedTimestamps(t *testing.T) {
	for _, field := range timeFields {
		for _, onEdge := range []bool{false, true} {
			where := "node"
			if onEdge {
				where = "edge"
			}
			t.Run(field+"/"+where, func(t *testing.T) {
				raw := recordWith(t, onEdge, field, badTime)
				id, err := hashContent(raw)
				require.NoError(t, err)
				_, err = DecodeClaim(id, raw)
				require.ErrorIs(t, err, ErrTimestampForm)
			})
		}
	}
}

// TestDecodeRefusesMalformedCreatedAt: created_at is `V-TIME`'s too, and its parse
// failure returned the bare time.Parse error — unmatchable, so a caller could not tell
// a timestamp violation from any other malformed record. The near-miss is the case
// that matters: RFC 3339 without the fraction, which a naive writer emits.
func TestDecodeRefusesMalformedCreatedAt(t *testing.T) {
	for name, value := range map[string]string{
		"unparsable":   badTime,
		"no-fraction":  "2026-01-02T03:04:05Z",
		"not-utc":      "2026-01-02T03:04:05.000000000+01:00",
		"milliseconds": "2026-01-02T03:04:05.000Z",
	} {
		t.Run(name, func(t *testing.T) {
			en := encNode{TypeClass: "source", TypeSub: "note", CreatedAt: value}
			raw, err := encodingMode.Marshal(encClaimFile{Node: en})
			require.NoError(t, err)
			id, err := hashContent(raw)
			require.NoError(t, err)
			_, err = DecodeClaim(id, raw)
			require.ErrorIs(t, err, ErrTimestampForm)
		})
	}
}

// TestDecodeAcceptsCanonicalAndAbsentTimestamps is the control that keeps the check
// from being a blanket refusal: an ABSENT field is no violation, since all three are
// optional, and a canonical value passes.
func TestDecodeAcceptsCanonicalAndAbsentTimestamps(t *testing.T) {
	t.Run("canonical", func(t *testing.T) {
		for _, field := range timeFields {
			raw := recordWith(t, false, field, canonicalTime)
			id, err := hashContent(raw)
			require.NoError(t, err)
			_, err = DecodeClaim(id, raw)
			require.NoError(t, err, "%s in canonical form is accepted", field)
		}
	})
	t.Run("absent", func(t *testing.T) {
		en := encNode{TypeClass: "source", TypeSub: "note",
			CreatedAt: "2026-01-02T03:04:05.000000000Z"}
		raw, err := encodingMode.Marshal(encClaimFile{Node: en})
		require.NoError(t, err)
		id, err := hashContent(raw)
		require.NoError(t, err)
		_, err = DecodeClaim(id, raw)
		require.NoError(t, err, "no timestamp field is no violation")
	})
}

// TestAssembleRefusesMalformedTimestamps: the second door, and the one a graph-native
// rebuild is the only traffic through. AssembleClaim took its fields as an unparsed
// string map, so a malformed timestamp reached a caller without ever meeting a parser.
func TestAssembleRefusesMalformedTimestamps(t *testing.T) {
	ctr := contributor(t)
	for _, field := range timeFields {
		t.Run(field, func(t *testing.T) {
			_, err := AssembleClaim(ClaimParts{
				ID: ctr.ID(), Type: "source/note", CreatedAt: ctr.Node().CreatedAt(),
				Fields: map[string]string{field: badTime},
			})
			require.ErrorIs(t, err, ErrTimestampForm)
		})
	}
}

// TestAssembleRefusesMalformedEdgeTimestamp: the same for a delete_by copied onto an
// edge, which is where `R-DPLANNED` keeps the date so the gap stays explained.
func TestAssembleRefusesMalformedEdgeTimestamp(t *testing.T) {
	ctr := contributor(t)
	_, err := AssembleClaim(ClaimParts{
		ID: ctr.ID(), Type: "source/note", CreatedAt: ctr.Node().CreatedAt(), Height: 1,
		Edges: []EdgeParts{{ID: ctr.ID(), Reference: ctr.ID(), Type: EdgeTypeContributor,
			Fields: map[string]string{FieldDeleteBy: badTime}}},
	})
	require.ErrorIs(t, err, ErrTimestampForm)
}

// TestAssembleAcceptsAbsentAndCanonical is the control, so a green suite is not green
// because the door refuses everything.
func TestAssembleAcceptsAbsentAndCanonical(t *testing.T) {
	ctr := contributor(t)
	_, err := AssembleClaim(ClaimParts{
		ID: ctr.ID(), Type: "source/note", CreatedAt: ctr.Node().CreatedAt(),
	})
	require.NoError(t, err, "no timestamp field is no violation")

	_, err = AssembleClaim(ClaimParts{
		ID: ctr.ID(), Type: "source/note", CreatedAt: ctr.Node().CreatedAt(),
		Fields: map[string]string{FieldDeleteBy: canonicalTime},
	})
	require.NoError(t, err, "a canonical delete_by passes")
}

// TestVerifyCatchesBoundOnANonSigner is the second hole closed, and the one the repo's
// own generator was falling into: pubkey_expires_after on a contribution/expiry claim.
// keyBound reads the bounds of a claim's SIGNER, and an expiry claim signs nothing, so
// its own bound was never parsed by anything.
//
// This also shows why no verifyRules entry is needed. The closure verifier reads
// GetClaimsRaw and decodes every claim it walks, so the decode refusal is what catches
// stored data — reported as a Failure against the claim's own id and collected with
// the rest, exactly as a rule entry would have been.
func TestVerifyCatchesBoundOnANonSigner(t *testing.T) {
	ctx := context.Background()
	ctr := contributor(t)
	expiry, err := NewClaim(NodeExpiry, ctr).
		WithField(FieldPubkeyExpiresAfter, badTime).
		WithHeight(HeightOf(ctr)).Sign()
	require.NoError(t, err)

	// It is not the signer of anything here, so ruleKeyWindow never reads this field.
	g := newGraph(t, ctr)
	require.NoError(t, g.AddClaims(ctx, expiry))
	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())

	var found bool
	for _, f := range run.Failures() {
		if f.ID.Equal(expiry.ID()) {
			require.ErrorIs(t, f.Err, ErrTimestampForm)
			found = true
		}
	}
	require.True(t, found, "an expiry claim's own bound must be judged, signer or not")
}

// TestVerifyKeyWindowRejectsUnparsableBound: a bound that is not RFC 3339 states no
// window, so it fails rather than being read as absent.
//
// It fails twice now, and both are the same violation reached from different sides: the
// contributor's own record no longer decodes, so the claim it signs cannot resolve its
// signer either. Before the decode check the bound was parsed only as a side effect of
// signing something, which is the hole `V-TIME` left open.
func TestVerifyKeyWindowRejectsUnparsableBound(t *testing.T) {
	at := time.Now().UTC()
	who, _ := windowedContributor(t, "", badTime, at)
	g := newGraph(t, who)
	require.NoError(t, g.AddClaims(context.Background(), signedAt(t, who, at)))

	run := g.Verify()
	run.Wait()
	require.NoError(t, run.Err())
	fs := run.Failures()
	require.Len(t, fs, 2, "an unreadable bound is a failure, not a free pass")
	for _, f := range fs {
		require.ErrorIs(t, f.Err, ErrTimestampForm, "both failures name the malformed bound")
	}
}

// TestGeneratorFormatWouldHaveFailed pins the format the fix corrects: RFC3339 alone
// drops the fraction `V-TIME` requires, and that is what the generator wrote.
func TestGeneratorFormatWouldHaveFailed(t *testing.T) {
	at := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	_, err := parseRFC3339Nano(at.Format(time.RFC3339))
	require.Error(t, err, "RFC3339 without a fraction is not the form V-TIME fixes")
	_, err = parseRFC3339Nano(at.Format(iso8601Nano))
	require.NoError(t, err, "the canonical layout is")
}
