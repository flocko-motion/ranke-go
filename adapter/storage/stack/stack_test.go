package stack_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"sync"
	"testing"
	"time"

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
		u, err := stack.NewStack(mem.New(), mem.New())
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
	if _, err := stack.NewStack(at(mem.New(), ranke.StorageTierLazy)); err == nil {
		t.Fatal("stack with no authoritative layer should error")
	}
}

// A write stores in every eager layer and passes lazy layers through untouched;
// a subsequent read then fills the lazy layer from below.
func TestWriteThroughAndReadFill(t *testing.T) {
	ctx := context.Background()
	top, bottom := mem.New(), mem.New()
	st, err := stack.NewStack(at(top, ranke.StorageTierLazy), bottom)
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
	healed(t, st)
	if !has(t, top, h) {
		t.Fatal("read miss should have read-filled the lazy top layer")
	}
}

// A content-capped layer (Capabilities.ContentCap) stores content within the cap
// but skips larger blobs; the over-cap blob still lives below and reads through
// transparently. The cap is what the layer's adapter reports — the stack routes
// content by it (an eager cache like neo4j caps its inline content).
func TestContentCapPlacement(t *testing.T) {
	ctx := context.Background()
	// A capped eager layer: keeps verbatim claims but only content up to 4 bytes
	// (no ExternalContent), so it can never be authoritative — it is eager.
	top := capsUniverse{mem.New(), ranke.Capabilities{
		Overwrite: true, Delete: true, Enumerate: true, RawClaims: true,
		ContentCap: 4, Tier: ranke.StorageTierEager,
	}}
	bottom := mem.New() // authoritative, unbounded content
	st, err := stack.NewStack(top, bottom)
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
	st, err := stack.NewStack(top, bottom)
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
	healed(t, st)
	if top.isCorrupt(h) {
		t.Fatal("read-through should have repaired the corrupt top layer")
	}
	// The repaired top now serves the good bytes directly.
	if direct, err := getContent(ctx, top, h, uint64(len(b))); err != nil || string(direct) != "treasure" {
		t.Fatalf("repaired top layer = %q, %v", direct, err)
	}
}

// Sync fills the first eager layer from the layers below it. A claim seeded only
// into the authoritative bottom (past a lazy hot cache and the empty eager layer)
// must land in the eager layer after Sync; with no eager layer it is a no-op.
func TestSyncFillsEagerFromBelow(t *testing.T) {
	ctx := context.Background()
	c := signedClaim(t)

	eager, authoritative := mem.New(), mem.New()
	st, err := stack.NewStack(
		at(mem.New(), ranke.StorageTierLazy),
		at(eager, ranke.StorageTierEager),
		authoritative,
	)
	if err != nil {
		t.Fatalf("NewStack: %v", err)
	}
	// Seed only the authoritative layer — the eager layer starts empty.
	if err := ranke.PutClaim(ctx, authoritative, c); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if present, _ := ranke.HasClaim(ctx, eager, c.ID()); present {
		t.Fatal("precondition: eager layer should start without the claim")
	}
	if res := <-st.Sync(ctx, nil, c.ID()); res.Err != nil {
		t.Fatalf("Sync: %v", res.Err)
	}
	if present, _ := ranke.HasClaim(ctx, eager, c.ID()); !present {
		t.Fatal("Sync should have filled the eager layer from below")
	}

	// No eager layer (all authoritative) → trivially synced.
	st2, err := stack.NewStack(mem.New(), mem.New())
	if err != nil {
		t.Fatalf("NewStack: %v", err)
	}
	if res := <-st2.Sync(ctx, nil, c.ID()); res.Err != nil || res.SyncedTo == nil {
		t.Fatalf("Sync with no eager layer = %+v", res)
	}
}

// signedClaim builds a self-contained initial contributor claim (no references).
func signedClaim(t *testing.T) ranke.Claim {
	t.Helper()
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
	return c
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

// healed waits for the stack's background filler to land every fill it was
// offered, so a read-through repair is observable.
func healed(t *testing.T, u ranke.Universe) {
	t.Helper()
	h, ok := u.(stack.Healing)
	if !ok {
		t.Fatal("the stack must report on its background filler")
	}
	for range 200 {
		if h.HealIdle() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the background filler did not settle")
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

// at returns u reporting write tier t while keeping its other capabilities —
// for building stack topologies in tests from adapters (like mem) that hardcode
// their own tier.
func at(u ranke.Universe, t ranke.StorageTier) ranke.Universe {
	c := u.Capabilities()
	c.Tier = t
	return capsUniverse{u, c}
}

func TestCapabilitiesDerivation(t *testing.T) {
	// All-mem: every field but Persistent (mem is ephemeral). mem keeps verbatim
	// claim bytes and holds tags, and a stack with an authoritative layer reports
	// itself authoritative.
	st, err := stack.NewStack(mem.New(), mem.New())
	if err != nil {
		t.Fatal(err)
	}
	want := ranke.Capabilities{
		Overwrite: true, Delete: true, Enumerate: true, RawClaims: true,
		ExternalContent: true, Tags: true, Tier: ranke.StorageTierAuthoritative,
	}
	if got := st.Capabilities(); got != want {
		t.Fatalf("all-mem stack caps = %+v, want %+v", got, want)
	}

	// A persistent AUTHORITATIVE layer (under a lazy cache) makes the stack
	// persistent — the source of truth is what survives a restart.
	durable := capsUniverse{mem.New(), ranke.Capabilities{
		Overwrite: true, Delete: true, Enumerate: true, Persistent: true,
		RawClaims: true, ExternalContent: true, Tier: ranke.StorageTierAuthoritative,
	}}
	st2, err := stack.NewStack(at(mem.New(), ranke.StorageTierLazy), durable)
	if err != nil {
		t.Fatal(err)
	}
	if c := st2.Capabilities(); !c.Persistent || !c.Enumerate || !c.Overwrite || !c.Delete {
		t.Fatalf("durable-backed stack caps = %+v", c)
	}

	// One non-deleting layer makes the whole stack non-deletable (a copy would linger).
	st3, err := stack.NewStack(capsUniverse{mem.New(), ranke.Capabilities{Overwrite: true, Tier: ranke.StorageTierLazy}}, mem.New())
	if err != nil {
		t.Fatal(err)
	}
	if st3.Capabilities().Delete {
		t.Fatal("a non-deleting layer should make the stack non-deletable")
	}

	// An eager layer that can't overwrite means the durable tier can't be repaired.
	st4, err := stack.NewStack(capsUniverse{mem.New(), ranke.Capabilities{Delete: true, Enumerate: true, Persistent: true, Tier: ranke.StorageTierEager}}, mem.New())
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
	c.Tier = ranke.StorageTierEager // a lossy projection can't be authoritative (like neo4j)
	return c
}

// projectionEngine is a structure-only cache in the engine seat: its Query
// reconstructs each claim from parts, so a result carries no stored record — the
// shape a graph-native engine (neo4j) returns.
type projectionEngine struct{ structOnlyCache }

func (p projectionEngine) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	return reshape(ctx, p.Universe, q, scope, func(r ranke.QueryResult) (ranke.QueryResult, error) {
		if r.ClaimNative == nil {
			return r, nil
		}
		c, err := stripContent(r.ClaimNative)
		if err != nil {
			return r, err
		}
		r.ClaimNative = c
		return r, nil
	})
}

// idOnlyEngine answers with identities alone, holding no canonical bytes to
// serialise — what neo4j does for a cbor read: it selects, a byte layer encodes.
type idOnlyEngine struct{ structOnlyCache }

func (e idOnlyEngine) Query(ctx context.Context, q ranke.Query, scope ranke.Scope) (ranke.ResultStream, error) {
	return reshape(ctx, e.Universe, q, scope, func(r ranke.QueryResult) (ranke.QueryResult, error) {
		return ranke.QueryResult{Kind: ranke.KindClaimId, ClaimId: r.ClaimId, PathId: r.PathId}, nil
	})
}

// reshape runs the query on u and rewrites every result through f.
func reshape(ctx context.Context, u ranke.Universe, q ranke.Query, scope ranke.Scope,
	f func(ranke.QueryResult) (ranke.QueryResult, error)) (ranke.ResultStream, error) {
	rs, err := u.Query(ctx, q, scope)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []ranke.QueryResult
	for rs.Next() {
		r, err := f(rs.Result())
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	return &sliceStream{results: out}, nil
}

// sliceStream serves an already-assembled result slice.
type sliceStream struct {
	results []ranke.QueryResult
	i       int
}

func (s *sliceStream) Next() bool {
	s.i++
	return s.i <= len(s.results)
}
func (s *sliceStream) Result() ranke.QueryResult  { return s.results[s.i-1] }
func (s *sliceStream) Report() *ranke.QueryReport { return nil }
func (s *sliceStream) Err() error                 { return nil }
func (s *sliceStream) Close() error               { return nil }

// TestQueryCBORIsTheStoredRecord: the canonical read (`R-QCANON`) through a stack whose
// engine layer keeps no canonical bytes must answer with the RawClaims layer's stored
// record, byte for byte. The engine does the selection — reconstructed claims, or ids
// alone — and the stack re-reads those ids from the layer that holds the bytes.
//
// Content in full is part of that form: a claim's inline content is inside S(v), so a
// read that caps it cannot deliver the bytes an id was computed over.
func TestQueryCBORIsTheStoredRecord(t *testing.T) {
	ctx := context.Background()
	engines := map[string]func(ranke.Universe) ranke.Universe{
		"reconstructed claims": func(u ranke.Universe) ranke.Universe { return projectionEngine{structOnlyCache{u}} },
		"ids only":             func(u ranke.Universe) ranke.Universe { return idOnlyEngine{structOnlyCache{u}} },
	}
	for name, engine := range engines {
		t.Run(name, func(t *testing.T) {
			store := mem.New()
			st, err := stack.NewStack(engine(mem.New()), store)
			if err != nil {
				t.Fatalf("NewStack: %v", err)
			}
			c := signedClaim(t)
			if err := ranke.PutClaim(ctx, st, c); err != nil {
				t.Fatalf("PutClaim: %v", err)
			}
			q := ranke.Query{
				Select: ranke.Select{Branch: ranke.BranchUniverse, Head: c.ID()},
				Output: ranke.Output{
					Detail: ranke.DetailClaims, Form: ranke.FormOriginal,
					Encoding: ranke.ResultCBOR, Content: &ranke.OutputContent{Max: 0},
				},
			}
			rs, err := st.Query(ctx, q, ranke.Scope{Branch: ranke.BranchUniverse})
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			var got []ranke.QueryResult
			for rs.Next() {
				got = append(got, rs.Result())
			}
			if err := rs.Err(); err != nil {
				t.Fatalf("stream: %v", err)
			}
			if err := rs.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("results = %d, want the one claim", len(got))
			}
			if got[0].Kind != ranke.KindClaimEncoded {
				t.Fatalf("Kind = %q, want %q", got[0].Kind, ranke.KindClaimEncoded)
			}
			raw, err := store.GetClaimsRaw(ctx, []ranke.Id{c.ID()})
			if err != nil {
				t.Fatalf("GetClaimsRaw: %v", err)
			}
			if len(got[0].ClaimEncoded) == 0 {
				t.Fatal("ClaimEncoded is empty — the cbor read served nothing")
			}
			if !bytes.Equal(got[0].ClaimEncoded, raw[0]) {
				t.Fatalf("ClaimEncoded = %d bytes, want the %d stored bytes verbatim",
					len(got[0].ClaimEncoded), len(raw[0]))
			}
		})
	}
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
	st, err := stack.NewStack(cache, store)
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
