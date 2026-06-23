// package: ranke / branch_table_head
// type:    io
// job:     file-backed BranchTableHead storing B_h as a single-line text file with atomic rename writes
// limits:  no in-memory variant (-> branch_table_head_mem); does not interpret the Id (-> branch_table)
package ranke

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// NewFsBranchTableHead persists B_h as a single-line text file.
func NewFsBranchTableHead(path string) (BranchTableHead, error) {
	if path == "" {
		return nil, errors.New("ranke.NewFsBranchTableHead: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("ranke.NewFsBranchTableHead: mkdir: %w", err)
	}
	return &fsBranchTableHead{path: path}, nil
}

type fsBranchTableHead struct {
	path string
}

func (b *fsBranchTableHead) Load(_ context.Context) (Id, error) {
	data, err := os.ReadFile(b.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParseId(strings.TrimSpace(string(data)))
}

func (b *fsBranchTableHead) Save(_ context.Context, id Id) error {
	if id == nil {
		if err := os.Remove(b.path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	return atomicWrite(b.path, []byte(id.String()))
}

func (b *fsBranchTableHead) Close() error { return nil }

// atomicWrite writes data to path via a temp file + rename, so a reader
// never sees a partially-written file.
func atomicWrite(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}
