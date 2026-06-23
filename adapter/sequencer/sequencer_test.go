package sequencer_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/sequencer"
)

// roundTrip exercises the BranchTableHead contract: empty load is nil, a
// saved id reloads, and clearing returns to nil.
func roundTrip(t *testing.T, h ranke.BranchTableHead) {
	t.Helper()
	ctx := context.Background()

	got, err := h.Load(ctx)
	if err != nil {
		t.Fatalf("Load (empty): %v", err)
	}
	if got != nil {
		t.Fatalf("Load (empty) = %v, want nil", got)
	}

	id, err := ranke.HashContent([]byte("a branch table claim"))
	if err != nil {
		t.Fatalf("HashContent: %v", err)
	}
	if err := h.Save(ctx, id); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err = h.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil || !got.Equal(id) {
		t.Fatalf("Load = %v, want %v", got, id)
	}

	if err := h.Save(ctx, nil); err != nil {
		t.Fatalf("Save(nil): %v", err)
	}
	if got, _ := h.Load(ctx); got != nil {
		t.Fatalf("Load after clear = %v, want nil", got)
	}
}

func TestMem(t *testing.T) { roundTrip(t, sequencer.Mem()) }

func TestFile(t *testing.T) {
	h, err := sequencer.File(filepath.Join(t.TempDir(), "branches", "B_h"))
	if err != nil {
		t.Fatalf("File: %v", err)
	}
	roundTrip(t, h)
}

// TestSequencer backs B_h with a plain in-process variable via injected
// closures — the "no dedicated adapter needed" path.
func TestSequencer(t *testing.T) {
	var cell ranke.Id
	h := sequencer.New(
		func(context.Context) (ranke.Id, error) { return cell, nil },
		func(_ context.Context, id ranke.Id) error { cell = id; return nil },
	)
	roundTrip(t, h)
}
