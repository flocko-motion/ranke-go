package ranke

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// Foundation unit tests for the Archive — the immutable read-only snapshot
// RA_k = (𝒰, k) obtained from the Sequencer. Because an Archive only
// READS, it can be exercised in /* against a minimal struct-built Universe;
// the write path (Sequencer) lives in the adapters world and is out of unit
// scope. These pin claim reads, content reads (inline + external), and
// branch-table materialisation including the contribution/diff chain.

// mapUniverse is a minimal, in-memory Universe for archive read-tests that
// behaves like a real backend at the seam that matters: it stores
// SERIALIZED claim bytes (Claim.Encode) under the claim id and content
// bytes under the content hash — 𝒰's two non-colliding key spaces (§4.5) —
// and decodes (DecodeClaim) on read. So reads exercise the real codec, not
// live objects the way a shortcut double would. It implements only what
// Archive reads need (claim + content get, a closure walk over edges); the
// embedded Universe is nil, so anything else panics. A real adapter can't
// be imported here (cycle); adapter integration proper is matrixed in tests/.
type mapUniverse struct {
	Universe
	claims  map[string][]byte // id → serialized claim (Claim.Encode)
	content map[string][]byte // content hash → bytes
}

func newMapUniverse() *mapUniverse {
	return &mapUniverse{claims: map[string][]byte{}, content: map[string][]byte{}}
}

// put serializes and stores claims, exactly as a backend's PutClaims would.
func (u *mapUniverse) put(claims ...Claim) {
	for _, c := range claims {
		b, err := c.Encode()
		if err != nil {
			panic("mapUniverse.put: encode " + c.ID().String() + ": " + err.Error())
		}
		u.claims[c.ID().String()] = b
	}
}

// get decodes the stored claim at id.
func (u *mapUniverse) get(id Id) (Claim, bool) {
	b, ok := u.claims[id.String()]
	if !ok {
		return nil, false
	}
	c, err := DecodeClaim(id, b)
	if err != nil {
		return nil, false
	}
	return c, true
}

func (u *mapUniverse) GetClaims(ctx context.Context, ids []Id, opts ...GetOption) ([]Claim, error) {
	out := make([]Claim, len(ids))
	for i, id := range ids {
		c, ok := u.get(id)
		if !ok {
			return nil, ErrNotFound
		}
		out[i] = c
	}
	// Faithful byte-store behaviour: materialise diff overlays on read
	// (via the ADT's default) unless the caller wants the raw delta.
	if !newGetConfig(opts...).rawDelta {
		if _, err := DefaultMaterialize(ctx, u, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// InClosure / GetFromClosure delegate to the ADT's default edge-walk — the
// fallback a byte-store Universe uses — instead of a hand-rolled traversal.
// So every archive/branch read in these tests exercises DefaultInClosure /
// DefaultGetFromClosure (and, through them, DefaultMaterialize).
func (u *mapUniverse) InClosure(ctx context.Context, head, id Id) (bool, error) {
	return DefaultInClosure(ctx, u, head, id)
}

func (u *mapUniverse) GetFromClosure(ctx context.Context, head, id Id) (Claim, error) {
	return DefaultGetFromClosure(ctx, u, head, id)
}

func (u *mapUniverse) PutContents(_ context.Context, blobs []ContentBlob) error {
	for _, b := range blobs {
		u.content[b.Hash.String()] = b.Content
	}
	return nil
}

func (u *mapUniverse) StreamContent(_ context.Context, hash Id, _ uint64) (io.ReadCloser, error) {
	b, ok := u.content[hash.String()]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// branchEdge builds a contribution/branch edge naming a branch and pointing
// at its head.
func branchEdge(t *testing.T, name string, head Id) Edge {
	t.Helper()
	e, err := NewEdge(EdgeConfig{
		Reference: head,
		Type:      EdgeTypeBranch,
		Fields:    map[string]string{FieldName: name},
	})
	require.NoError(t, err)
	return e
}

// branchTable builds a contribution/branches claim (a branch table head)
// carrying the given branch edges, attributed to root.
func branchTable(t *testing.T, root Contributor, edges ...Edge) Claim {
	t.Helper()
	c, err := NewClaim(NodeBranches, root).WithEdges(edges...).Sign()
	require.NoError(t, err)
	return c
}

// --- NewArchive validation ---------------------------------------------

func TestNewArchiveValidation(t *testing.T) {
	ctx := context.Background()
	u := newMapUniverse()
	root := contributor(t)
	u.put(root)

	_, err := NewArchive(ctx, nil, root.ID())
	require.Error(t, err, "nil Universe rejected")
	_, err = NewArchive(ctx, u, nil)
	require.Error(t, err, "nil head id rejected")

	missing, _ := HashContent([]byte("absent"))
	_, err = NewArchive(ctx, u, missing)
	require.Error(t, err, "head not in the Universe rejected")
}

// --- claim & content reads ---------------------------------------------

func TestArchiveClaimAndContentReads(t *testing.T) {
	ctx := context.Background()
	u := newMapUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "the body")
	bth := branchTable(t, root, branchEdge(t, "main", em.ID()))
	u.put(root, em, bth)

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	// Membership across the head's closure.
	has, err := arc.HasClaim(ctx, em.ID())
	require.NoError(t, err)
	require.True(t, has, "email is in the head's closure")
	has, err = arc.HasClaim(ctx, root.ID())
	require.NoError(t, err)
	require.True(t, has, "contributor is in the closure")
	absent, _ := HashContent([]byte("nope"))
	has, err = arc.HasClaim(ctx, absent)
	require.NoError(t, err)
	require.False(t, has, "an unrelated id is not in the closure")

	// Lookup.
	got, err := arc.GetClaim(ctx, em.ID())
	require.NoError(t, err)
	require.True(t, got.ID().Equal(em.ID()))

	// Inline content served without touching content storage.
	r, err := arc.GetClaimContent(ctx, em.ID())
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, []byte("the body"), b)
}

func TestArchiveExternalContentRead(t *testing.T) {
	ctx := context.Background()
	u := newMapUniverse()
	root := contributor(t)

	blob := []byte("external archive payload")
	hash, err := HashContent(blob)
	require.NoError(t, err)
	ext, err := NewClaim(TypeSource("blob"), root).WithExternalContent(hash, uint64(len(blob))).Sign()
	require.NoError(t, err)

	bth := branchTable(t, root, branchEdge(t, "main", ext.ID()))
	u.put(root, ext, bth)
	require.NoError(t, u.PutContents(ctx, []ContentBlob{{Hash: hash, Content: blob}}))

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	r, err := arc.GetClaimContent(ctx, ext.ID())
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, blob, b, "external content streams from the Universe through the Archive")
}

// --- diff materialisation on read --------------------------------------

// TestArchiveMaterializesDiff: reading a diff claim through the archive
// materialises the overlay (the universe applies DefaultMaterialize), so a
// delta that restates only one field inherits the predecessor's other
// fields AND its content. WithNotDiffMaterialized returns the bare delta.
func TestArchiveMaterializesDiff(t *testing.T) {
	ctx := context.Background()
	u := newMapUniverse()
	root := contributor(t)

	base, err := NewClaim(TypeSource("note"), root).
		WithInlineContent([]byte("the full base content")).
		WithField("author", "alice").
		Sign()
	require.NoError(t, err)
	// A revision restating only "rev" — content and "author" are inherited.
	delta, err := NewClaim(TypeSource("note"), root).
		WithDiff(base.ID()).
		WithField("rev", "2").
		Sign()
	require.NoError(t, err)

	bth := branchTable(t, root, branchEdge(t, "main", delta.ID()))
	u.put(root, base, delta, bth)
	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	got, err := arc.GetClaim(ctx, delta.ID())
	require.NoError(t, err)
	rev, err := got.Node().GetField("rev")
	require.NoError(t, err)
	require.Equal(t, "2", rev, "the delta's own field")
	author, err := got.Node().GetField("author")
	require.NoError(t, err)
	require.Equal(t, "alice", author, "field inherited from the predecessor")

	r, err := arc.GetClaimContent(ctx, delta.ID())
	require.NoError(t, err)
	b, err := io.ReadAll(r)
	require.NoError(t, err)
	require.Equal(t, []byte("the full base content"), b, "content inherited from the predecessor")

	// The stored delta itself carries neither — that's the space saving.
	raw, err := u.GetClaims(ctx, []Id{delta.ID()}, WithNotDiffMaterialized())
	require.NoError(t, err)
	_, err = raw[0].Node().GetField("author")
	require.Error(t, err, "the raw delta does not carry the inherited field")
}

// --- branch reads -------------------------------------------------------

func TestArchiveBranchReads(t *testing.T) {
	ctx := context.Background()
	u := newMapUniverse()
	root := contributor(t)
	em := srcClaim(t, root, "seed")
	bth := branchTable(t, root, branchEdge(t, "main", em.ID()))
	u.put(root, em, bth)

	arc, err := NewArchive(ctx, u, bth.ID())
	require.NoError(t, err)

	has, err := arc.HasBranch(ctx, "main")
	require.NoError(t, err)
	require.True(t, has)
	has, err = arc.HasBranch(ctx, "dev")
	require.NoError(t, err)
	require.False(t, has, "an unknown branch is absent")

	b, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err)
	require.Equal(t, "main", b.Name())
	require.True(t, b.Head().Equal(em.ID()), "branch head points at the seed claim")

	_, err = arc.GetBranch(ctx, "dev")
	require.Error(t, err, "GetBranch on an unknown name errors")

	all, err := arc.GetBranches(ctx)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "main", all[0].Name())
}

// TestArchiveBranchDiffChain: a branch table that is a contribution/diff
// over a predecessor inherits the predecessor's branches and adds its own —
// the materialisation the Archive performs newest-over-oldest.
func TestArchiveBranchDiffChain(t *testing.T) {
	ctx := context.Background()
	u := newMapUniverse()
	root := contributor(t)
	emMain := srcClaim(t, root, "main head")
	emDev := srcClaim(t, root, "dev head")

	// table1: just main.
	table1 := branchTable(t, root, branchEdge(t, "main", emMain.ID()))
	// table2: a diff over table1 that adds dev.
	table2, err := NewClaim(NodeBranches, root).
		WithDiff(table1.ID()).
		WithEdges(branchEdge(t, "dev", emDev.ID())).
		Sign()
	require.NoError(t, err)

	u.put(root, emMain, emDev, table1, table2)

	arc, err := NewArchive(ctx, u, table2.ID())
	require.NoError(t, err)

	all, err := arc.GetBranches(ctx)
	require.NoError(t, err)
	names := []string{}
	for _, b := range all {
		names = append(names, b.Name())
	}
	require.ElementsMatch(t, []string{"main", "dev"}, names,
		"diff table inherits main and adds dev")

	main, err := arc.GetBranch(ctx, "main")
	require.NoError(t, err)
	require.True(t, main.Head().Equal(emMain.ID()), "inherited main head preserved")
	dev, err := arc.GetBranch(ctx, "dev")
	require.NoError(t, err)
	require.True(t, dev.Head().Equal(emDev.ID()), "added dev head resolved")
}
