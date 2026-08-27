package ranke

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// `V-DATED` at every door a claim arrives through: decode, ClaimBuilder, and
// AssembleClaim. dated is a record slot like created_at, not a generic field, so
// each door parses it independently (codec.go, claim_builder.go, claim_assemble.go).

// TestDecodeRefusesMalformedDated mirrors TestDecodeRefusesMalformedCreatedAt: a
// present value that will not parse is refused at the door.
func TestDecodeRefusesMalformedDated(t *testing.T) {
	en := encNode{
		TypeClass: "source", TypeSub: "note",
		CreatedAt: "2026-01-02T03:04:05.000000000Z",
		Dated:     "whenever",
	}
	raw := sealed(t, en)
	id, err := hashContent(raw)
	require.NoError(t, err)
	_, err = DecodeClaim(id, raw)
	require.ErrorIs(t, err, ErrDatedForm)
}

// TestDecodeAcceptsAbsentAndValidDated: an absent dated is no violation — it is
// optional (`dated?`) — and a valid EDTF Level 1 value round-trips.
func TestDecodeAcceptsAbsentAndValidDated(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		en := encNode{TypeClass: "source", TypeSub: "note", CreatedAt: "2026-01-02T03:04:05.000000000Z"}
		raw := sealed(t, en)
		id, err := hashContent(raw)
		require.NoError(t, err)
		c, err := DecodeClaim(id, raw)
		require.NoError(t, err)
		require.Equal(t, "", c.Node().Dated())
	})
	t.Run("valid", func(t *testing.T) {
		en := encNode{
			TypeClass: "source", TypeSub: "note",
			CreatedAt: "2026-01-02T03:04:05.000000000Z",
			Dated:     "2014-06",
		}
		raw := sealed(t, en)
		id, err := hashContent(raw)
		require.NoError(t, err)
		c, err := DecodeClaim(id, raw)
		require.NoError(t, err)
		require.Equal(t, "2014-06", c.Node().Dated())
	})
}

// TestBuilderWithDated: WithDated round-trips through the codec, unlike CreatedAt
// distinct from it — a claim carries both independently.
func TestBuilderWithDated(t *testing.T) {
	alice := contributor(t)
	c, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("body")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(alice)).
		WithDated("201X").
		Sign()
	require.NoError(t, err)
	require.Equal(t, "201X", c.Node().Dated())
	require.Equal(t, "201X", roundTrip(t, c).Node().Dated(), "dated survives the codec")
}

// TestBuilderRefusesMalformedDated: `V-DATED` at the builder door.
func TestBuilderRefusesMalformedDated(t *testing.T) {
	alice := contributor(t)
	_, err := NewClaim(TypeSource("note"), alice).
		WithInlineContent([]byte("body")).
		WithEncoding(EncodingPlain).
		WithHeight(HeightOf(alice)).
		WithDated("{2001,2002}").
		Sign()
	require.ErrorIs(t, err, ErrDatedForm)
}

// TestAssembleRefusesMalformedDated: the third door — a graph-native cache
// rebuilding a claim from parsed parts.
func TestAssembleRefusesMalformedDated(t *testing.T) {
	ctr := contributor(t)
	_, err := AssembleClaim(ClaimParts{
		ID: ctr.ID(), Type: "source/note", CreatedAt: ctr.Node().CreatedAt(), Height: 1,
		Dated: "whenever",
	})
	require.ErrorIs(t, err, ErrDatedForm)
}

// TestAssembleAcceptsValidDated is the control: a valid value passes and survives.
func TestAssembleAcceptsValidDated(t *testing.T) {
	ctr := contributor(t)
	c, err := AssembleClaim(ClaimParts{
		ID: ctr.ID(), Type: "source/note", CreatedAt: ctr.Node().CreatedAt(), Height: 1,
		Dated: "2014/2016",
	})
	require.NoError(t, err)
	require.Equal(t, "2014/2016", c.Node().Dated())
}
