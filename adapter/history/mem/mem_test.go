package mem_test

import (
	"context"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/history/mem"
)

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	h := mem.New()

	if n, _ := h.Len(ctx); n != 0 {
		t.Fatalf("Len (empty) = %d, want 0", n)
	}

	id, err := ranke.HashContent([]byte("a branch table claim"))
	if err != nil {
		t.Fatalf("HashContent: %v", err)
	}
	item, err := h.Append(ctx, id, 0, 0)
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	if item.GetHeight() != 0 || !item.GetId().Equal(id) || item.GetTimestamp().IsZero() {
		t.Fatalf("Append = {%v, h=%d, ts=%v}, want {id, h=0, non-zero ts}", item.GetId(), item.GetHeight(), item.GetTimestamp())
	}

	latest, _ := h.Latest(ctx)
	if !latest.GetId().Equal(id) {
		t.Fatalf("Latest = %v, want %v", latest.GetId(), id)
	}
	at0, err := h.GetAtRevision(ctx, 0)
	if err != nil || !at0.GetId().Equal(id) {
		t.Fatalf("GetAtRevision(0) = %v (%v), want %v", at0.GetId(), err, id)
	}

	bulk, err := h.GetBulk(ctx, 0, 1)
	if err != nil || len(bulk) != 1 || !bulk[0].GetId().Equal(id) {
		t.Fatalf("GetBulk(0,1) = %v (%v), want [%v]", bulk, err, id)
	}
}
