// package: file / coordination
// type:    adapter
// job:     file-backed History storing the head-id timeline as a text file, one "id timestamp" line
// per entry, atomic-rename writes
// limits:  single-node only (-> a db timeline for distributed); stores ids only (-> ranke)
//
// Package file is a filesystem head-id timeline: it persists the sequence
// k₀…kₙ as a text file, one line per entry ("<id> <height> <rfc3339nano>"),
// the line's position being its revision. The timeline is loaded into memory
// at New and the whole file is rewritten atomically (temp + rename) on each
// Append, so a reader never sees a partial write.
package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flocko-motion/ranke-go"
)

var (
	errEmptyPath = errors.New("adapter/history/file.New: empty path")
	errNew       = errors.New("adapter/history/file.New")
	errNilID     = errors.New("adapter/history/file: nil id")
	errRevRange  = errors.New("adapter/history/file: revision out of range")
	errRange     = errors.New("adapter/history/file: range out of bounds")
	errMalformed = errors.New("adapter/history/file: malformed")
	errParse     = errors.New("adapter/history/file: parse")
	errIO        = errors.New("adapter/history/file: io")
)

// New returns a file-backed History persisting the timeline at path,
// loading any existing timeline into memory.
func New(path string) (ranke.History, error) {
	if path == "" {
		return nil, errEmptyPath
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("%w: mkdir: %w", errNew, err)
	}
	h := &history{path: path}
	if err := h.load(); err != nil {
		return nil, fmt.Errorf("%w: load: %w", errNew, err)
	}
	return h, nil
}

type history struct {
	mu    sync.Mutex
	path  string
	items []ranke.HistoryItem
}

func (h *history) Append(_ context.Context, id ranke.Id, height int, revision int) (ranke.HistoryItem, error) {
	if id == nil {
		return ranke.HistoryItem{}, errNilID
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	item := ranke.NewHistoryItem(id, revision, height, time.Now().UTC())
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

func (h *history) GetAtRevision(_ context.Context, revision int) (ranke.HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if revision < 0 || revision >= len(h.items) {
		return ranke.HistoryItem{}, fmt.Errorf("%w: revision %d out of range [0,%d)", errRevRange, revision, len(h.items))
	}
	return h.items[revision], nil
}

// GetBulk returns the half-open revision range [fromRevision, toExcludingRevision).
func (h *history) GetBulk(_ context.Context, fromRevision, toExcludingRevision int) ([]ranke.HistoryItem, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if fromRevision < 0 || toExcludingRevision > len(h.items) || fromRevision > toExcludingRevision {
		return nil, fmt.Errorf("%w: range [%d,%d) out of bounds [0,%d)", errRange, fromRevision, toExcludingRevision, len(h.items))
	}
	return append([]ranke.HistoryItem(nil), h.items[fromRevision:toExcludingRevision]...), nil
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
		idStr, rest, ok := strings.Cut(line, " ")
		if !ok {
			return fmt.Errorf("%w: line %d: %q", errMalformed, len(items), line)
		}
		heightStr, tsStr, ok := strings.Cut(rest, " ")
		if !ok {
			return fmt.Errorf("%w: line %d: %q", errMalformed, len(items), line)
		}
		id, err := ranke.ParseId(idStr)
		if err != nil {
			return fmt.Errorf("%w: id on line %d: %w", errParse, len(items), err)
		}
		height, err := strconv.Atoi(heightStr)
		if err != nil {
			return fmt.Errorf("%w: height on line %d: %w", errParse, len(items), err)
		}
		ts, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil {
			return fmt.Errorf("%w: time on line %d: %w", errParse, len(items), err)
		}
		// Revision is the entry's position in the file (entries are written in
		// order); height is stored per line.
		items = append(items, ranke.NewHistoryItem(id, len(items), height, ts))
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
		b.WriteString(strconv.Itoa(it.GetHeight()))
		b.WriteByte(' ')
		b.WriteString(it.GetTimestamp().UTC().Format(time.RFC3339Nano))
		b.WriteByte('\n')
	}
	tmp := h.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("%w: write tmp %s: %w", errIO, tmp, err)
	}
	if err := os.Rename(tmp, h.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: rename %s -> %s: %w", errIO, tmp, h.path, err)
	}
	return nil
}
