// package: adaptertest / conformance
// type:    test
// job:     black-box conformance suite exercising any ranke.Universe via the public API
// limits:  no medium-specific scenarios; each adapter adds those alongside
// (-> adapter/storage/fs, adapter/storage/mem)
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
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"

	"github.com/flocko-motion/ranke-go"
)

// Factory returns a fresh, empty Universe. Run calls it several times, so each
// call must yield an independent store.
type Factory func(t *testing.T) ranke.Universe

// Run executes the full black-box conformance suite against universes
// produced by newU.
func Run(t *testing.T, newU Factory) {
	t.Run("claims bulk round-trip", func(t *testing.T) { testClaimsRoundTrip(t, newU) })
	t.Run("content round-trip and stream", func(t *testing.T) { testContentRoundTrip(t, newU) })
	t.Run("absent lookups", func(t *testing.T) { testAbsentLookups(t, newU) })
	t.Run("copy closure merges provenance", func(t *testing.T) { testCopyClosure(t, newU) })
	t.Run("capabilities", func(t *testing.T) { testCapabilities(t, newU) })
	t.Run("sync reports readiness", func(t *testing.T) { testSync(t, newU) })
}

// testSync checks Sync delivers one result: an in-tree backend holds the whole
// archive, so it reports synced up to the requested id.
func testSync(t *testing.T, newU Factory) {
	u := newU(t)
	defer u.Close()
	ctx := context.Background()
	op, em, _, _, _ := sample(t)
	if err := u.PutClaims(ctx, []ranke.Claim{op, em}); err != nil {
		t.Fatalf("PutClaims: %v", err)
	}
	res := <-u.Sync(ctx, nil, em.ID())
	if res.Err != nil {
		t.Fatalf("Sync: %v", res.Err)
	}
	if res.SyncedTo == nil || res.SyncedTo.String() != em.ID().String() {
		t.Fatalf("Sync SyncedTo = %v, want %v", res.SyncedTo, em.ID())
	}
}

// testCapabilities requires Overwrite, which the read-through repair in
// adapter/storage/stack relies on.
func testCapabilities(t *testing.T, newU Factory) {
	u := newU(t)
	defer u.Close()
	if !u.Capabilities().Overwrite {
		t.Fatal("Capabilities: expected Overwrite=true for an in-tree backend")
	}
}

// sample builds a tiny signed graph via the public builder: a contributor op, an
// inline source em, and blob, an external source returned with its hash + bytes.
func sample(t *testing.T) (op ranke.Contributor, em, blob ranke.Claim, blobHash ranke.Id, blobBytes []byte) {
	t.Helper()
	ctx := context.Background()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	if err != nil {
		t.Fatalf("encode pubkey: %v", err)
	}
	// A contributor's pubkey is its content (§5.7).
	opClaim, err := ranke.NewClaim(ranke.NodeContributor, nil).WithInlineContent(pubkey).WithEncoding(ranke.EncodingOctetStream).Sign(priv)
	if err != nil {
		t.Fatalf("contributor: %v", err)
	}
	op, err = opClaim.AsContributor(ctx, nil, priv) // inline pubkey → no Universe needed
	if err != nil {
		t.Fatalf("AsContributor: %v", err)
	}

	// A referencing claim must declare its height; HeightOf derives it.
	em, err = ranke.NewClaim(ranke.TypeSource("note"), op).
		WithInlineContent([]byte("hi")).
		WithEncoding(ranke.EncodingPlain).
		WithHeight(ranke.HeightOf(op)).
		Sign()
	if err != nil {
		t.Fatalf("inline source: %v", err)
	}

	blobBytes = []byte("external blob payload")
	blobHash, err = ranke.HashContent(blobBytes)
	if err != nil {
		t.Fatalf("hash content: %v", err)
	}
	blob, err = ranke.NewClaim(ranke.TypeSource("blob"), op).
		WithExternalContent(blobHash, uint64(len(blobBytes))).
		WithEncoding(ranke.EncodingOctetStream).
		WithHeight(ranke.HeightOf(op)).
		Sign()
	if err != nil {
		t.Fatalf("external source: %v", err)
	}
	return op, em, blob, blobHash, blobBytes
}

func testClaimsRoundTrip(t *testing.T, newU Factory) {
	ctx := context.Background()
	u := newU(t)
	defer u.Close()
	op, em, _, _, _ := sample(t)

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

	bs, err := u.GetContents(ctx, []ranke.ContentRef{{Hash: h, ContentSize: size}})
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
	_, em, _, _, _ := sample(t)

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

// testCopyClosure copies a provenance closure between Universes and checks the
// destination holds all of it: the referenced contributor and the content blob.
func testCopyClosure(t *testing.T, newU Factory) {
	ctx := context.Background()
	src := newU(t)
	defer src.Close()
	dst := newU(t)
	defer dst.Close()

	op, em, blob, blobHash, blobBytes := sample(t)

	if err := src.PutClaims(ctx, []ranke.Claim{op, em, blob}); err != nil {
		t.Fatalf("src PutClaims: %v", err)
	}
	if err := src.PutContents(ctx, []ranke.ContentBlob{{Hash: blobHash, Content: blobBytes}}); err != nil {
		t.Fatalf("src PutContents: %v", err)
	}

	// Copy the closure of em and blob, so their contributor op comes along.
	heads := []ranke.Id{em.ID(), blob.ID()}
	if err := dst.CopyClaims(ctx, src, heads, ranke.WithClosure(), ranke.WithContent()); err != nil {
		t.Fatalf("CopyClaims: %v", err)
	}

	has, err := dst.HasClaims(ctx, []ranke.Id{op.ID(), em.ID(), blob.ID()})
	if err != nil {
		t.Fatalf("dst HasClaims: %v", err)
	}
	for i, id := range []ranke.Id{op.ID(), em.ID(), blob.ID()} {
		if !has[i] {
			t.Errorf("dst closure missing %s", id.String())
		}
	}
	cHas, err := dst.HasContents(ctx, []ranke.Id{blobHash})
	if err != nil {
		t.Fatalf("dst HasContents: %v", err)
	}
	if !cHas[0] {
		t.Errorf("dst missing the copied external content")
	}

	// Idempotent re-copy.
	if err := dst.CopyClaims(ctx, src, heads, ranke.WithClosure(), ranke.WithContent()); err != nil {
		t.Fatalf("CopyClaims (rerun): %v", err)
	}

	// A head absent from src must surface an error.
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	pubkey, _ := ranke.EncodePublicKey(priv.Public())
	orphan, err := ranke.NewClaim(ranke.NodeContributor, nil).WithInlineContent(pubkey).WithEncoding(ranke.EncodingOctetStream).Sign(priv)
	if err != nil {
		t.Fatalf("synthesize orphan: %v", err)
	}
	if err := dst.CopyClaims(ctx, src, []ranke.Id{orphan.ID()}, ranke.WithClosure(), ranke.WithContent()); err == nil {
		t.Errorf("CopyClaims with a head missing from src should error")
	}
}

// readFull reads len(buf) bytes, tolerating short reads and a trailing EOF.
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
