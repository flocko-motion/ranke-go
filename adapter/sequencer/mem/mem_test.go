package mem_test

import (
	"context"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/sequencer/mem"
)

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	h := mem.New()

	if got, _ := h.Load(ctx); got != nil {
		t.Fatalf("Load (empty) = %v, want nil", got)
	}
	id, err := ranke.HashContent([]byte("a branch table claim"))
	if err != nil {
		t.Fatalf("HashContent: %v", err)
	}
	if err := h.Save(ctx, id); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got, _ := h.Load(ctx); got == nil || !got.Equal(id) {
		t.Fatalf("Load = %v, want %v", got, id)
	}
}
