package ranke

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the default closure walk (universe_closure.go):
// DefaultInClosure / DefaultGetFromClosure — the fallback edge-walk a
// byte-store Universe uses. Driven directly over mapUniverse (the toy
// byte-store), so these exercise the real ADT functions.

// TestDefaultInClosure: every claim reachable from the head by following
// edges is in the closure; an unrelated claim is not; nil head/id error.
func TestDefaultInClosure(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "seed")
	ent := entityClaim(t, root, "person", "Alice", em) // → em (derivation), root (contributor)
	putClaims(t, u, root, em, ent)

	for _, id := range []Id{ent.ID(), em.ID(), root.ID()} {
		in, err := DefaultInClosure(ctx, u, []Id{ent.ID()}, id)
		require.NoError(t, err)
		require.True(t, in, "%s is reachable from the head", id)
	}

	other := srcClaim(t, root, "unrelated")
	putClaims(t, u, other)
	in, err := DefaultInClosure(ctx, u, []Id{ent.ID()}, other.ID())
	require.NoError(t, err)
	require.False(t, in, "an unrelated claim is not in the closure")

	// An empty/nil head set is a valid, trivial query — nothing is reachable
	// from no heads, but it is not an error.
	in, err = DefaultInClosure(ctx, u, nil, em.ID())
	require.NoError(t, err, "an empty head set is a valid, trivial query")
	require.False(t, in, "nothing is in the closure of no heads")
	// A nil target id remains a usage error.
	_, err = DefaultInClosure(ctx, u, []Id{ent.ID()}, nil)
	require.Error(t, err, "nil id")
}

// TestDefaultInClosureGapIsError: a dangling reference (a claim the walk
// needs but the store lacks) is a real error, not a silent "not found".
func TestDefaultInClosureGapIsError(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "seed")
	ent := entityClaim(t, root, "person", "Alice", em)
	putClaims(t, u, root, ent) // em deliberately NOT stored — a gap under ent

	target, _ := HashContent([]byte("anything"))
	_, err := DefaultInClosure(ctx, u, []Id{ent.ID()}, target)
	require.Error(t, err, "a missing referenced claim aborts the walk")
}

// TestDefaultGetFromClosure: returns the claim when in the head's closure,
// ErrNotFound when not.
func TestDefaultGetFromClosure(t *testing.T) {
	ctx := context.Background()
	u := NewMemoryUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "seed")
	putClaims(t, u, root, em)

	got, err := DefaultGetFromClosure(ctx, u, []Id{em.ID()}, root.ID())
	require.NoError(t, err)
	require.True(t, got.ID().Equal(root.ID()), "contributor fetched from the closure")

	outside := srcClaim(t, root, "outside")
	putClaims(t, u, outside)
	_, err = DefaultGetFromClosure(ctx, u, []Id{em.ID()}, outside.ID())
	require.ErrorIs(t, err, ErrNotFound, "a claim outside the closure is not found")
}
