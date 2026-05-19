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
