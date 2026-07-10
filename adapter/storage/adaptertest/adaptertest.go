// package: adaptertest / conformance
// type:    test
// job:     black-box conformance suite exercising any ranke.Universe via the public API
// limits:  no medium-specific scenarios; each adapter adds those alongside (-> adapter/storage/fs, adapter/storage/mem)
//
// Package adaptertest is a black-box conformance suite for ranke.Universe
// implementations. It exercises an adapter purely through the public
// ranke API — claims and content go in and come back out, copies merge
// closures, lookups of absent ids report absence — without any knowledge
// of how or where the adapter stores its bytes.
//
// Each adapter wraps it in a one-line test:
//
//	func TestConformance(t *testing.T) {
//	    adaptertest.Run(t, func(t *testing.T) ranke.Universe { return mem.New() })
//	}
//
// and adds its own medium-specific scenarios (e.g. fs simulating a
// corrupted file on disk) alongside.
package adaptertest

import (
	"context"
	"errors"
	"testing"

	"github.com/flocko-motion/ranke-go"
)

// memHead is a throwaway in-memory BranchTableHead for the copy test. The
// contract suite depends only on ranke's interfaces — never on a concrete
// adapter — so it carries its own instead of importing adapter/mem (which
// would cycle: adapter/mem's test imports this package).
type memHead struct{ id ranke.Id }

func (h *memHead) Load(context.Context) (ranke.Id, error)    { return h.id, nil }
func (h *memHead) Save(_ context.Context, id ranke.Id) error { h.id = id; return nil }
func (h *memHead) Close() error                              { return nil }

// Factory returns a fresh, empty Universe. Run calls it more than once
// (e.g. a source and a destination for copy tests), so each call must
// yield an independent, empty store.
type Factory func(t *testing.T) ranke.Universe

// Run executes the full black-box conformance suite against universes
// produced by newU.
func Run(t *testing.T, newU Factory) {
	t.Run("claims bulk round-trip", func(t *testing.T) { testClaimsRoundTrip(t, newU) })
	t.Run("content round-trip and stream", func(t *testing.T) { testContentRoundTrip(t, newU) })
	t.Run("absent lookups", func(t *testing.T) { testAbsentLookups(t, newU) })
	t.Run("copy closure merges provenance", func(t *testing.T) { testCopyClosure(t, newU) })
	t.Run("capabilities", func(t *testing.T) { testCapabilities(t, newU) })
}

// testCapabilities verifies the adapter reports usable capabilities. Every
// in-tree backend can overwrite (Put replaces existing bytes), which the
// read-through repair in adapter/storage/stack relies on.
func testCapabilities(t *testing.T, newU Factory) {
	u := newU(t)
	defer u.Close()
	if !u.Capabilities().Overwrite {
		t.Fatal("Capabilities: expected Overwrite=true for an in-tree backend")
	}
}

// sample builds a tiny signed graph: a contributor op and an email claim
// em attributed to it. Returned via the public builder API only.
func sample(t *testing.T) (op ranke.Contributor, em ranke.Claim) {
	t.Helper()
	operator, err := ranke.ClaimBuilder{
		Type:    ranke.NodeContributor,
		Content: []byte("op@example.com"),
	}.Sign()
	if err != nil {
		t.Fatalf("contributor: %v", err)
	}
	op, err = operator.AsContributor()
	if err != nil {
		t.Fatalf("AsContributor: %v", err)
	}
	em, err = ranke.ClaimBuilder{
		Type:        ranke.TypeSource("email"),
		Encoding:    ranke.EncodingMessage("rfc822"),
		Content:     []byte("From: a\r\nTo: b\r\n\r\nhi\r\n"),
		Contributor: op,
	}.Sign()
	if err != nil {
		t.Fatalf("email: %v", err)
	}
	return op, em
}

func testClaimsRoundTrip(t *testing.T, newU Factory) {
	ctx := context.Background()
	u := newU(t)
	defer u.Close()
	op, em := sample(t)

	if err := u.PutClaims(ctx, []ranke.Claim{op, em}); err != nil {
		t.Fatalf("PutClaims: %v", err)
	}
	// Idempotent re-put.
	if err := u.PutClaims(ctx, []ranke.Claim{op, em}); err != nil {
		t.Fatalf("PutClaims (rerun): %v", err)
	}

	got, err := u.GetClaims(ctx, []ranke.Id{em.ID(), op.ID()})
	if err != nil {
		t.Fatalf("GetClaims: %v", err)
	}
	if len(got) != 2 || !got[0].ID().Equal(em.ID()) || !got[1].ID().Equal(op.ID()) {
		t.Fatalf("GetClaims returned wrong claims/order")
	}

	has, err := u.HasClaims(ctx, []ranke.Id{em.ID(), op.ID()})
	if err != nil {
		t.Fatalf("HasClaims: %v", err)
	}
	if !has[0] || !has[1] {
		t.Fatalf("HasClaims = %v, want both true", has)
	}
}

func testContentRoundTrip(t *testing.T, newU Factory) {
	ctx := context.Background()
	u := newU(t)
	defer u.Close()

	payload := []byte("hello, ranke")
	h, err := ranke.HashContent(payload)
	if err != nil {
		t.Fatalf("HashContent: %v", err)
	}
	size := uint64(len(payload))

	if err := u.PutContents(ctx, []ranke.ContentBlob{{Hash: h, Content: payload}}); err != nil {
		t.Fatalf("PutContents: %v", err)
	}

	has, err := u.HasContents(ctx, []ranke.Id{h})
	if err != nil {
		t.Fatalf("HasContents: %v", err)
	}
	if !has[0] {
		t.Fatalf("HasContents = false, want true")
	}

	bs, err := u.GetContents(ctx, []ranke.ContentRef{{Hash: h, Size: size}})
	if err != nil {
		t.Fatalf("GetContents: %v", err)
	}
	if string(bs[0]) != string(payload) {
		t.Fatalf("GetContents = %q, want %q", bs[0], payload)
	}

	r, err := u.StreamContent(ctx, h, size)
	if err != nil {
		t.Fatalf("StreamContent: %v", err)
	}
	defer r.Close()
	streamed := make([]byte, size)
	if _, err := readFull(r, streamed); err != nil {
		t.Fatalf("stream read: %v", err)
	}
	if string(streamed) != string(payload) {
		t.Fatalf("StreamContent = %q, want %q", streamed, payload)
	}
}

func testAbsentLookups(t *testing.T, newU Factory) {
	ctx := context.Background()
	u := newU(t)
	defer u.Close()
	_, em := sample(t)

	has, err := u.HasClaims(ctx, []ranke.Id{em.ID()})
	if err != nil {
		t.Fatalf("HasClaims: %v", err)
	}
	if has[0] {
		t.Fatalf("HasClaims on empty store = true, want false")
	}

	if _, err := u.GetClaims(ctx, []ranke.Id{em.ID()}); !errors.Is(err, ranke.ErrNotFound) {
		t.Fatalf("GetClaims(absent) err = %v, want ErrNotFound", err)
	}

	cHas, err := u.HasContents(ctx, []ranke.Id{em.ID()})
	if err != nil {
		t.Fatalf("HasContents: %v", err)
	}
	if cHas[0] {
		t.Fatalf("HasContents on empty store = true, want false")
	}
}

func testCopyClosure(t *testing.T, newU Factory) {
	ctx := context.Background()
	src := newU(t)
	defer src.Close()
	dst := newU(t)
	defer dst.Close()

	op, em := sample(t)

	srcSeq, err := ranke.NewSequencer(ctx, src, &memHead{}, op)
	if err != nil {
		t.Fatalf("NewSequencer(src): %v", err)
	}

	g := ranke.NewGraph(op)
	if err := g.Add(em); err != nil {
		t.Fatalf("g.Add: %v", err)
	}
	if err := srcSeq.AddGraph(ctx, "main", g, op); err != nil {
		t.Fatalf("AddGraph: %v", err)
	}
	srcArc, _, err := srcSeq.GetArchive(ctx)
	if err != nil {
		t.Fatalf("GetArchive(src): %v", err)
	}
	srcBranch, err := srcArc.GetBranch(ctx, "main")
	if err != nil {
		t.Fatalf("GetBranch: %v", err)
	}
	head := srcBranch.Latest().Head()

	if err := dst.CopyClaims(ctx, src, []ranke.Id{head}, ranke.WithClosure(), ranke.WithContent()); err != nil {
		t.Fatalf("CopyClaims: %v", err)
	}

	dstArc, err := ranke.NewArchive(ctx, dst, &memHead{})
	if err != nil {
		t.Fatalf("NewArchive(dst): %v", err)
	}
	mergedClaim, err := dstArc.GetClaim(ctx, head)
	if err != nil {
		t.Fatalf("dst GetClaim: %v", err)
	}
	merged, err := mergedClaim.Graph(ctx)
	if err != nil {
		t.Fatalf("merged.Graph: %v", err)
	}
	for _, want := range []ranke.Id{head, em.ID(), op.ID()} {
		if !merged.Contains(want) {
			t.Errorf("dst closure missing %s", want.String())
		}
	}
	if err := merged.Validate(); err != nil {
		t.Errorf("merged closure does not validate: %v", err)
	}

	// Idempotent re-copy.
	if err := dst.CopyClaims(ctx, src, []ranke.Id{head}, ranke.WithClosure(), ranke.WithContent()); err != nil {
		t.Fatalf("CopyClaims (rerun): %v", err)
	}

	// A head absent from src must surface an error.
	orphan, err := ranke.ClaimBuilder{
		Type:    ranke.NodeContributor,
		Content: []byte("never-saved"),
	}.Sign()
	if err != nil {
		t.Fatalf("synthesize orphan: %v", err)
	}
	if err := dst.CopyClaims(ctx, src, []ranke.Id{orphan.ID()}, ranke.WithClosure(), ranke.WithContent()); err == nil {
		t.Errorf("CopyClaims with missing head should error")
	}
}

// readFull reads len(buf) bytes, tolerating short reads, treating a clean
// EOF after a full read as success.
func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			if total == len(buf) {
				return total, nil
			}
			return total, err
		}
	}
	return total, nil
}
