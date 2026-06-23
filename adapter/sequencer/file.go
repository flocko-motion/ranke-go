package sequencer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flocko-motion/ranke-go"
)

// File persists B_h as a single-line text file, written atomically
// (temp + rename) so a reader never sees a partial write.
func File(path string) (ranke.BranchTableHead, error) {
	if path == "" {
		return nil, errors.New("adapter/sequencer.File: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("adapter/sequencer.File: mkdir: %w", err)
	}
	return &fileHead{path: path}, nil
}

type fileHead struct {
	path string
}

func (h *fileHead) Load(context.Context) (ranke.Id, error) {
	data, err := os.ReadFile(h.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return ranke.ParseId(strings.TrimSpace(string(data)))
}

func (h *fileHead) Save(_ context.Context, id ranke.Id) error {
	if id == nil {
		if err := os.Remove(h.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	tmp := h.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(id.String()), 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, h.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, h.path, err)
	}
	return nil
}

func (h *fileHead) Close() error { return nil }
