package stack_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"sync"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/adaptertest"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	"github.com/flocko-motion/ranke-go/adapter/storage/stack"
)

// getContent fetches one blob by hash via the content-plane primitive (the
// single-item ranke.GetContent helper was removed in favour of claim-addressed
// reads; blob-plane tests use GetContents directly).
func getContent(ctx context.Context, u ranke.Universe, h ranke.Id, size uint64) ([]byte, error) {
	bs, err := u.GetContents(ctx, []ranke.ContentRef{{Hash: h, ContentSize: size}})
	if err != nil {
		return nil, err
	}
	return bs[0], nil
}

// TestConformance runs the shared black-box Universe suite against a two-layer
// eager stack — the composite must satisfy the full contract like any backend.
func TestConformance(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) ranke.Universe {
		u, err := stack.NewStack(stack.Eager(mem.New()), stack.Eager(mem.New()))
		if err != nil {
			t.Fatalf("NewStack: %v", err)
		}
		return u
	})
}

func TestNewStackValidation(t *testing.T) {
	if _, err := stack.NewStack(); err == nil {
		t.Fatal("empty stack should error")
	}
	if _, err := stack.NewStack(stack.Lazy(mem.New())); err == nil {
		t.Fatal("stack with no eager layer should error")
	}
}

// A write stores in every eager layer and passes lazy layers through untouched;
// a subsequent read then fills the lazy layer from below.
func TestWriteThroughAndReadFill(t *testing.T) {
	ctx := context.Background()
	top, bottom := mem.New(), mem.New()
	st, err := stack.NewStack(stack.Lazy(top), stack.Eager(bottom))
	if err != nil {
		t.Fatalf("NewStack: %v", err)
	}
	h, b := blob(t, "alice")
	if err := st.PutContents(ctx, []ranke.ContentBlob{{Hash: h, Content: b}}); err != nil {
		t.Fatalf("PutContents: %v", err)
	}
	if !has(t, bottom, h) {
		t.Fatal("eager bottom layer should hold the write")
	}
	if has(t, top, h) {
		t.Fatal("lazy top layer should NOT be written on a write")
	}

	got, err := getContent(ctx, st, h, uint64(len(b)))
	if err != nil || string(got) != "alice" {
		t.Fatalf("GetContent = %q, %v; want alice", got, err)
	}
	if !has(t, top, h) {
		t.Fatal("read miss should have read-filled the lazy top layer")
	}
}

func TestNoReadFill(t *testing.T) {
	ctx := context.Background()
	top, bottom := mem.New(), mem.New()
	st, err := stack.NewStack(stack.Lazy(top, stack.NoReadFill()), stack.Eager(bottom))
	if err != nil {
		t.Fatalf("NewStack: %v", err)
	}
	h, b := blob(t, "bob")
	mustPut(t, st, h, b)
	if _, err := getContent(ctx, st, h, uint64(len(b))); err != nil {
		t.Fatalf("GetContent: %v", err)
	}
	if has(t, top, h) {
		t.Fatal("a NoReadFill layer must not be populated by reads")
	}
}

// A size-capped layer stores content within the cap but skips larger blobs;
// the over-cap blob still lives below and reads through transparently.
func TestMaxContentSize(t *testing.T) {
	ctx := context.Background()
	top, bottom := mem.New(), mem.New()
	st, err := stack.NewStack(stack.Eager(top, stack.MaxContentSize(4)), stack.Eager(bottom))
	if err != nil {
		t.Fatalf("NewStack: %v", err)
	}
	small, sb := blob(t, "hi")         // 2 bytes
	big, bb := blob(t, "way too long") // > 4 bytes
	if err := st.PutContents(ctx, []ranke.ContentBlob{{Hash: small, Content: sb}, {Hash: big, Content: bb}}); err != nil {
		t.Fatalf("PutContents: %v", err)
	}
	if !has(t, top, small) || has(t, top, big) {
		t.Fatal("capped top layer should hold small content but skip the large blob")
	}
	if !has(t, bottom, small) || !has(t, bottom, big) {
		t.Fatal("uncapped bottom layer should hold both")
	}
	got, err := getContent(ctx, st, big, uint64(len(bb)))
	if err != nil || string(got) != "way too long" {
		t.Fatalf("read of over-cap blob = %q, %v", got, err)
	}
}

// Byte corruption at an upper layer is a storage failure: the stack serves the
// good copy from below and repairs the corrupt layer in place (read-through
// repair, enabled by overwrite Put).
func TestRepairViaReadThrough(t *testing.T) {
	ctx := context.Background()
	top := newCorrupt()
	bottom := mem.New()
	st, err := stack.NewStack(stack.Eager(top), stack.Eager(bottom))
	if err != nil {
		t.Fatalf("NewStack: %v", err)
	}
	h, b := blob(t, "treasure")
	if err := st.PutContents(ctx, []ranke.ContentBlob{{Hash: h, Content: b}}); err != nil {
		t.Fatalf("PutContents: %v", err)
	}
	top.flag(h) // top's stored bytes go bad

	got, err := getContent(ctx, st, h, uint64(len(b)))
	if err != nil || string(got) != "treasure" {
		t.Fatalf("GetContent through corruption = %q, %v; want treasure", got, err)
	}
	if top.isCorrupt(h) {
		t.Fatal("read-through should have repaired the corrupt top layer")
	}
	// The repaired top now serves the good bytes directly.
	if direct, err := getContent(ctx, top, h, uint64(len(b))); err != nil || string(direct) != "treasure" {
		t.Fatalf("repaired top layer = %q, %v", direct, err)
	}
}

// --- helpers ---

func blob(t *testing.T, s string) (ranke.Id, []byte) {
	t.Helper()
	b := []byte(s)
	h, err := ranke.HashContent(b)
	if err != nil {
		t.Fatalf("HashContent: %v", err)
	}
	return h, b
}

func has(t *testing.T, u ranke.Universe, h ranke.Id) bool {
	t.Helper()
	ok, err := ranke.HasContent(context.Background(), u, h)
	if err != nil {
		t.Fatalf("HasContent: %v", err)
	}
	return ok
}

func mustPut(t *testing.T, u ranke.Universe, h ranke.Id, b []byte) {
	t.Helper()
	if err := u.PutContents(context.Background(), []ranke.ContentBlob{{Hash: h, Content: b}}); err != nil {
		t.Fatalf("PutContents: %v", err)
	}
}

// corruptUniverse wraps a real Universe and simulates byte corruption: a flagged
// hash reads back as ranke.ErrIntegrity until a write (the stack's repair) clears it.
type corruptUniverse struct {
	ranke.Universe
	mu      sync.Mutex
	corrupt map[string]bool
}

func newCorrupt() *corruptUniverse {
	return &corruptUniverse{Universe: mem.New(), corrupt: map[string]bool{}}
}

func (c *corruptUniverse) flag(h ranke.Id) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.corrupt[h.String()] = true
}

func (c *corruptUniverse) isCorrupt(h ranke.Id) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.corrupt[h.String()]
}

func (c *corruptUniverse) GetContents(ctx context.Context, refs []ranke.ContentRef) ([][]byte, error) {
	c.mu.Lock()
	for _, r := range refs {
		if c.corrupt[r.Hash.String()] {
			c.mu.Unlock()
			return nil, ranke.ErrIntegrity
		}
	}
	c.mu.Unlock()
	return c.Universe.GetContents(ctx, refs)
}

func (c *corruptUniverse) PutContents(ctx context.Context, blobs []ranke.ContentBlob) error {
	c.mu.Lock()
	for _, b := range blobs {
		delete(c.corrupt, b.Hash.String())
	}
	c.mu.Unlock()
	return c.Universe.PutContents(ctx, blobs)
}

// capsUniverse overrides a Universe's reported capabilities (for derivation tests).
type capsUniverse struct {
	ranke.Universe
	caps ranke.Capabilities
}

func (c capsUniverse) Capabilities() ranke.Capabilities { return c.caps }

func TestCapabilitiesDerivation(t *testing.T) {
	full := ranke.Capabilities{Overwrite: true, Delete: true, Enumerate: true, Persistent: true}

	// All-mem: every field but Persistent (mem is ephemeral). mem keeps verbatim
	// claim bytes, so RawClaims holds.
	st, err := stack.NewStack(stack.Eager(mem.New()), stack.Eager(mem.New()))
	if err != nil {
		t.Fatal(err)
	}
	want := ranke.Capabilities{Overwrite: true, Delete: true, Enumerate: true, RawClaims: true, ExternalContent: true}
	if got := st.Capabilities(); got != want {
		t.Fatalf("all-mem stack caps = %+v, want %+v", got, want)
	}

	// A persistent eager layer (under a mem cache) makes the stack persistent.
	st2, err := stack.NewStack(stack.Lazy(mem.New()), stack.Eager(capsUniverse{mem.New(), full}))
	if err != nil {
		t.Fatal(err)
	}
	if c := st2.Capabilities(); !c.Persistent || !c.Enumerate || !c.Overwrite || !c.Delete {
		t.Fatalf("durable-backed stack caps = %+v", c)
	}

	// One non-deleting layer makes the whole stack non-deletable (a copy would linger).
	st3, err := stack.NewStack(stack.Lazy(capsUniverse{mem.New(), ranke.Capabilities{Overwrite: true}}), stack.Eager(mem.New()))
	if err != nil {
		t.Fatal(err)
	}
	if st3.Capabilities().Delete {
		t.Fatal("a non-deleting layer should make the stack non-deletable")
	}

	// An eager layer that can't overwrite means the durable tier can't be repaired.
	st4, err := stack.NewStack(stack.Eager(capsUniverse{mem.New(), ranke.Capabilities{Delete: true, Enumerate: true, Persistent: true}}), stack.Eager(mem.New()))
	if err != nil {
		t.Fatal(err)
	}
	if st4.Capabilities().Overwrite {
		t.Fatal("a non-overwriting eager layer should make the stack non-overwriting")
	}
}

// --- structure-only cache: models a neo4j-style cache in a stack ---
//
// It holds claim STRUCTURE but reconstructs claims WITHOUT their inline content
// bytes (like neo4j, which cannot inline binary content), and keeps no verbatim
// CBOR (RawClaims=false) and no content blobs (ExternalContent=false). So a
// content read cannot be served from this layer: it must fall through to the
// byte layer below, addressed BY CLAIM (GetClaimContent) — a bare hash lookup
// would miss, since inline content is not a standalone blob.
type structOnlyCache struct{ ranke.Universe }

func (s structOnlyCache) GetClaims(ctx context.Context, ids []ranke.Id, opts ...ranke.GetOption) ([]ranke.Claim, error) {
	cs, err := s.Universe.GetClaims(ctx, ids, opts...)
	if err != nil {
		return nil, err
	}
	for i, c := range cs {
		stripped, err := stripContent(c)
		if err != nil {
			return nil, err
		}
		cs[i] = stripped
	}
	return cs, nil
}

func (s structOnlyCache) GetClaimsRaw(context.Context, []ranke.Id) ([][]byte, error) {
	return nil, ranke.ErrNotFound // structure-only: keeps no verbatim CBOR
}

func (s structOnlyCache) GetContents(context.Context, []ranke.ContentRef) ([][]byte, error) {
	return nil, ranke.ErrNotFound // holds no content blobs
}

func (s structOnlyCache) HasContents(_ context.Context, hashes []ranke.Id) ([]bool, error) {
	return make([]bool, len(hashes)), nil
}

func (s structOnlyCache) StreamContent(context.Context, ranke.Id, uint64) (io.ReadCloser, error) {
	return nil, ranke.ErrNotFound
}

func (s structOnlyCache) Capabilities() ranke.Capabilities {
	c := s.Universe.Capabilities()
	c.RawClaims = false
	c.ExternalContent = false
	c.ContentCap = 0
	return c
}

type fielder interface {
	Fields() []string
	GetField(string) (string, error)
}

func fieldsOf(f fielder) map[string]string {
	names := f.Fields()
	if len(names) == 0 {
		return nil
	}
	m := make(map[string]string, len(names))
	for _, k := range names {
		v, _ := f.GetField(k)
		m[k] = v
	}
	return m
}

// stripContent rebuilds a claim from its parts WITHOUT the inline content bytes
// (InlineContent omitted) — exactly how a structure-only cache reconstructs a
// claim it holds but whose binary content it never inlined.
func stripContent(c ranke.Claim) (ranke.Claim, error) {
	n := c.Node()
	parts := ranke.ClaimParts{
		ID:          n.ID(),
		Type:        n.Type(),
		Encoding:    n.Encoding(),
		CreatedAt:   n.CreatedAt(),
		Height:      n.Height(),
		ContentHash: n.GetContentHash(),
		ContentSize: n.GetContentSize(),
		Fields:      fieldsOf(n),
	}
	for _, e := range c.Edges() {
		parts.Edges = append(parts.Edges, ranke.EdgeParts{
			ID:                e.ID(),
			Reference:         e.Reference(),
			Type:              e.Type(),
			Encoding:          e.Encoding(),
			RelationDirection: e.RelationDirection(),
			ContentHash:       e.GetContentHash(),
			ContentSize:       e.GetContentSize(),
			Fields:            fieldsOf(e),
		})
	}
	return ranke.AssembleClaim(parts)
}

// TestStackResolvesContributorContentByClaim stores a contributor (whose content
// is its binary, octet-stream pubkey — content a structure-only cache cannot
// inline) into a neo4j/mem-shaped stack and reads its content back by claim. The
// top cache returns the contributor without its pubkey bytes; resolving BY CLAIM
// must recover them from the byte layer. A bare hash lookup would miss, since
// inline content is not a standalone blob — this is the regression the neo4j/mem
// perf run hit.
func TestStackResolvesContributorContentByClaim(t *testing.T) {
	ctx := context.Background()
	cache := structOnlyCache{mem.New()}
	store := mem.New()
	st, err := stack.NewStack(stack.Eager(cache), stack.Eager(store))
	if err != nil {
		t.Fatalf("NewStack: %v", err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pubkey, err := ranke.EncodePublicKey(priv.Public())
	if err != nil {
		t.Fatalf("EncodePublicKey: %v", err)
	}
	c, err := ranke.NewClaim(ranke.NodeContributor, nil).
		WithInlineContent(pubkey).
		WithEncoding(ranke.EncodingOctetStream).
		Sign(priv)
	if err != nil {
		t.Fatalf("build contributor: %v", err)
	}
	if err := ranke.PutClaim(ctx, st, c); err != nil {
		t.Fatalf("PutClaim: %v", err)
	}

	// Sanity: reading the claim through the stack yields the structure-only
	// (content-stripped) view from the top cache — no inline bytes in hand.
	got, err := ranke.GetClaim(ctx, st, c.ID())
	if err != nil {
		t.Fatalf("GetClaim: %v", err)
	}
	if b, _ := got.Node().GetInlineContent(); len(b) != 0 {
		t.Fatalf("expected the cache to return a content-stripped claim, got %d inline bytes", len(b))
	}

	// Reading content from the claim in hand recovers the pubkey: the
	// structure-only view falls through to the byte layer.
	rc, err := got.GetContent(ctx, st)
	if err != nil {
		t.Fatalf("GetContent: %v", err)
	}
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read content: %v", err)
	}
	if !bytes.Equal(data, pubkey) {
		t.Fatalf("content = %d bytes, want the %d-byte pubkey", len(data), len(pubkey))
	}
}
