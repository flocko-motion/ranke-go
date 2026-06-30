package stack_test

import (
	"context"
	"sync"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/adaptertest"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	"github.com/flocko-motion/ranke-go/adapter/storage/stack"
)

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

	got, err := ranke.GetContent(ctx, st, h, uint64(len(b)))
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
	if _, err := ranke.GetContent(ctx, st, h, uint64(len(b))); err != nil {
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
	small, sb := blob(t, "hi")           // 2 bytes
	big, bb := blob(t, "way too long")   // > 4 bytes
	if err := st.PutContents(ctx, []ranke.ContentBlob{{Hash: small, Content: sb}, {Hash: big, Content: bb}}); err != nil {
		t.Fatalf("PutContents: %v", err)
	}
	if !has(t, top, small) || has(t, top, big) {
		t.Fatal("capped top layer should hold small content but skip the large blob")
	}
	if !has(t, bottom, small) || !has(t, bottom, big) {
		t.Fatal("uncapped bottom layer should hold both")
	}
	got, err := ranke.GetContent(ctx, st, big, uint64(len(bb)))
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

	got, err := ranke.GetContent(ctx, st, h, uint64(len(b)))
	if err != nil || string(got) != "treasure" {
		t.Fatalf("GetContent through corruption = %q, %v; want treasure", got, err)
	}
	if top.isCorrupt(h) {
		t.Fatal("read-through should have repaired the corrupt top layer")
	}
	// The repaired top now serves the good bytes directly.
	if direct, err := ranke.GetContent(ctx, top, h, uint64(len(b))); err != nil || string(direct) != "treasure" {
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

	// All-mem: every field but Persistent (mem is ephemeral).
	st, err := stack.NewStack(stack.Eager(mem.New()), stack.Eager(mem.New()))
	if err != nil {
		t.Fatal(err)
	}
	want := ranke.Capabilities{Overwrite: true, Delete: true, Enumerate: true}
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
