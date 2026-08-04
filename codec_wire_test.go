package ranke

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// Wire-format tests: a contribution stream is a CBOR sequence of records, opening with
// one record per constraint so a receiver reads what it is being asked for before
// taking any of it.

// stream writes blobs then claims (content first, so a claim citing it follows) and
// returns the encoded contribution.
func stream(t *testing.T, blobs []ContentBlob, branch string, claims ...Claim) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWireWriter(&buf, WireConstraints{Branches: []string{branch}})
	for _, b := range blobs {
		require.NoError(t, w.WriteContent(b))
	}
	for _, c := range claims {
		require.NoError(t, w.WriteClaim(branch, c))
	}
	return buf.Bytes()
}

// records concatenates hand-built records, for streams a WireWriter would refuse.
func records(t *testing.T, recs ...[]any) []byte {
	t.Helper()
	var buf bytes.Buffer
	for _, rec := range recs {
		b, err := encodingMode.Marshal(rec)
		require.NoError(t, err)
		buf.Write(b)
	}
	return buf.Bytes()
}

// declares is the pair of records every stream must carry.
func declares(writes, reads []string) [][]any {
	return [][]any{
		{uint64(WireBranches), writes},
		{uint64(WireReferencable), reads},
	}
}

// TestWireRoundTrip: both record kinds read back in order, each claim under its
// branch, with its bytes the ones its id was signed over.
func TestWireRoundTrip(t *testing.T) {
	root := contributor(t)
	a := srcClaim(t, root, "aardvark")
	blob := []byte("externalized bytes")
	hash, err := HashContent(blob)
	require.NoError(t, err)

	var claims []WireRecord
	var blobs []ContentBlob
	r := NewWireReader(bytes.NewReader(stream(t, []ContentBlob{{Hash: hash, Content: blob}}, "main", root, a)))
	for r.Next() {
		switch rec := r.Record(); rec.Kind {
		case WireClaim:
			claims = append(claims, rec)
		case WireContent:
			blobs = append(blobs, rec.Blob)
		}
	}
	require.NoError(t, r.Err())

	require.Len(t, blobs, 1)
	require.True(t, blobs[0].Hash.Equal(hash))
	require.Equal(t, blob, blobs[0].Content)

	require.Len(t, claims, 2)
	require.True(t, claims[0].Claim.ID().Equal(root.ID()), "ids survive the round trip")
	require.Equal(t, "main", claims[1].Branch, "each claim names its branch")

	// The record is the bytes the id derives from, so re-encoding what was decoded
	// reproduces them.
	want, err := a.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	got, err := claims[1].Claim.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	require.Equal(t, want, got, "the canonical record crosses the wire unchanged")
}

// TestWireSpansBranches is why the branch rides on the record: one stream carries
// claims for several branches, which a bulk contribution needs.
func TestWireSpansBranches(t *testing.T) {
	root := contributor(t)
	alpha, beta := srcClaim(t, root, "for alpha"), srcClaim(t, root, "for beta")

	var buf bytes.Buffer
	w := NewWireWriter(&buf, WireConstraints{Branches: []string{"alpha", "beta"}})
	require.NoError(t, w.WriteClaim("alpha", alpha))
	require.NoError(t, w.WriteClaim("beta", beta))

	got := map[string]string{}
	r := NewWireReader(bytes.NewReader(buf.Bytes()))
	for r.Next() {
		rec := r.Record()
		got[rec.Branch] = rec.Claim.ID().String()
	}
	require.NoError(t, r.Err())
	require.Equal(t, map[string]string{
		"alpha": alpha.ID().String(),
		"beta":  beta.ID().String(),
	}, got, "one stream, two branches")
}

// TestWireDeclaresConstraintsUpFront is what the records are for: a receiver reads
// every declaration from the head of the stream, before taking the payload.
func TestWireDeclaresConstraintsUpFront(t *testing.T) {
	root := contributor(t)
	want := WireConstraints{
		Branches:     []string{"alpha", "beta"},
		Referencable: []string{"main", BranchArchive},
		Lifted:       []string{NodeDelete},
	}

	var buf bytes.Buffer
	w := NewWireWriter(&buf, want)
	require.NoError(t, w.WriteClaim("alpha", srcClaim(t, root, "for alpha")))

	// The tail is unreadable, so the declarations came from the head alone.
	tail := append(buf.Bytes(), 0xff, 0xff, 0xff)
	got, err := NewWireReader(bytes.NewReader(tail)).Constraints()
	require.NoError(t, err, "the records decode without the payload behind them")
	require.Equal(t, want, got)
}

// TestWireLiftIsVisibleToTheReceiver: a lift widens, so a relay must be able to see
// one before it acts on the stream, and refuse what the sender may not ask for.
func TestWireLiftIsVisibleToTheReceiver(t *testing.T) {
	root := contributor(t)

	var buf bytes.Buffer
	w := NewWireWriter(&buf, WireConstraints{
		Branches: []string{"main"},
		Lifted:   []string{NodeBranches, NodeDelete},
	})
	require.NoError(t, w.WriteClaim("main", srcClaim(t, root, "a note")))

	cons, err := NewWireReader(bytes.NewReader(buf.Bytes())).Constraints()
	require.NoError(t, err)
	require.Equal(t, []string{NodeBranches, NodeDelete}, cons.Lifted,
		"the lift is legible without draining the stream")

	// A stream asking for nothing says so, which is what makes the check decidable.
	plain := stream(t, nil, "main", srcClaim(t, root, "another"))
	cons, err = NewWireReader(bytes.NewReader(plain)).Constraints()
	require.NoError(t, err)
	require.Empty(t, cons.Lifted)
}

// TestWireConstraintsBecomeOptions: the declarations open the contribution, so the
// enforcement path is the same one an in-process caller uses.
func TestWireConstraintsBecomeOptions(t *testing.T) {
	cons := WireConstraints{
		Branches:     []string{"main"},
		Referencable: []string{"main", "research"},
		Lifted:       []string{NodeDelete},
		Creatable:    []string{"main"},
	}
	got := NewConstraints(cons.Options()...)

	require.NoError(t, got.AdmitType(NodeDelete), "the lifted type is admitted")
	require.ErrorIs(t, got.AdmitType(NodeBranches), ErrReservedType, "and only that one")
	require.Equal(t, []string{"main", "research"}, got.referencable)
	require.Equal(t, []string{"main"}, got.creatable)
}

// TestWireCarriesCreatable: a new constraint is a new record kind, so the branches a
// contribution may bring into being cross the wire alongside the rest.
func TestWireCarriesCreatable(t *testing.T) {
	root := contributor(t)
	want := WireConstraints{
		Branches:     []string{"fresh"},
		Referencable: []string{BranchArchive},
		Creatable:    []string{"fresh"},
	}

	var buf bytes.Buffer
	w := NewWireWriter(&buf, want)
	require.NoError(t, w.WriteClaim("fresh", srcClaim(t, root, "a note")))

	got, err := NewWireReader(bytes.NewReader(buf.Bytes())).Constraints()
	require.NoError(t, err)
	require.Equal(t, want, got)

	// It narrows like the others, so a relay adds its own limits by merging.
	narrowed := want.Narrow(WireConstraints{
		Branches:     []string{"fresh"},
		Referencable: []string{BranchArchive},
		Creatable:    nil,
	})
	require.Empty(t, narrowed.Creatable, "a relay granting no creation leaves none")
}

// TestWireRepeatedConstraintsNarrow: a second declaration of a kind composes with the
// first, so a relay adds its own limits by merging into one effective constraint.
func TestWireRepeatedConstraintsNarrow(t *testing.T) {
	root := contributor(t)
	raw, err := srcClaim(t, root, "a note").EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	claim := srcClaim(t, root, "a note")

	r := NewWireReader(bytes.NewReader(records(t,
		[]any{uint64(WireBranches), []string{"main", "other"}},
		[]any{uint64(WireBranches), []string{"main"}},
		[]any{uint64(WireReferencable), []string{BranchArchive}},
		[]any{uint64(WireReferencable), []string{"main"}},
		[]any{uint64(WireLifted), []string{NodeDelete, NodeExpiry}},
		[]any{uint64(WireLifted), []string{NodeDelete}},
		[]any{uint64(WireClaim), idBytes(claim.ID()), raw, "main"},
	)))
	cons, err := r.Constraints()
	require.NoError(t, err)
	require.Equal(t, []string{"main"}, cons.Branches, "writes narrow to the overlap")
	require.Equal(t, []string{"main"}, cons.Referencable, "$archive alongside a branch leaves the branch")
	require.Equal(t, []string{NodeDelete}, cons.Lifted, "the lift narrows too")

	require.True(t, r.Next(), "the payload behind the block still reads")
}

// TestWireNarrowRespectsScopeContainment: $universe covers $archive covers a branch, so
// merging keeps the narrower of the two.
func TestWireNarrowRespectsScopeContainment(t *testing.T) {
	branch := WireConstraints{Referencable: []string{"main"}}
	archive := WireConstraints{Referencable: []string{BranchArchive}}
	universe := WireConstraints{Referencable: []string{BranchUniverse}}

	require.Equal(t, []string{"main"}, branch.Narrow(archive).Referencable)
	require.Equal(t, []string{"main"}, archive.Narrow(branch).Referencable)
	require.Equal(t, []string{BranchArchive}, archive.Narrow(universe).Referencable)
	require.Equal(t, []string{"main"}, universe.Narrow(branch).Referencable)
}

// TestWireLateConstraintRefused: the block closes at the first payload record, so a
// declaration after one would bind records already applied.
func TestWireLateConstraintRefused(t *testing.T) {
	root := contributor(t)
	claim := srcClaim(t, root, "a note")
	raw, err := claim.EncodeCBOR(FormOriginal)
	require.NoError(t, err)

	r := NewWireReader(bytes.NewReader(records(t,
		append(declares([]string{"main"}, nil),
			[]any{uint64(WireClaim), idBytes(claim.ID()), raw, "main"},
			[]any{uint64(WireReferencable), []string{BranchUniverse}},
		)...,
	)))
	require.True(t, r.Next(), "the claim ahead of it reads")
	require.False(t, r.Next())
	require.ErrorIs(t, r.Err(), ErrWireLateConstraint)
}

// TestWireUnknownConstraintRefused: a receiver cannot guarantee what it does not
// understand, so an unknown record stops the stream.
func TestWireUnknownConstraintRefused(t *testing.T) {
	r := NewWireReader(bytes.NewReader(records(t,
		append(declares([]string{"main"}, nil),
			[]any{uint64(99), []string{"whatever"}},
		)...,
	)))
	require.False(t, r.Next())
	require.ErrorIs(t, r.Err(), ErrWireKind)
}

// TestWireNeedsBothRequiredRecords: what a contribution writes to and what it may read
// from have no defaults, so a stream states both.
func TestWireNeedsBothRequiredRecords(t *testing.T) {
	root := contributor(t)
	raw, err := root.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	payload := []any{uint64(WireClaim), idBytes(root.ID()), raw, "main"}

	for name, recs := range map[string][][]any{
		"neither": {payload},
		"no writes": {
			{uint64(WireReferencable), []string{"main"}}, payload,
		},
		"no referencable": {
			{uint64(WireBranches), []string{"main"}}, payload,
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := NewWireReader(bytes.NewReader(records(t, recs...)))
			require.False(t, r.Next())
			require.ErrorIs(t, r.Err(), ErrWireNoHeader)
		})
	}
}

// TestWireClaimNeedsBranch: there is no default branch, so an unnamed one is
// refused on the way out and a record missing it is refused on the way in.
func TestWireClaimNeedsBranch(t *testing.T) {
	root := contributor(t)

	var buf bytes.Buffer
	writer := NewWireWriter(&buf, WireConstraints{Branches: []string{"main"}})
	require.ErrorIs(t, writer.WriteClaim("", root), ErrWireNoBranch)

	// A claim record built without the branch element is refused when read.
	r := NewWireReader(bytes.NewReader(records(t,
		append(declares([]string{"main"}, nil),
			[]any{uint64(WireClaim), idBytes(root.ID()), []byte("x")},
		)...,
	)))
	require.False(t, r.Next())
	require.ErrorIs(t, r.Err(), ErrWireNoBranch)
}

// TestWireDeclarationBindsTheStream: the declarations are worth checking only if the
// records cannot exceed them, so a claim on an undeclared branch is refused either way.
func TestWireDeclarationBindsTheStream(t *testing.T) {
	root := contributor(t)
	c := srcClaim(t, root, "smuggled")

	var buf bytes.Buffer
	w := NewWireWriter(&buf, WireConstraints{Branches: []string{"alpha"}})
	require.ErrorIs(t, w.WriteClaim("beta", c), ErrWireUndeclared)

	raw, err := c.EncodeCBOR(FormOriginal)
	require.NoError(t, err)
	r := NewWireReader(bytes.NewReader(records(t,
		append(declares([]string{"alpha"}, nil),
			[]any{uint64(WireClaim), idBytes(c.ID()), raw, "beta"},
		)...,
	)))
	require.False(t, r.Next(), "a claim outside the declared branches stops the stream")
	require.ErrorIs(t, r.Err(), ErrWireUndeclared)
}

// TestWireContentNeedsNoBranch: content lives in the Universe unbranched, so a
// stream carrying only blobs declares no branches and is still well-formed.
func TestWireContentNeedsNoBranch(t *testing.T) {
	blob := []byte("unbranched bytes")
	hash, err := HashContent(blob)
	require.NoError(t, err)

	var buf bytes.Buffer
	w := NewWireWriter(&buf, WireConstraints{})
	require.NoError(t, w.WriteContent(ContentBlob{Hash: hash, Content: blob}))

	r := NewWireReader(bytes.NewReader(buf.Bytes()))
	cons, err := r.Constraints()
	require.NoError(t, err)
	require.Empty(t, cons.Branches, "content touches no branch")
	require.True(t, r.Next())
	require.Equal(t, blob, r.Record().Blob.Content)
	require.False(t, r.Next())
	require.NoError(t, r.Err())
}

// TestWireContentProvesItself: content is addressed by its hash, so a record whose
// bytes do not hash to the key it names is refused.
func TestWireContentProvesItself(t *testing.T) {
	honest, err := HashContent([]byte("the real bytes"))
	require.NoError(t, err)

	var buf bytes.Buffer
	w := NewWireWriter(&buf, WireConstraints{})
	require.NoError(t, w.WriteContent(ContentBlob{Hash: honest, Content: []byte("tampered")}))

	r := NewWireReader(bytes.NewReader(buf.Bytes()))
	require.False(t, r.Next(), "a blob that does not match its hash stops the stream")
	require.ErrorIs(t, r.Err(), ErrIntegrity)
}

// TestWireEmptyStream: an empty stream ends without an error, so a contribution
// carrying nothing is not a failure.
func TestWireEmptyStream(t *testing.T) {
	r := NewWireReader(bytes.NewReader(nil))
	require.False(t, r.Next())
	require.NoError(t, r.Err())
}
