// package: file / coordination
// type:    adapter
// job:     file-backed BranchTableHead storing B_h as a single-line text file with atomic-rename writes
// limits:  single-node only (-> a db/etcd sequencer for distributed); stores only the Id (-> ranke)
//
// Package file is a filesystem sequencer: it persists B_h as a single-line
// text file, written atomically (temp + rename) so a reader never sees a
// partial write.
package file

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flocko-motion/ranke-go"
)

// New returns a file-backed BranchTableHead persisting B_h at path.
func New(path string) (ranke.BranchTableHead, error) {
	if path == "" {
		return nil, errors.New("adapter/sequencer/file.New: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("adapter/sequencer/file.New: mkdir: %w", err)
	}
	return &head{path: path}, nil
}

type head struct {
	path string
}

func (h *head) Load(context.Context) (ranke.Id, error) {
	data, err := os.ReadFile(h.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return ranke.ParseId(strings.TrimSpace(string(data)))
}

func (h *head) Save(_ context.Context, id ranke.Id) error {
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

func (h *head) Close() error { return nil }
