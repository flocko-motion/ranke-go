// package: file / coordination
// type:    adapter
// job:     file-backed History storing the head-id timeline as a text file, one "id timestamp" line per entry, atomic-rename writes
// limits:  single-node only (-> a db timeline for distributed); stores ids only (-> ranke)
//
// Package file is a filesystem head-id timeline: it persists the sequence
// k₀…kₙ as a text file, one line per entry ("<id> <rfc3339nano>"), the
// line's position being its height. The timeline is loaded into memory at
// New and the whole file is rewritten atomically (temp + rename) on each
// Append, so a reader never sees a partial write.
package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flocko-motion/ranke-go"
)

// New returns a file-backed History persisting the timeline at path,
// loading any existing timeline into memory.
func New(path string) (ranke.History, error) {
	if path == "" {
		return nil, errors.New("adapter/history/file.New: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("adapter/history/file.New: mkdir: %w", err)
	}
	h := &history{path: path}
	if err := h.load(); err != nil {
		return nil, fmt.Errorf("adapter/history/file.New: load: %w", err)
	}
	return h, nil
}

type history struct {
	mu    sync.Mutex
	path  string
	items []ranke.HistoryItem
}

func (h *history) Append(_ context.Context, id ranke.Id) (ranke.HistoryItem, error) {
	if id == nil {
		return ranke.HistoryItem{}, fmt.Errorf("adapter/history/file: nil id")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	item := ranke.NewHistoryItem(id, len(h.items), time.Now().UTC())
	next := append(append([]ranke.HistoryItem(nil), h.items...), item)
	if err := h.persist(next); err != nil {
		return ranke.HistoryItem{}, err
	}
	h.items = next
	return item, nil
}

func (h *history) Latest(_ context.Context) (ranke.HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.items) == 0 {
		return ranke.HistoryItem{}, nil
	}
	return h.items[len(h.items)-1], nil
}

func (h *history) Get(_ context.Context, i int) (ranke.HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i < 0 || i >= len(h.items) {
		return ranke.HistoryItem{}, fmt.Errorf("adapter/history/file: index %d out of range [0,%d)", i, len(h.items))
	}
	return h.items[i], nil
}

// GetBulk returns the half-open range [from, to).
func (h *history) GetBulk(_ context.Context, from, to int) ([]ranke.HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if from < 0 || to > len(h.items) || from > to {
		return nil, fmt.Errorf("adapter/history/file: range [%d,%d) out of bounds [0,%d)", from, to, len(h.items))
	}
	return append([]ranke.HistoryItem(nil), h.items[from:to]...), nil
}

func (h *history) Len(_ context.Context) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.items), nil
}

func (h *history) Close() error { return nil }

// load reads the timeline file into h.items. A missing file is an empty
// timeline. Called once at New, before the History is handed out.
func (h *history) load() error {
	data, err := os.ReadFile(h.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var items []ranke.HistoryItem
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idStr, tsStr, ok := strings.Cut(line, " ")
		if !ok {
			return fmt.Errorf("adapter/history/file: malformed line %d: %q", len(items), line)
		}
		id, err := ranke.ParseId(idStr)
		if err != nil {
			return fmt.Errorf("adapter/history/file: parse id on line %d: %w", len(items), err)
		}
		ts, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			return fmt.Errorf("adapter/history/file: parse time on line %d: %w", len(items), err)
		}
		// Height is the entry's position in the file, which equals the
		// height Append assigned (entries are written in order).
		items = append(items, ranke.NewHistoryItem(id, len(items), ts))
	}
	h.items = items
	return nil
}

// persist rewrites the whole timeline atomically (temp + rename).
func (h *history) persist(items []ranke.HistoryItem) error {
	var b strings.Builder
	for _, it := range items {
		b.WriteString(it.GetId().String())
		b.WriteByte(' ')
		b.WriteString(it.GetTimestamp().UTC().Format(time.RFC3339Nano))
		b.WriteByte('\n')
	}
	tmp := h.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, h.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, h.path, err)
	}
	return nil
}
