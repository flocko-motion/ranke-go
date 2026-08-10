package ranke

import (
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// recordKeys reads the numeric key each field serializes under off the struct tags,
// which is where the wire contract actually lives.
func recordKeys(t *testing.T, v any) map[string]int {
	t.Helper()
	rt := reflect.TypeOf(v)
	out := make(map[string]int, rt.NumField())
	for i := range rt.NumField() {
		f := rt.Field(i)
		tag := f.Tag.Get("cbor")
		require.NotEmpty(t, tag, "%s carries no cbor tag, so its key is whatever order gives it", f.Name)
		key, err := strconv.Atoi(strings.Split(tag, ",")[0])
		require.NoError(t, err, "%s: keyasint needs a number", f.Name)
		out[f.Name] = key
	}
	return out
}

// TestRecordKeysMatchTheTable pins the numbering `V-SER` fixes. The keys live in struct
// tags, so a renumber round-trips happily while every id moves — only a reader holding
// bytes from the other numbering notices, and by then they are published.
func TestRecordKeysMatchTheTable(t *testing.T) {
	// The eight slots a node and an edge share, which take the same key in both.
	shared := map[string]int{
		"TypeClass":     1,
		"TypeSub":       2,
		"EncodingClass": 3,
		"EncodingSub":   4,
		"ContentHash":   5,
		"Content":       6,
		"ContentSize":   7,
		"Fields":        8,
	}
	node := map[string]int{"CreatedAt": 9, "Edges": 10, "Height": 11}
	edge := map[string]int{"Reference": 12, "RelationDirection": 13}

	want := func(own map[string]int) map[string]int {
		m := map[string]int{}
		for k, v := range shared {
			m[k] = v
		}
		for k, v := range own {
			m[k] = v
		}
		return m
	}

	require.Equal(t, want(node), recordKeys(t, encNode{}), "node record keys")
	require.Equal(t, want(edge), recordKeys(t, encEdge{}), "edge record keys")
}

// One number, one meaning. The records are untyped maps, so a reader that mixed them up
// would decode rather than fail, reading a created_at as a reference.
func TestSharedRecordKeysAgree(t *testing.T) {
	n, e := recordKeys(t, encNode{}), recordKeys(t, encEdge{})
	for name, key := range n {
		other, both := e[name]
		if !both {
			continue
		}
		require.Equal(t, key, other, "%s takes key %d in a node and %d in an edge", name, key, other)
	}

	// And a key one record uses for its own slot is never the other's: 9 to 11 belong to
	// a node, 12 to 13 to an edge.
	byKey := func(m map[string]int) map[int]string {
		out := make(map[int]string, len(m))
		for name, key := range m {
			out[key] = name
		}
		return out
	}
	nk, ek := byKey(n), byKey(e)
	for key, nodeField := range nk {
		if edgeField, taken := ek[key]; taken {
			require.Equal(t, nodeField, edgeField, "key %d means two different things", key)
		}
	}
}

// Keys stay under 24 so CBOR encodes each in the head byte alone; 24 and up cost a
// second byte on every record, which the table was arranged to avoid.
func TestRecordKeysFitOneByte(t *testing.T) {
	for _, rec := range []any{encNode{}, encEdge{}} {
		for name, key := range recordKeys(t, rec) {
			require.Less(t, key, 24, "%s at key %d spills into a second head byte", name, key)
			require.Positive(t, key, "%s: key 0 is unassigned", name)
		}
	}
}
