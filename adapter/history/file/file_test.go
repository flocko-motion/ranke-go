package file_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/history/file"
)

func TestRoundTripAndReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "branches", "history")

	h, err := file.New(path)
	if err != nil {
		t.Fatalf("file.New: %v", err)
	}

	if n, _ := h.Len(ctx); n != 0 {
		t.Fatalf("Len (empty) = %d, want 0", n)
	}

	ids := make([]ranke.Id, 2)
	for i := range ids {
		id, err := ranke.HashContent([]byte{byte('x' + i)})
		if err != nil {
			t.Fatalf("HashContent: %v", err)
		}
		ids[i] = id
		item, err := h.Append(ctx, id, i, i)
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
		if item.GetHeight() != i || item.GetTimestamp().IsZero() {
			t.Fatalf("Append = {h=%d, ts=%v}, want {h=%d, non-zero ts}", item.GetHeight(), item.GetTimestamp(), i)
		}
	}

	// Reopen a fresh handle at the same path: the timeline must survive,
	// heights and timestamps intact.
	reopened, err := file.New(path)
	if err != nil {
		t.Fatalf("file.New (reopen): %v", err)
	}
	n, err := reopened.Len(ctx)
	if err != nil || n != 2 {
		t.Fatalf("Len after reopen = %d (%v), want 2", n, err)
	}
	list, err := reopened.GetBulk(ctx, 0, n)
	if err != nil {
		t.Fatalf("GetBulk after reopen: %v", err)
	}
	for i, want := range ids {
		if !list[i].GetId().Equal(want) || list[i].GetHeight() != i {
			t.Fatalf("GetBulk[%d] = {%v, h=%d}, want {%v, h=%d}", i, list[i].GetId(), list[i].GetHeight(), want, i)
		}
		if list[i].GetTimestamp().IsZero() {
			t.Fatalf("GetBulk[%d] timestamp not persisted", i)
		}
	}
	latest, _ := reopened.Latest(ctx)
	if !latest.GetId().Equal(ids[1]) || latest.GetHeight() != 1 {
		t.Fatalf("Latest after reopen = {%v, h=%d}, want {%v, h=1}", latest.GetId(), latest.GetHeight(), ids[1])
	}
}
