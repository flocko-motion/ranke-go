package ranke

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// A cap bounds the claim, not each record (R-QCONTENT), and is spent along the claim's
// content sequence: its edges' content in S(v)'s order, then the node's. Edges first is
// what keeps the smaller values whole, so the order is pinned here.
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

	// Two edges of 7 bytes each and a node of 2, so the sequence is 7, 7, 2 — the node
	// last and small enough to fit in a remainder the prefix rule denies it.
	c, err := NewClaim(TypeSource("note"), root).
		WithInlineContent([]byte("no")).
		WithEncoding(EncodingPlain).
		WithEdges(edgeWith("edgeone"), edgeWith("edgetwo")).
		WithHeight(HeightOf(root, target)).
		Sign()
	require.NoError(t, err)

	// Edges serialize in canonical (id) order rather than the order they were passed
	// in, so the two are told apart by what each carried, not by name.
	read := func(t *testing.T, oc *OutputContent) (edges []int, node int) {
		t.Helper()
		raw, err := c.unwrap().encodeCBOR(FormOriginal, newContentBudget(oc))
		require.NoError(t, err)
		got, err := DecodeClaim(nil, raw)
		require.NoError(t, err)
		for _, e := range got.Edges() {
			if e.GetContentSize() != uint64(len("edgeone")) {
				continue // the contributor edge every signature adds carries no content
			}
			inline, err := e.GetInlineContent()
			require.NoError(t, err)
			edges = append(edges, len(inline))
		}
		inline, err := got.Node().GetInlineContent()
		require.NoError(t, err)
		return edges, len(inline)
	}

	// The node's 2 bytes would fit a cap of 2 on their own. They do not arrive, because
	// the sequence reaches the edges first and stops at the one it cannot afford.
	t.Run("the edges come before the node", func(t *testing.T) {
		edges, node := read(t, &OutputContent{Max: 2, Overflow: OverflowOmit})
		require.ElementsMatch(t, []int{0, 0}, edges)
		require.Zero(t, node, "the node is last, so a cap this small never reaches it")
	})

	// The sharp case for a prefix: 3 bytes are left over and the node needs 2, yet the
	// prefix already ended at the edge that overflowed.
	t.Run("omit ends the prefix at the last whole value", func(t *testing.T) {
		edges, node := read(t, &OutputContent{Max: 10, Overflow: OverflowOmit})
		require.ElementsMatch(t, []int{7, 0}, edges, "one edge whole, the next omitted")
		require.Zero(t, node, "content after the prefix is left out though it would fit")
	})

	t.Run("cutoff ends the prefix inside a value", func(t *testing.T) {
		edges, node := read(t, &OutputContent{Max: 10, Overflow: OverflowCutoff})
		require.ElementsMatch(t, []int{7, 3}, edges, "the second edge is cut at the cap")
		require.Zero(t, node)
	})

	// A cap the whole sequence fits leaves every value whole, node included.
	t.Run("a cap the claim fits inlines all of it", func(t *testing.T) {
		edges, node := read(t, &OutputContent{Max: 16, Overflow: OverflowOmit})
		require.ElementsMatch(t, []int{7, 7}, edges)
		require.Equal(t, 2, node)
	})
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

// content_size is the FULL length whatever a record holds of it. Writing the truncated
// length would leave the shortfall unrecoverable, and the record unverifiable — the size
// sits inside S(v).
func TestACappedRecordDeclaresTheTrueSize(t *testing.T) {
	root := contributor(t)
	const body = "seventeen bytes!!"
	c := srcClaim(t, root, body)

	for _, tc := range []struct {
		name string
		oc   *OutputContent
		held int
	}{
		{"in full", &OutputContent{Max: 0}, len(body)},
		{"absent", nil, 0},
		{"cutoff", &OutputContent{Max: 4, Overflow: OverflowCutoff}, 4},
		{"omit", &OutputContent{Max: 4, Overflow: OverflowOmit}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := c.unwrap().encodeCBOR(FormOriginal, newContentBudget(tc.oc))
			require.NoError(t, err)
			got, err := DecodeClaim(nil, raw)
			require.NoError(t, err)

			inline, err := got.Node().GetInlineContent()
			require.NoError(t, err)
			require.Len(t, inline, tc.held, "the bytes served")
			require.Equal(t, uint64(len(body)), got.Node().GetContentSize(),
				"the declared length is the content's, not the prefix's")
			require.Equal(t, tc.held == len(body), got.Node().ContentComplete())
		})
	}
}

// ContentComplete is the question a caller actually has — is what I hold all of it —
// which a kind or a size alone cannot answer.
func TestContentCompleteDistinguishesAPrefix(t *testing.T) {
	root := contributor(t)

	bare, err := NewClaim(TypeSource("marker"), root).WithHeight(HeightOf(root)).Sign()
	require.NoError(t, err)
	require.Equal(t, ContentNone, bare.Node().ContentKind())
	require.True(t, bare.Node().ContentComplete(), "no content is complete trivially")

	whole := srcClaim(t, root, "all of it")
	require.True(t, whole.Node().ContentComplete())

	// A prefix reports Inline and a non-zero size exactly as a whole content does, so
	// neither tells a caller the bytes are short.
	raw, err := whole.unwrap().encodeCBOR(FormOriginal, newContentBudget(
		&OutputContent{Max: 3, Overflow: OverflowCutoff}),
	)
	require.NoError(t, err)
	prefix, err := DecodeClaim(nil, raw)
	require.NoError(t, err)
	require.Equal(t, ContentInline, prefix.Node().ContentKind(), "as a whole content does")
	require.NotZero(t, prefix.Node().GetContentSize(), "as a whole content does")
	require.False(t, prefix.Node().ContentComplete(), "and yet it holds 3 of 9 bytes")

	// External content is never held by the record, so it is incomplete until fetched.
	blob := []byte("elsewhere")
	hash, err := HashContent(blob)
	require.NoError(t, err)
	ext, err := NewClaim(TypeSource("blob"), root).
		WithExternalContent(hash, uint64(len(blob))).
		WithEncoding(EncodingOctetStream).
		WithHeight(HeightOf(root)).
		Sign()
	require.NoError(t, err)
	require.False(t, ext.Node().ContentComplete())
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

	t.Run("cutoff ends the prefix inside a value", func(t *testing.T) {
		b := newContentBudget(&OutputContent{Max: 3, Overflow: OverflowCutoff})
		require.Equal(t, []byte("abc"), b.take(content))
		require.Empty(t, b.take([]byte("a")), "the prefix has ended")
	})

	t.Run("omit ends the prefix before it", func(t *testing.T) {
		b := newContentBudget(&OutputContent{Max: 3, Overflow: OverflowOmit})
		require.Empty(t, b.take(content))
		require.Empty(t, b.take([]byte("ab")),
			"what arrives is a prefix, so a later value that fits is still left out")
	})

	// An absent overflow is omit (R-QCONTENT), so a cap alone is a whole answer.
	t.Run("an absent overflow is omit", func(t *testing.T) {
		b := newContentBudget(&OutputContent{Max: 3})
		require.Empty(t, b.take(content), "no partial value at the cap")
	})

	t.Run("a record with no content passes through", func(t *testing.T) {
		b := newContentBudget(&OutputContent{Max: 3, Overflow: OverflowCutoff})
		require.Empty(t, b.take(nil))
		require.Equal(t, []byte("abc"), b.take(content), "nothing was spent, nothing stopped")
	})
}
