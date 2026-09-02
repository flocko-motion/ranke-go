package fs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage/adaptertest"
)

// TestConformance runs the shared black-box Universe suite against the fs
// adapter, each universe rooted at a fresh temp dir.
func TestConformance(t *testing.T) {
	adaptertest.Run(t, func(t *testing.T) ranke.Universe {
		u, err := New(t.TempDir())
		if err != nil {
			t.Fatalf("fs.New: %v", err)
		}
		return u
	})
}

// TestStreamContent is fs-specific: it corrupts/truncates/extends the
// backing file on disk to exercise every termination branch of the
// streaming integrity check — a scenario only a file-backed adapter has.
func TestStreamContent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	u, err := New(dir)
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}

	payload := []byte("hello, ranke")
	h, err := ranke.HashContent(payload)
	if err != nil {
		t.Fatalf("HashContent: %v", err)
	}
	if err := ranke.PutContent(ctx, u, h, payload); err != nil {
		t.Fatalf("PutContent: %v", err)
	}
	size := uint64(len(payload))
	path := filepath.Join(dir, h.String())

	t.Run("happy path returns bytes + EOF", func(t *testing.T) {
		r, err := u.StreamContent(ctx, h, size)
		if err != nil {
			t.Fatalf("StreamContent: %v", err)
		}
		defer r.Close()
		got, err := io.ReadAll(r)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		if string(got) != string(payload) {
			t.Fatalf("payload mismatch: got %q want %q", got, payload)
		}
	})

	t.Run("overflow surfaces on final Read", func(t *testing.T) {
		if err := os.WriteFile(path, append([]byte{}, append(payload, []byte("EXTRA")...)...), 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		defer os.WriteFile(path, payload, 0o644)

		r, err := u.StreamContent(ctx, h, size)
		if err != nil {
			t.Fatalf("StreamContent: %v", err)
		}
		defer r.Close()
		_, err = io.ReadAll(r)
		if err == nil || !strings.Contains(err.Error(), "longer than expected") {
			t.Fatalf("expected 'longer than expected' error, got: %v", err)
		}
	})

	t.Run("truncation surfaces as error", func(t *testing.T) {
		if err := os.WriteFile(path, payload[:5], 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		defer os.WriteFile(path, payload, 0o644)

		r, err := u.StreamContent(ctx, h, size)
		if err != nil {
			t.Fatalf("StreamContent: %v", err)
		}
		defer r.Close()
		_, err = io.ReadAll(r)
		if err == nil || !strings.Contains(err.Error(), "truncated") {
			t.Fatalf("expected 'truncated' error, got: %v", err)
		}
	})

	// Same-length byte flip: only the final-block hash check catches
	// it. Consumer must receive an error instead of a clean EOF, and
	// must not have seen the full tampered payload.
	t.Run("same-size tamper held back at final block", func(t *testing.T) {
		tampered := []byte("ranke, hello")
		if uint64(len(tampered)) != size {
			t.Fatalf("test bug: tampered length %d != %d", len(tampered), size)
		}
		if err := os.WriteFile(path, tampered, 0o644); err != nil {
			t.Fatalf("setup: %v", err)
		}
		defer os.WriteFile(path, payload, 0o644)

		r, err := u.StreamContent(ctx, h, size)
		if err != nil {
			t.Fatalf("StreamContent: %v", err)
		}
		defer r.Close()
		got, err := io.ReadAll(r)
		if err == nil || !strings.Contains(err.Error(), "hash mismatch") {
			t.Fatalf("expected 'hash mismatch' error, got: %v (read %q)", err, got)
		}
		if len(got) >= len(payload) {
			t.Fatalf("consumer received the full tampered payload (%d bytes) — final block was not held back", len(got))
		}
	})
}

// TestBookmarksShareTheDirectory is `R-BMPREFIX`, and the collision it exists to rule
// out is real rather than theoretical: external content whose bytes are S([i, s]) is
// stored at H(c) = id_seq(i, s), which is a bookmark's own slot. One directory holds
// both here, so unprefixed the two would overwrite each other.
func TestBookmarksShareTheDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	u, err := New(dir)
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	hist := u.Bookmarks()

	// The content whose hash IS the slot: S([0, s]) stored under H(c).
	seed := []byte("prefix-collision-seed")
	collide, err := ranke.MarshalCBOR([]any{uint64(0), seed})
	if err != nil {
		t.Fatalf("marshal id_seq input: %v", err)
	}
	slot, err := ranke.IdSeq(0, seed)
	if err != nil {
		t.Fatalf("IdSeq: %v", err)
	}
	if err := ranke.PutContent(ctx, u, slot, collide); err != nil {
		t.Fatalf("put content at the slot's hash: %v", err)
	}

	record := []byte("a bookmark record, not content")
	if err := hist.Put(ctx, slot, record); err != nil {
		t.Fatalf("put bookmark: %v", err)
	}

	got, err := hist.Get(ctx, slot)
	if err != nil {
		t.Fatalf("get bookmark: %v", err)
	}
	if string(got) != string(record) {
		t.Fatalf("bookmark reads back as %q, want %q", got, record)
	}
	back, err := u.GetContents(ctx, []ranke.ContentRef{{Hash: slot, ContentSize: uint64(len(collide))}})
	if err != nil {
		t.Fatalf("get content: %v", err)
	}
	if string(back[0]) != string(collide) {
		t.Fatalf("content reads back as %q, want %q", back[0], collide)
	}
}

// TestBookmarkStoreRefusesANilSlot: a nil key names no slot, so it is refused rather
// than reaching the filesystem as an empty path.
func TestBookmarkStoreRefusesANilSlot(t *testing.T) {
	ctx := context.Background()
	u, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("fs.New: %v", err)
	}
	hist := u.Bookmarks()
	if _, err := hist.Get(ctx, nil); err == nil {
		t.Fatal("Get(nil) must be refused")
	}
	if err := hist.Put(ctx, nil, []byte("x")); err == nil {
		t.Fatal("Put(nil) must be refused")
	}
}
