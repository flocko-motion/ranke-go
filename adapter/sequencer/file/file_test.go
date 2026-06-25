package file_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/sequencer/file"
)

func TestRoundTripAndClear(t *testing.T) {
	ctx := context.Background()
	h, err := file.New(filepath.Join(t.TempDir(), "branches", "B_h"))
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}

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
	if err := h.Save(ctx, nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	if got, _ := h.Load(ctx); got != nil {
		t.Fatalf("Load after clear = %v, want nil", got)
	}
}
