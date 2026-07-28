package neo4j

import (
	"context"
	"testing"

	neo4jdriver "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"

	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	"github.com/flocko-motion/ranke-go/adapter/storage/stack"
	"github.com/flocko-motion/ranke-go/tests/generator"
)

// cypherToyDelta reads the content overlay straight out of the projection, as this
// cache stored it. Scoped to the derivation: branch tables chain by
// contribution/diff too, and those carry no content, so a null size is right there.
const cypherToyDelta = "MATCH (delta:`derivation/summary`)-[:`contribution/diff`]->(base:`derivation/summary`) " +
	"RETURN delta.content_size AS deltaSize, delta.encoding AS deltaEncoding, " +
	"base.content_size AS baseSize, base.encoding AS baseEncoding"

// TestStoresMaterializedClaims asserts the projection holds MATERIALISED claims.
// GetClaims is diff-materialised by default and this cache runs no overlay pass on
// read, so a stored delta must already carry its inherited content_size and
// encoding. Keeping the bytes is optional — a lower tier serves those — but
// content_size is not, or the claim reads content-less instead of held-elsewhere.
func TestStoresMaterializedClaims(t *testing.T) {
	cache, driver := connectTestNeo4j(t)
	st, err := stack.NewStack(cache, mem.New())
	require.NoError(t, err)
	// Written through a stack: the Sequencer verifies against canonical CBOR,
	// which this cache does not hold.
	_, err = generator.Generate(context.Background(), st, generator.ToyDiff(1))
	require.NoError(t, err)

	rows := toyRows(t, driver)
	require.Len(t, rows, 1, "the toy archive has exactly one diff overlay")
	r := rows[0]

	baseSize := asInt(r["baseSize"])
	require.NotZero(t, baseSize, "the base must carry its own content_size")

	require.Equal(t, baseSize, asInt(r["deltaSize"]),
		"the delta must be stored with its inherited content_size (%d)", baseSize)
	require.Equal(t, asString(r["baseEncoding"]), asString(r["deltaEncoding"]),
		"the delta must be stored with its inherited encoding")
}

// toyRows runs cypherToyDelta and returns the records as column→value maps.
func toyRows(t *testing.T, driver neo4jdriver.DriverWithContext) []map[string]any {
	t.Helper()
	res, err := neo4jdriver.ExecuteQuery(context.Background(), driver, cypherToyDelta, nil,
		neo4jdriver.EagerResultTransformer, neo4jdriver.ExecuteQueryWithDatabase("neo4j"))
	require.NoError(t, err)
	out := make([]map[string]any, 0, len(res.Records))
	for _, rec := range res.Records {
		out = append(out, rec.AsMap())
	}
	return out
}
