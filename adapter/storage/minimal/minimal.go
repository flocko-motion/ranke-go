// package: minimal / persistence
// type:    adapter
// job:     the smallest possible Universe — a map[string][]byte behind the BlobStore seam
// limits:  ephemeral, unsynchronized illustration of the minimum an adapter must implement (-> adapter)
//
// Package minimal is the floor: Get/Put/Has plus Close over a map[string][]byte,
// with storage.NewBlobUniverse supplying the rest of ranke.Universe. A paper
// artifact, deliberately frozen — adapter/mem is the one that gets optimized.
package minimal

import (
	"context"
	"github.com/rankegraph/ranke-go"
	"github.com/rankegraph/ranke-go/adapter/storage"
	"sync"
)

// New returns a minimal in-memory Universe — an unsynchronized
// map[string][]byte behind the BlobStore seam.
func New() ranke.Universe {
	return storage.NewBlobUniverse(&store{m: make(map[string][]byte)})
}

type store struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *store) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.m[key]
	if !ok {
		return nil, ranke.ErrNotFound
	}
	return b, nil
}

func (s *store) Put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = data
	return nil
}

func (s *store) Has(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[key]
	return ok, nil
}

// Delete is refused: the floor claims no Delete capability, and answering a removal
// it does not make would let a lawful deletion read as done.
func (s *store) Delete(_ context.Context, _ string) error { return ranke.ErrUnsupported }

func (s *store) Close() error { return nil }

// Capabilities: the minimal floor exposes only get/put/has over an ephemeral
// map — Put overwrites; it claims nothing more.
func (s *store) Capabilities() ranke.Capabilities {
	return ranke.Capabilities{Overwrite: true}
}
