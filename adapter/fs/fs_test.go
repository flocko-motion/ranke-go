package fs

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flocko-motion/ranke-go"
	"github.com/flocko-motion/ranke-go/adapter/adaptertest"
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
