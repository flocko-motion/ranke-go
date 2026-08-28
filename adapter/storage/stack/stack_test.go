package stack_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage/adaptertest"
	"github.com/rankegraph/ranke-go/adapter/storage/mem"
	"github.com/rankegraph/ranke-go/adapter/storage/minimal"
	"github.com/rankegraph/ranke-go/adapter/storage/stack"
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

// TestDeleteNeedsEveryLayer: a lawful deletion is done only when no layer still serves
// the claim, so a cache that cannot delete would undo it on the next read. The stack
// reports Delete only when all its layers do, and refuses as a whole rather than
// removing from some — a partial removal reported as a deletion is the worst answer.
func TestDeleteNeedsEveryLayer(t *testing.T) {
	ctx := context.Background()
	keeper := minimal.New() // the floor: Overwrite only, no Delete
	st, err := stack.NewStack(mem.New(), keeper)
	if err != nil {
		t.Fatalf("NewStack: %v", err)
	}
	if st.Capabilities().Delete {
		t.Fatal("a stack holding a layer that cannot delete must not claim Delete")
	}
	c := signedClaim(t)
	if err := ranke.PutClaim(ctx, st, c); err != nil {
		t.Fatalf("PutClaim: %v", err)
	}
	if err := st.DeleteClaims(ctx, []ranke.Id{c.ID()}); !errors.Is(err, ranke.ErrUnsupported) {
		t.Fatalf("DeleteClaims = %v, want ErrUnsupported", err)
	}
	// Nothing was removed anywhere: the refusal came before the first deletion.
	has, err := st.HasClaims(ctx, []ranke.Id{c.ID()})
	if err != nil || !has[0] {
		t.Fatalf("the claim must survive a refused deletion (has=%v err=%v)", has, err)
	}

	// The control: every layer able to delete, and the deletion goes through.
	ok, err := stack.NewStack(mem.New(), mem.New())
	if err != nil {
		t.Fatalf("NewStack: %v", err)
	}
	if !ok.Capabilities().Delete {
		t.Fatal("a stack of deleting layers reports Delete")
	}
	if err := ranke.PutClaim(ctx, ok, c); err != nil {
		t.Fatalf("PutClaim: %v", err)
	}
	if err := ok.DeleteClaims(ctx, []ranke.Id{c.ID()}); err != nil {
		t.Fatalf("DeleteClaims: %v", err)
	}
	if has, err := ok.HasClaims(ctx, []ranke.Id{c.ID()}); err != nil || has[0] {
		t.Fatalf("the claim is gone from every layer (has=%v err=%v)", has, err)
	}
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

// TestQueryEnvelopeIsTheStoredRecord: `detail: envelope` through a stack whose engine
// layer keeps no stored bytes must answer with the RawClaims layer's record, byte for
// byte (`R-QCANON`). The engine does the selection — reconstructed claims, or ids
// alone — and the stack re-reads those ids from the layer holding the bytes.
//
// `detail: claims` is the other half: an engine that rebuilds serves it, and what it
// returns is a serialized claim rather than the record an id covers.
func TestQueryEnvelopeIsTheStoredRecord(t *testing.T) {
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
			raw, err := store.GetClaimsRaw(ctx, []ranke.Id{c.ID()})
			if err != nil {
				t.Fatalf("GetClaimsRaw: %v", err)
			}

			read := func(detail ranke.Detail) ranke.QueryResult {
				t.Helper()
				q := ranke.Query{
					Select: ranke.Select{Branch: ranke.BranchUniverse, Head: c.ID()},
					Output: ranke.Output{Detail: detail, Encoding: ranke.ResultCBOR},
				}
				rs, err := st.Query(ctx, q, ranke.Scope{Branch: ranke.BranchUniverse})
				if err != nil {
					t.Fatalf("Query(%s): %v", detail, err)
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
				return got[0]
			}

			// An envelope is the stored record copied out, which only a RawClaims layer
			// holds — so the stack routes past an engine that reconstructs (`R-QCANON`).
			env := read(ranke.DetailEnvelope)
			if env.Kind != ranke.KindClaimEnvelope {
				t.Fatalf("Kind = %q, want %q", env.Kind, ranke.KindClaimEnvelope)
			}
			if !bytes.Equal(env.ClaimEncoded, raw[0]) {
				t.Fatalf("envelope = %d bytes, want the %d stored bytes verbatim",
					len(env.ClaimEncoded), len(raw[0]))
			}

			// A serialized claim is the payload, and carries no such guarantee: the id
			// covers the envelope, not what is inside it.
			claims := read(ranke.DetailClaims)
			if claims.Kind != ranke.KindClaimEncoded {
				t.Fatalf("Kind = %q, want %q", claims.Kind, ranke.KindClaimEncoded)
			}
			if len(claims.ClaimEncoded) == 0 {
				t.Fatal("ClaimEncoded is empty — the cbor read served nothing")
			}
			if bytes.Equal(claims.ClaimEncoded, raw[0]) {
				t.Fatal("the serialized claim must not be mistaken for the stored record")
			}
		})
	}
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
