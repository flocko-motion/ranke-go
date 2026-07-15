package partition_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/storage/adaptertest"
	"github.com/flocko-motion/ranke-go/adapter/storage/mem"
	"github.com/flocko-motion/ranke-go/adapter/storage/partition"
)

// TestConformance runs the shared black-box Universe suite against a 3-shard
// partition — the composite must satisfy the full contract like any backend.
func TestConformance(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) ranke.Universe {
		p, err := partition.NewPartition(mem.New(), mem.New(), mem.New())
		if err != nil {
			t.Fatalf("NewPartition: %v", err)
		}
		return p
	})
}

func TestNewPartitionValidation(t *testing.T) {
	if _, err := partition.NewPartition(); err == nil {
		t.Fatal("empty partition should error")
	}
}

func TestSingleShardUnwrapped(t *testing.T) {
	u := mem.New()
	p, err := partition.NewPartition(u)
	if err != nil {
		t.Fatal(err)
	}
	if p != u {
		t.Fatal("a single-shard partition should return the shard itself, unwrapped")
	}
}

// Each key lands on exactly one shard (disjoint), and routing is deterministic.
func TestRoutingDisjointAndDeterministic(t *testing.T) {
	ctx := context.Background()
	shards := []ranke.Universe{mem.New(), mem.New(), mem.New()}
	p, err := partition.NewPartition(shards...)
	if err != nil {
		t.Fatalf("NewPartition: %v", err)
	}
	h, b := blob(t, "alice-likes-apple")
	if err := p.PutContents(ctx, []ranke.ContentBlob{{Hash: h, Content: b}}); err != nil {
		t.Fatalf("PutContents: %v", err)
	}
	count := 0
	for _, s := range shards {
		if has(t, s, h) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("key landed on %d shards, want exactly 1", count)
	}
	// Read it back through the partition (routes to the same shard).
	got, err := ranke.GetContent(ctx, p, h, uint64(len(b)))
	if err != nil || string(got) != "alice-likes-apple" {
		t.Fatalf("GetContent = %q, %v", got, err)
	}
}

// Many keys spread across the shards rather than piling onto one.
func TestDistribution(t *testing.T) {
	ctx := context.Background()
	shards := []ranke.Universe{mem.New(), mem.New(), mem.New()}
	p, err := partition.NewPartition(shards...)
	if err != nil {
		t.Fatalf("NewPartition: %v", err)
	}
	for i := 0; i < 60; i++ {
		h, b := blob(t, fmt.Sprintf("item-%d", i))
		if err := p.PutContents(ctx, []ranke.ContentBlob{{Hash: h, Content: b}}); err != nil {
			t.Fatalf("PutContents: %v", err)
		}
	}
	used := 0
	for _, s := range shards {
		if n := countContents(t, s); n > 0 {
			used++
		}
	}
	if used < 2 {
		t.Fatalf("60 keys used only %d shard(s); expected a spread", used)
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

// countContents reports how many of a probe set the shard holds — a coarse
// "is this shard used" signal for the distribution test.
func countContents(t *testing.T, u ranke.Universe) int {
	t.Helper()
	n := 0
	for i := 0; i < 60; i++ {
		h, _ := blob(t, fmt.Sprintf("item-%d", i))
		if has(t, u, h) {
			n++
		}
	}
	return n
}

// capsUniverse overrides a Universe's reported capabilities (for derivation tests).
type capsUniverse struct {
	ranke.Universe
	caps ranke.Capabilities
}

func (c capsUniverse) Capabilities() ranke.Capabilities { return c.caps }

func TestCapabilities(t *testing.T) {
	// A partition has a capability only if every shard does (a key may land on any).
	p, err := partition.NewPartition(mem.New(), mem.New(), mem.New())
	if err != nil {
		t.Fatal(err)
	}
	want := ranke.Capabilities{Overwrite: true, Delete: true, Enumerate: true, Tags: true} // mem: not persistent, but tag-capable (routed per shard)
	if got := p.Capabilities(); got != want {
		t.Fatalf("all-mem partition caps = %+v, want %+v", got, want)
	}

	// One non-deleting shard makes the whole partition non-deleting.
	p2, err := partition.NewPartition(mem.New(), capsUniverse{mem.New(), ranke.Capabilities{Overwrite: true}})
	if err != nil {
		t.Fatal(err)
	}
	if p2.Capabilities().Delete {
		t.Fatal("a non-deleting shard should make the partition non-deleting")
	}
}
