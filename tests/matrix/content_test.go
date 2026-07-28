package matrix_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/tests/generator"
	"github.com/flocko-motion/ranke-go/tests/rql"
)

// TestContentOverCap: a read asking for more content than a projection layer keeps
// inline still gets all of it. A layer holding a prefix cannot answer the request,
// so the bytes come from a tier that holds the whole claim.
func TestContentOverCap(t *testing.T) {
	eachToyBackend(t, generator.ToyOverCapContent(1), func(t *testing.T, u ranke.Universe, m *generator.Manifest) {
		big, size := largestSource(t, u, m)
		require.Greater(t, size, uint64(4<<10), "the toy's large blob must exceed the cache's inline cap")

		for _, r := range contentResults(t, u, m.Head, big, size*2) {
			if !r.Id.Equal(big) {
				continue
			}
			require.Len(t, r.Content, int(size),
				"asked for %d bytes of a %d-byte claim, so all of it is due", size*2, size)
			return
		}
		t.Fatal("the large source was not among the results")
	})
}

// contentResults reads the claim's closure asking for up to max inlined bytes.
func contentResults(t *testing.T, u ranke.Universe, archiveHead, head ranke.Id, max uint64) []ranke.QueryResult {
	t.Helper()
	arc, err := ranke.NewArchive(context.Background(), u, archiveHead)
	require.NoError(t, err)
	rs, err := arc.Query(context.Background(), ranke.Query{
		Select: ranke.Select{Branch: rql.Branch, Head: head},
		Output: ranke.Output{Content: &ranke.Content{Max: int64(max), Overflow: ranke.OverflowCutoff}},
	})
	require.NoError(t, err)
	var out []ranke.QueryResult
	for rs.Next() {
		out = append(out, rs.Result())
	}
	require.NoError(t, rs.Err())
	require.NoError(t, rs.Close())
	return out
}

// largestSource is the toy's biggest source claim and its content size.
func largestSource(t *testing.T, u ranke.Universe, m *generator.Manifest) (ranke.Id, uint64) {
	t.Helper()
	var big ranke.Id
	var size uint64
	for _, id := range m.Sources {
		c, err := ranke.GetClaim(context.Background(), u, id)
		require.NoError(t, err)
		if s := c.Node().GetContentSize(); s > size {
			big, size = id, s
		}
	}
	require.NotNil(t, big, "the toy must carry a source with content")
	return big, size
}
