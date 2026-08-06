package ranke

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// A cap is stated per claim (R-QCONTENT), so it spends across the claim's records —
// node content first, then its edges' in canonical order. The reading matters only
// where an edge carries content of its own, which is why it is pinned here.
func TestContentBudgetSpendsAcrossTheClaim(t *testing.T) {
	root := contributor(t)
	target := srcClaim(t, root, "target")

	edgeWith := func(body string) Edge {
		e, err := NewEdge(EdgeConfig{
			Reference: target.ID(), Referenced: target, Type: "derivation/transcription",
			Encoding: EncodingPlain, InlineContent: []byte(body),
		})
		require.NoError(t, err)
		return e
	}

	c, err := NewClaim(TypeSource("note"), root).
		WithInlineContent([]byte("nodecontent")).
		WithEncoding(EncodingPlain).
		WithEdges(edgeWith("edgeone"), edgeWith("edgetwo")).
		WithHeight(HeightOf(root, target)).
		Sign()
	require.NoError(t, err)

	// Node content is 11 bytes, so a cap of 14 leaves 3 for the first edge and none
	// for the second.
	raw, err := c.unwrap().encodeCBOR(FormOriginal, newContentBudget(&OutputContent{
		Max: 14, Overflow: OverflowCutoff,
	}))
	require.NoError(t, err)

	got, err := DecodeClaim(nil, raw)
	require.NoError(t, err)
	nodeInline, err := got.Node().GetInlineContent()
	require.NoError(t, err)
	require.Equal(t, []byte("nodecontent"), nodeInline, "the node is served first, and fits")

	// The claim's own edges, leaving aside the contributor edge every signature adds.
	var carried []int
	for _, e := range got.Edges() {
		if e.GetContentSize() != uint64(len("edgeone")) {
			continue
		}
		inline, err := e.GetInlineContent()
		require.NoError(t, err)
		carried = append(carried, len(inline))
	}
	// Edges serialize in canonical (id) order, so which of the two took the remainder
	// follows that order rather than the order they were passed in.
	require.ElementsMatch(t, []int{3, 0}, carried,
		"one edge takes the 3 bytes left, the other none — and both still declare 7")
}

// The budget applies to the JSON projection as it does to CBOR, the two carrying the
// same information (R-QENCODING).
func TestContentBudgetAppliesToJSON(t *testing.T) {
	root := contributor(t)
	const body = "json content"
	c := srcClaim(t, root, body)

	decode := func(raw []byte) map[string]any {
		var m map[string]any
		require.NoError(t, json.Unmarshal(raw, &m))
		return m
	}

	full, err := c.unwrap().encodeJSON(FormOriginal, newContentBudget(&OutputContent{Max: 0}))
	require.NoError(t, err)
	require.Equal(t, body, string(mustBase64(t, decode(full)[FieldContent])))

	absent, err := c.unwrap().encodeJSON(FormOriginal, newContentBudget(nil))
	require.NoError(t, err)
	m := decode(absent)
	require.NotContains(t, m, FieldContent, "no content came with it")
	require.Equal(t, float64(len(body)), m[FieldContentSize], "the size still declares it")

	cut, err := c.unwrap().encodeJSON(FormOriginal, newContentBudget(&OutputContent{
		Max: 4, Overflow: OverflowCutoff,
	}))
	require.NoError(t, err)
	require.Equal(t, body[:4], string(mustBase64(t, decode(cut)[FieldContent])))
}

func mustBase64(t *testing.T, v any) []byte {
	t.Helper()
	s, ok := v.(string)
	require.True(t, ok, "content renders as a base64 string")
	b, err := json.Marshal(s)
	require.NoError(t, err)
	var out []byte
	require.NoError(t, json.Unmarshal(b, &out))
	return out
}

// A claim whose content_size stands without its bytes is what a read under a cap
// delivers, and what a structure-only cache holds (node.ContentKind). The size has to
// survive the codec, else the claim reads as having no content at all.
func TestWithheldContentKeepsItsSize(t *testing.T) {
	root := contributor(t)
	const body = "withheld"
	c := srcClaim(t, root, body)

	raw, err := c.unwrap().encodeCBOR(FormOriginal, newContentBudget(nil))
	require.NoError(t, err)

	got, err := DecodeClaim(nil, raw)
	require.NoError(t, err)
	require.Equal(t, uint64(len(body)), got.Node().GetContentSize())
	require.Equal(t, ContentInline, got.Node().ContentKind(),
		"content it holds but did not send, so inline with the bytes missing")

	// And it survives a second trip, so a hop through a cache does not lose it either.
	again, err := got.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	twice, err := DecodeClaim(nil, again)
	require.NoError(t, err)
	require.Equal(t, uint64(len(body)), twice.Node().GetContentSize())
}

// The budget's own arithmetic, R-QCONTENT case by case.
func TestContentBudgetTake(t *testing.T) {
	content := []byte("abcdefgh")

	t.Run("nil inlines in full", func(t *testing.T) {
		var b *contentBudget
		require.True(t, b.inFull())
		require.Equal(t, content, b.take(content))
	})

	t.Run("Max 0 is the nil budget", func(t *testing.T) {
		require.True(t, newContentBudget(&OutputContent{Max: 0, Overflow: OverflowOmit}).inFull(),
			"overflow has no effect where nothing overflows")
	})

	t.Run("absent content inlines nothing", func(t *testing.T) {
		b := newContentBudget(nil)
		require.False(t, b.inFull())
		require.Empty(t, b.take(content))
	})

	t.Run("cutoff keeps the bytes up to the cap", func(t *testing.T) {
		b := newContentBudget(&OutputContent{Max: 3, Overflow: OverflowCutoff})
		require.Equal(t, []byte("abc"), b.take(content))
		require.Empty(t, b.take(content), "the cap is spent")
	})

	t.Run("omit keeps none of an overflowing content", func(t *testing.T) {
		b := newContentBudget(&OutputContent{Max: 3, Overflow: OverflowOmit})
		require.Empty(t, b.take(content))
		require.Equal(t, []byte("ab"), b.take([]byte("ab")),
			"the cap is unspent, so a content that fits still arrives")
	})

	t.Run("no content needs no budget", func(t *testing.T) {
		b := newContentBudget(&OutputContent{Max: 3, Overflow: OverflowCutoff})
		require.Empty(t, b.take(nil))
		require.Equal(t, []byte("abc"), b.take(content), "nothing was spent")
	})
}
